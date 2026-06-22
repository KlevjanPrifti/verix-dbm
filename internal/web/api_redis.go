package web

// Redis/Valkey endpoints: keyspace SCAN browse, type-aware value viewer, and the
// command console with its read-only allowlist + destructive-command confirm
// gate. Split out of api.go; mounted under /api.

import (
	"net/http"
	"strconv"
	"strings"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/redisdb"
	"verix-dbm/internal/store"
)

// Redis

func (s *Server) apiRedisKeys(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "connect: "+err.Error())
		return
	}
	cursor, _ := strconv.ParseUint(r.URL.Query().Get("cursor"), 10, 64)
	page, err := redisdb.Scan(r.Context(), client, orStar(r.URL.Query().Get("match")), cursor, 100)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": page.Keys, "cursor": page.Cursor})
}

func (s *Server) apiRedisValue(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "connect: "+err.Error())
		return
	}
	val, err := redisdb.Get(r.Context(), client, r.URL.Query().Get("key"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": val})
}

func (s *Server) apiRedisCmd(w http.ResponseWriter, r *http.Request) {
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
		Cmd     string `json:"cmd"`
		Confirm bool   `json:"confirm"`
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	args := redisdb.ParseArgs(in.Cmd)
	if len(args) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"error": "empty command"})
		return
	}
	readOnly := c.ReadOnly || !s.access(r.Context(), u, c).Write
	cmd := strings.ToLower(args[0])
	if readOnly && !redisReadAllow[cmd] {
		writeJSON(w, http.StatusOK, map[string]any{"error": "read-only: command '" + cmd + "' is not permitted"})
		return
	}
	if redisdb.NeedsConfirm(args) {
		if !u.Admin {
			writeJSON(w, http.StatusOK, map[string]any{"error": "admin required for '" + cmd + "'"})
			return
		}
		if !in.Confirm {
			writeJSON(w, http.StatusOK, map[string]any{"needConfirm": true, "cmd": in.Cmd})
			return
		}
	}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	res, err := redisdb.Command(r.Context(), client, args)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "redis_cmd", Detail: auditDetail(in.Cmd), Success: err == nil})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"out": res})
}
