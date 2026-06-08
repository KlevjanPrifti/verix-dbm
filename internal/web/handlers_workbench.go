package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/postgres"
	"verix-dbm/internal/redisdb"
	"verix-dbm/internal/store"
)

// workbench is the DataGrip-style single-page shell: a persistent Database
// Explorer on the left and a tabbed workspace on the right.
func (s *Server) workbench(w http.ResponseWriter, r *http.Request) {
	v := s.newView(r, "workbench")
	conns, err := s.st.ListConnections(r.Context())
	if err != nil {
		v.Error = err.Error()
	}
	v.Connections = conns
	s.rnd.page(w, "workbench", v)
}

// Explorer tree fragments

type explorerData struct {
	Conn    store.Connection
	Schemas []postgres.Schema
	Err     string
}

// explorer returns a connection's top-level children: schemas+tables for
// Postgres, or a keyspace entry for Redis. Loaded lazily when the node expands.
func (s *Server) explorer(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	d := explorerData{Conn: c}
	if c.Kind == "redis" {
		s.rnd.partial(w, "explorerRedis", d)
		return
	}
	pool, err := s.reg.PG(r.Context(), c)
	if err != nil {
		d.Err = "connect: " + err.Error()
		s.rnd.partial(w, "explorerPG", d)
		return
	}
	d.Schemas, err = postgres.Schemas(r.Context(), pool)
	if err != nil {
		d.Err = err.Error()
	}
	s.rnd.partial(w, "explorerPG", d)
}

type nodeListData struct {
	Conn    store.Connection
	Schema  string
	Table   string
	Columns []postgres.Column
	Indexes []postgres.Index
	Keys    []postgres.Key
	Err     string
}

func (s *Server) pgColumns(w http.ResponseWriter, r *http.Request) {
	c, pool, ok := s.pgPoolFor(w, r)
	if !ok {
		return
	}
	d := nodeListData{Conn: c, Schema: r.URL.Query().Get("schema"), Table: r.URL.Query().Get("table")}
	cols, err := postgres.Columns(r.Context(), pool, d.Schema, d.Table)
	if err != nil {
		d.Err = err.Error()
	}
	d.Columns = cols
	s.rnd.partial(w, "nodeColumns", d)
}

func (s *Server) pgIndexes(w http.ResponseWriter, r *http.Request) {
	c, pool, ok := s.pgPoolFor(w, r)
	if !ok {
		return
	}
	d := nodeListData{Conn: c, Schema: r.URL.Query().Get("schema"), Table: r.URL.Query().Get("table")}
	ix, err := postgres.Indexes(r.Context(), pool, d.Schema, d.Table)
	if err != nil {
		d.Err = err.Error()
	}
	d.Indexes = ix
	s.rnd.partial(w, "nodeIndexes", d)
}

func (s *Server) pgKeys(w http.ResponseWriter, r *http.Request) {
	c, pool, ok := s.pgPoolFor(w, r)
	if !ok {
		return
	}
	d := nodeListData{Conn: c, Schema: r.URL.Query().Get("schema"), Table: r.URL.Query().Get("table")}
	keys, err := postgres.Keys(r.Context(), pool, d.Schema, d.Table)
	if err != nil {
		d.Err = err.Error()
	}
	d.Keys = keys
	s.rnd.partial(w, "nodeKeys", d)
}

// Data grid tab

type gridData struct {
	Conn     store.Connection
	Schema   string
	Table    string
	Where    string
	Order    string
	Page     int
	Result   *postgres.Result
	Err      string
	CSRF     string
	ReadOnly bool
}

func (s *Server) gridView(w http.ResponseWriter, r *http.Request) {
	if !s.auth.CheckCSRF(r) {
		http.Error(w, "bad csrf", http.StatusForbidden)
		return
	}
	u, _ := auth.FromContext(r.Context())
	c, pool, ok := s.pgPoolFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	d := gridData{
		Conn:     c,
		Schema:   q.Get("schema"),
		Table:    q.Get("table"),
		Where:    q.Get("where"),
		Order:    q.Get("order"),
		CSRF:     u.CSRF,
		ReadOnly: c.ReadOnly || !u.Write,
	}
	d.Page, _ = strconv.Atoi(q.Get("page"))
	if d.Page < 0 {
		d.Page = 0
	}
	// Always browse read-only: the where/order fragments are raw user SQL.
	res, err := postgres.BrowseWhere(r.Context(), pool, d.Schema, d.Table, d.Where, d.Order, browseLimit, d.Page*browseLimit, true)
	if err != nil {
		d.Err = err.Error()
	}
	d.Result = res
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "pg_browse", Detail: d.Schema + "." + d.Table, Success: err == nil})
	s.rnd.partial(w, "grid", d)
}

