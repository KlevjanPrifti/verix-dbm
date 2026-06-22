package web

// JSON handlers for the MongoDB engine. Like the Redis handlers, MongoDB lives
// outside the dbsql.Engine interface and is reached through the registry's Mongo
// getter and the internal/mongodb helpers. The browse endpoints are GET (no
// CSRF); the command console is POST + CSRF with a read-only allowlist and a
// confirm gate for destructive commands, mirroring apiRedisCmd.

import (
	"net/http"
	"strconv"
	"strings"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/mongodb"
	"verix-dbm/internal/store"
)

// apiMongoDocs returns a page of documents from a collection, with optional
// JSON filter / sort / projection (relaxed extended JSON) and page/size paging.
func (s *Server) apiMongoDocs(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	q := r.URL.Query()
	db, coll := q.Get("db"), q.Get("coll")
	if db == "" || coll == "" {
		apiErr(w, http.StatusBadRequest, "db and coll are required")
		return
	}
	size, _ := strconv.Atoi(q.Get("size"))
	if size <= 0 {
		size = 50
	}
	pageNum, _ := strconv.Atoi(q.Get("page"))
	if pageNum < 0 {
		pageNum = 0
	}
	// $where/$function/$accumulator run server-side JavaScript: a non-admin could
	// use them to DoS the server or read fields a projection hides. The command
	// console already gates this kind of access; mirror it on the browse filter.
	u, _ := auth.FromContext(r.Context())
	if !u.Admin && mongodb.UsesServerJS(q.Get("filter"), q.Get("sort"), q.Get("projection")) {
		apiErr(w, http.StatusForbidden, "server-side JavaScript ($where/$function/$accumulator) requires admin")
		return
	}
	client, err := s.reg.Mongo(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "connect: "+err.Error())
		return
	}
	res, err := mongodb.Find(r.Context(), client, db, coll,
		q.Get("filter"), q.Get("sort"), q.Get("projection"), int64(size), int64(pageNum*size))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"docs": res.Docs, "hasMore": res.HasMore, "page": pageNum})
}

// apiMongoIndexes lists a collection's indexes.
func (s *Server) apiMongoIndexes(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	q := r.URL.Query()
	db, coll := q.Get("db"), q.Get("coll")
	if db == "" || coll == "" {
		apiErr(w, http.StatusBadRequest, "db and coll are required")
		return
	}
	client, err := s.reg.Mongo(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "connect: "+err.Error())
		return
	}
	ix, err := mongodb.Indexes(r.Context(), client, db, coll)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexes": ix})
}

// apiMongoCmd runs a database command supplied as relaxed extended JSON. Reads
// are allow-listed for read-only users; destructive commands need admin + an
// explicit confirmation, exactly like the Redis command console.
func (s *Server) apiMongoCmd(w http.ResponseWriter, r *http.Request) {
	if !s.auth.CheckCSRF(r) {
		apiErr(w, http.StatusForbidden, "bad csrf")
		return
	}
	u, _ := auth.FromContext(r.Context())
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	var in struct {
		DB      string `json:"db"`
		Cmd     string `json:"cmd"`
		Confirm bool   `json:"confirm"`
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if strings.TrimSpace(in.DB) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"error": "a database is required"})
		return
	}
	name, err := mongodb.CommandName(in.Cmd)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	readOnly := c.ReadOnly || !s.access(r.Context(), u, c).Write
	if readOnly && !mongodb.ReadAllowed(name) {
		writeJSON(w, http.StatusOK, map[string]any{"error": "read-only: command '" + name + "' is not permitted"})
		return
	}
	if mongodb.NeedsConfirm(name) {
		if !u.Admin {
			writeJSON(w, http.StatusOK, map[string]any{"error": "admin required for '" + name + "'"})
			return
		}
		if !in.Confirm {
			writeJSON(w, http.StatusOK, map[string]any{"needConfirm": true, "cmd": in.Cmd})
			return
		}
	}
	client, err := s.reg.Mongo(r.Context(), c)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	out, err := mongodb.RunCommand(r.Context(), client, in.DB, in.Cmd)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "mongo_cmd", Detail: auditDetail(in.DB + ": " + in.Cmd), Success: err == nil})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"out": out})
}
