package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/qerr"
)

type queryLogJSON struct {
	ID            int64     `json:"id"`
	Connection    string    `json:"connection"`
	KeyID         string    `json:"key_id,omitempty"`
	Tool          string    `json:"tool"`
	Query         string    `json:"query"`
	Rows          int64     `json:"rows"`
	DurationMS    int64     `json:"duration_ms"`
	ErrorCategory string    `json:"error_category,omitempty"`
	At            time.Time `json:"at"`
}

func (a *api) queryLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := core.QueryLogFilter{Connection: q.Get("connection")}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			a.fail(w, r, qerr.New(qerr.Invalid, "limit must be a number"))
			return
		}
		f.Limit = n
	}
	if v := q.Get("since"); v != "" {
		t, err := core.ParseSince(v, time.Now())
		if err != nil {
			a.fail(w, r, err)
			return
		}
		f.Since = t
	}
	entries, err := a.cfg.Service.QueryLog(r.Context(), workspace(r), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]queryLogJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, queryLogJSON{ID: e.ID, Connection: e.Connection, KeyID: e.KeyID, Tool: e.Tool, Query: e.Query,
			Rows: e.Rows, DurationMS: e.Duration.Milliseconds(), ErrorCategory: e.ErrorCategory, At: e.At})
	}
	writeJSON(w, http.StatusOK, out)
}