// Console tabs

type consoleData struct {
	Conn     store.Connection
	CSRF     string
	ReadOnly bool
}

func (s *Server) consoleTab(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	d := consoleData{Conn: c, CSRF: u.CSRF, ReadOnly: c.ReadOnly || !u.Write}
	if c.Kind == "redis" {
		s.redisTabFragment(w, r, c, d)
		return
	}
	s.rnd.partial(w, "consolePG", d)
}

func (s *Server) redisTabFragment(w http.ResponseWriter, r *http.Request, c store.Connection, d consoleData) {
	rd := redisViewData{Match: "*", ConnID: c.ID}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		rd.Err = "connect: " + err.Error()
	} else if page, e := redisdb.Scan(r.Context(), client, rd.Match, 0, 100); e != nil {
		rd.Err = e.Error()
	} else {
		rd.Page = page
	}
	s.rnd.partial(w, "redisTab", map[string]any{"Console": d, "Redis": rd})
}

// Connection edit (Properties)

// editConnForm renders the prefilled "Data Sources & Drivers" modal for an
// existing connection (loaded via HTMX when the user picks Properties).
func (s *Server) editConnForm(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	s.rnd.partial(w, "connEditModal", map[string]any{"Conn": c, "CSRF": u.CSRF})
}

func (s *Server) updateConnection(w http.ResponseWriter, r *http.Request) {
	if !s.auth.CheckCSRF(r) {
		http.Error(w, "bad csrf", http.StatusForbidden)
		return
	}
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	port, _ := strconv.Atoi(r.FormValue("port"))
	c.Name = r.FormValue("name")
	c.Kind = r.FormValue("kind")
	c.Host = r.FormValue("host")
	c.Port = port
	c.DBName = r.FormValue("dbname")
	c.Username = r.FormValue("username")
	c.Options = r.FormValue("options")
	c.ReadOnly = r.FormValue("readonly") == "on"

	updatePw := false
	if pw := r.FormValue("password"); pw != "" {
		enc, err := s.box.Encrypt(pw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		c.PasswordEnc = enc
		updatePw = true
	}
	if err := s.st.UpdateConnection(r.Context(), c, updatePw); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.reg.Forget(c.ID) // creds/host may have changed; drop cached pool
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "update_connection", Detail: c.Name, Success: true})
	http.Redirect(w, r, "/", http.StatusFound)
}

// Connection test

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	if !s.auth.CheckCSRF(r) {
		http.Error(w, "bad csrf", http.StatusForbidden)
		return
	}
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	port, _ := strconv.Atoi(r.FormValue("port"))
	c := store.Connection{
		Kind:     r.FormValue("kind"),
		Host:     r.FormValue("host"),
		Port:     port,
		DBName:   r.FormValue("dbname"),
		Username: r.FormValue("username"),
		Options:  r.FormValue("options"),
	}
	pw := r.FormValue("password")

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	var err error
	if c.Kind == "redis" {
		err = pingRedis(ctx, c, pw)
	} else {
		err = pingPG(ctx, c, pw)
	}
	if err != nil {
		s.rnd.partial(w, "testResult", map[string]any{"Err": err.Error()})
		return
	}
	s.rnd.partial(w, "testResult", map[string]any{"OK": true})
}

func pingPG(ctx context.Context, c store.Connection, pw string) error {
	cfg, err := pgxpool.ParseConfig(c.DSN(pw))
	if err != nil {
		return err
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	return pool.Ping(ctx)
}

func pingRedis(ctx context.Context, c store.Connection, pw string) error {
	dbNum := 0
	if c.DBName != "" {
		if n, e := strconv.Atoi(c.DBName); e == nil {
			dbNum = n
		}
	}
	user := c.Username
	if user == "" {
		user = "default"
	}
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", c.Host, c.Port),
		Username: user,
		Password: pw,
		DB:       dbNum,
	})
	defer client.Close()
	return client.Ping(ctx).Err()
}

// helpers

// pgPoolFor resolves the URL's connection and its Postgres pool, writing an
// error fragment and returning ok=false on failure.
func (s *Server) pgPoolFor(w http.ResponseWriter, r *http.Request) (store.Connection, *pgxpool.Pool, bool) {
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return store.Connection{}, nil, false
	}
	pool, err := s.reg.PG(r.Context(), c)
	if err != nil {
		s.rnd.partial(w, "fragErr", map[string]any{"Err": "connect: " + err.Error()})
		return c, nil, false
	}
	return c, pool, true
}

var _ = strings.TrimSpace
