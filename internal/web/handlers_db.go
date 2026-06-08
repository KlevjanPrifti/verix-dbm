package web

import (
	"net/http"
	"strconv"
	"strings"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/postgres"
	"verix-dbm/internal/redisdb"
	"verix-dbm/internal/store"
)

const browseLimit = 100

// Postgres

type pgViewData struct {
	DB      string
	Schemas []postgres.Schema
	Schema  string
	Table   string
	Page    int
	Result  *postgres.Result
	Err     string
}

func (s *Server) pgView(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	v := s.newView(r, "conn")
	v.HasConn = true
	v.Conn = c

	pool, err := s.reg.PG(r.Context(), c)
	if err != nil {
		v.Error = "connect: " + err.Error()
		s.rnd.page(w, "pg", v)
		return
	}
	d := pgViewData{Schema: r.URL.Query().Get("schema"), Table: r.URL.Query().Get("table")}
	d.DB, _ = postgres.DatabaseName(r.Context(), pool)
	d.Schemas, err = postgres.Schemas(r.Context(), pool)
	if err != nil {
		d.Err = err.Error()
	}
	if d.Schema != "" && d.Table != "" {
		d.Page, _ = strconv.Atoi(r.URL.Query().Get("page"))
		if d.Page < 0 {
			d.Page = 0
		}
		res, err := postgres.Browse(r.Context(), pool, d.Schema, d.Table, browseLimit, d.Page*browseLimit)
		if err != nil {
			d.Err = err.Error()
		}
		d.Result = res
	}
	v.Data = d
	s.rnd.page(w, "pg", v)
}

type queryResultData struct {
	Result      *postgres.Result
	Err         string
	NeedConfirm bool
	SQL         string
	ReadOnly    bool
	ConnID      int64
	CSRF        string
}

func (s *Server) pgQuery(w http.ResponseWriter, r *http.Request) {
	if !s.auth.CheckCSRF(r) {
		http.Error(w, "bad csrf", http.StatusForbidden)
		return
	}
	u, _ := auth.FromContext(r.Context())
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	sql := strings.TrimSpace(r.FormValue("sql"))
	confirm := r.FormValue("confirm") == "on"
	readOnly := c.ReadOnly || !u.Write

	out := queryResultData{SQL: sql, ConnID: c.ID, ReadOnly: readOnly, CSRF: u.CSRF}
	if sql == "" {
		out.Err = "empty statement"
		s.rnd.partial(w, "queryResult", out)
		return
	}
	if !readOnly && postgres.NeedsConfirm(sql) && !confirm {
		out.NeedConfirm = true
		s.rnd.partial(w, "queryResult", out)
		return
	}

	pool, err := s.reg.PG(r.Context(), c)
	if err != nil {
		out.Err = "connect: " + err.Error()
		s.rnd.partial(w, "queryResult", out)
		return
	}
	res, err := postgres.Query(r.Context(), pool, sql, readOnly)
	if err != nil {
		out.Err = err.Error()
	}
	out.Result = res
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "pg_query", Detail: truncate(sql, 500), Success: err == nil})
	s.rnd.partial(w, "queryResult", out)
}

// Redis / Valkey

type redisViewData struct {
	Match  string
	Page   *redisdb.KeyPage
	Err    string
	ConnID int64
}

func (s *Server) redisView(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	v := s.newView(r, "conn")
	v.HasConn = true
	v.Conn = c

	d := redisViewData{Match: orStar(r.URL.Query().Get("match")), ConnID: c.ID}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		v.Error = "connect: " + err.Error()
		v.Data = d
		s.rnd.page(w, "redis", v)
		return
	}
	page, err := redisdb.Scan(r.Context(), client, d.Match, 0, 100)
	if err != nil {
		d.Err = err.Error()
	}
	d.Page = page
	v.Data = d
	s.rnd.page(w, "redis", v)
}

func (s *Server) redisKeys(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		s.rnd.partial(w, "redisKeys", redisViewData{Err: err.Error(), ConnID: c.ID})
		return
	}
	cursor, _ := strconv.ParseUint(r.URL.Query().Get("cursor"), 10, 64)
	d := redisViewData{Match: orStar(r.URL.Query().Get("match")), ConnID: c.ID}
	page, err := redisdb.Scan(r.Context(), client, d.Match, cursor, 100)
	if err != nil {
		d.Err = err.Error()
	}
	d.Page = page
	s.rnd.partial(w, "redisKeys", d)
}

func (s *Server) redisValue(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		s.rnd.partial(w, "redisValue", map[string]any{"Err": err.Error()})
		return
	}
	val, err := redisdb.Get(r.Context(), client, r.URL.Query().Get("key"))
	if err != nil {
		s.rnd.partial(w, "redisValue", map[string]any{"Err": err.Error()})
		return
	}
	s.rnd.partial(w, "redisValue", map[string]any{"Value": val})
}

var redisReadAllow = map[string]bool{
	"get": true, "mget": true, "type": true, "ttl": true, "pttl": true, "scan": true,
	"hget": true, "hgetall": true, "hkeys": true, "hlen": true, "lrange": true, "llen": true,
	"smembers": true, "scard": true, "zrange": true, "zcard": true, "exists": true,
	"strlen": true, "info": true, "dbsize": true, "ping": true, "memory": true, "object": true,
}

func (s *Server) redisCmd(w http.ResponseWriter, r *http.Request) {
	if !s.auth.CheckCSRF(r) {
		http.Error(w, "bad csrf", http.StatusForbidden)
		return
	}
	u, _ := auth.FromContext(r.Context())
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	args := redisdb.ParseArgs(r.FormValue("cmd"))
	confirm := r.FormValue("confirm") == "on"
	out := map[string]any{"Cmd": r.FormValue("cmd"), "ConnID": c.ID, "CSRF": u.CSRF}

	if len(args) == 0 {
		out["Err"] = "empty command"
		s.rnd.partial(w, "redisCmdResult", out)
		return
	}
	readOnly := c.ReadOnly || !u.Write
	cmd := strings.ToLower(args[0])
	if readOnly && !redisReadAllow[cmd] {
		out["Err"] = "read-only: command '" + cmd + "' is not permitted"
		s.rnd.partial(w, "redisCmdResult", out)
		return
	}
	if redisdb.NeedsConfirm(args) {
		if !u.Admin {
			out["Err"] = "admin required for '" + cmd + "'"
			s.rnd.partial(w, "redisCmdResult", out)
			return
		}
		if !confirm {
			out["NeedConfirm"] = true
			s.rnd.partial(w, "redisCmdResult", out)
			return
		}
	}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		out["Err"] = err.Error()
		s.rnd.partial(w, "redisCmdResult", out)
		return
	}
	res, err := redisdb.Command(r.Context(), client, args)
	if err != nil {
		out["Err"] = err.Error()
	} else {
		out["Out"] = res
	}
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "redis_cmd", Detail: truncate(r.FormValue("cmd"), 500), Success: err == nil})
	s.rnd.partial(w, "redisCmdResult", out)
}

func orStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
