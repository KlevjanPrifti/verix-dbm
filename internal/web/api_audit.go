package web

// Audit log endpoints (list + full streaming export) and the credential
// re-encryption admin action. Split out of api.go; mounted under /api.

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/store"
)

// Audit

type auditDTO struct {
	TS      string `json:"ts"`
	User    string `json:"user"`
	ConnID  int64  `json:"connId"`
	Action  string `json:"action"`
	Detail  string `json:"detail"`
	Success bool   `json:"success"`
}

func (s *Server) apiAudit(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		apiErr(w, http.StatusForbidden, "admin required")
		return
	}
	rows, err := s.st.ListAudit(r.Context(), 200)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]auditDTO, 0, len(rows))
	for _, a := range rows {
		out = append(out, auditDTO{
			TS: a.TS.Format(time.RFC3339), User: a.User, ConnID: a.ConnID,
			Action: a.Action, Detail: a.Detail, Success: a.Success,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out})
}

// apiAuditExport streams the FULL audit log as a download for SIEM ingestion or
// forensics (admin only). format=jsonl (default) emits one JSON object per line;
// format=csv emits a header plus rows. It streams via IterAudit so a large log
// isn't buffered in memory.
func (s *Server) apiAuditExport(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		apiErr(w, http.StatusForbidden, "admin required")
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "jsonl"
	}
	switch format {
	case "jsonl":
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="audit.jsonl"`)
		enc := json.NewEncoder(w)
		_ = s.st.IterAudit(r.Context(), func(a store.Audit) error {
			return enc.Encode(auditDTO{
				TS: a.TS.Format(time.RFC3339), User: a.User, ConnID: a.ConnID,
				Action: a.Action, Detail: a.Detail, Success: a.Success,
			})
		})
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"ts", "user", "conn_id", "action", "detail", "success"})
		_ = s.st.IterAudit(r.Context(), func(a store.Audit) error {
			// user/action/detail are attacker-influenced (emails, SQL text, grant
			// subjects, connection names), so neutralize spreadsheet formula injection
			// before they reach a CSV a forensic analyst may open in Excel/Sheets.
			return cw.Write([]string{
				a.TS.Format(time.RFC3339), csvSafe(a.User), strconv.FormatInt(a.ConnID, 10),
				csvSafe(a.Action), csvSafe(a.Detail), strconv.FormatBool(a.Success),
			})
		})
		cw.Flush()
	default:
		apiErr(w, http.StatusBadRequest, "format must be 'jsonl' or 'csv'")
	}
}

// apiReencrypt re-encrypts every stored credential under the current primary
// key, the second half of a non-destructive key rotation (admin only). It is
// safe to run repeatedly: connections already on the primary key are skipped.
func (s *Server) apiReencrypt(w http.ResponseWriter, r *http.Request) {
	u, ok := s.apiRequireAdmin(w, r)
	if !ok {
		return
	}
	conns, err := s.st.ListConnections(r.Context())
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var checked, rewritten, failed int
	for _, c := range conns {
		if c.PasswordEnc == "" {
			continue
		}
		checked++
		enc, changed, err := s.box.Reencrypt(c.PasswordEnc)
		if err != nil {
			failed++
			continue
		}
		if !changed {
			continue
		}
		if err := s.st.UpdatePasswordEnc(r.Context(), c.ID, enc); err != nil {
			failed++
			continue
		}
		s.reg.Forget(c.ID) // drop any cached pool so it re-decrypts next use
		rewritten++
	}
	s.st.AddAudit(r.Context(), store.Audit{
		User: u.Email, Action: "reencrypt",
		Detail:  "key=" + s.box.PrimaryID() + " checked=" + strconv.Itoa(checked) + " rewritten=" + strconv.Itoa(rewritten) + " failed=" + strconv.Itoa(failed),
		Success: failed == 0,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"primaryKey": s.box.PrimaryID(), "checked": checked, "rewritten": rewritten, "failed": failed,
	})
}
