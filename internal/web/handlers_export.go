package web

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/postgres"
	"verix-dbm/internal/store"
)

// exportTable streams a table's rows (honouring the grid's WHERE/ORDER filters)
// as a CSV or JSON download. It reuses the read-only browse path, so the same
// 1000-row cap applies — the download is a convenience snapshot, not a dump.
func (s *Server) exportTable(w http.ResponseWriter, r *http.Request) {
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
	schema, table := q.Get("schema"), q.Get("table")
	format := q.Get("format")
	if format != "json" {
		format = "csv"
	}
	if serverSideBlocked(u, q.Get("where"), q.Get("order")) {
		http.Error(w, serverSideBlockedMsg, http.StatusForbidden)
		return
	}

	res, err := postgres.BrowseWhere(r.Context(), pool, schema, table, q.Get("where"), q.Get("order"), 1000, 0, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "pg_export_" + format, Detail: schema + "." + table, Success: true})

	fname := sanitizeFilename(schema+"_"+table) + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)

	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		out := make([]map[string]string, 0, len(res.Rows))
		for _, row := range res.Rows {
			obj := make(map[string]string, len(res.Columns))
			for i, col := range res.Columns {
				if i < len(row) {
					obj[col] = row[i]
				}
			}
			out = append(out, obj)
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	cw.Write(res.Columns)
	for _, row := range res.Rows {
		safe := make([]string, len(row))
		for i, v := range row {
			safe[i] = csvSafe(v)
		}
		cw.Write(safe)
	}
	cw.Flush()
}

// csvSafe neutralises spreadsheet formula injection: a cell that a spreadsheet
// would interpret as a formula (starts with = + - @, or a tab/CR) is prefixed
// with a single quote so it's imported as literal text rather than executed.
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// sanitizeFilename keeps the download name to a safe subset of characters.
func sanitizeFilename(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}
