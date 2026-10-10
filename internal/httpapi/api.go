// Package httpapi serves qh over HTTP: MCP at /mcp and the REST API under
// /api/v1, with API-key authentication, workspace scoping and per-key
// concurrency limits.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

const maxBody = 1 << 20

// Config configures the API.
type Config struct {
	Service     *core.Service
	Store       store.Store
	Auth        *auth.Authenticator // nil: local mode, every request acts as auth.Local()
	Version     string
	Logger      *slog.Logger
	PerKeyLimit int           // limited requests per key at once; default 4
	SlotWait    time.Duration // wait for a slot before busy; default 10s
}

type route struct {
	method    string // "" matches every method
	pattern   string
	scope     string // "" for public routes
	workspace bool   // resolve the caller's workspace
	limited   bool   // counts against the per-key concurrency limit
	handle    http.HandlerFunc
}

type api struct {
	cfg     Config
	limiter *limiter
	mcpMu   sync.Mutex
	servers map[string]*mcp.Server
	mcp     http.Handler
}

// New returns the HTTP handler.
func New(cfg Config) http.Handler {
	if cfg.PerKeyLimit <= 0 {
		cfg.PerKeyLimit = 4
	}
	if cfg.SlotWait <= 0 {
		cfg.SlotWait = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	a := &api{cfg: cfg, limiter: newLimiter(cfg.PerKeyLimit, cfg.SlotWait), servers: map[string]*mcp.Server{}}
	a.mcp = mcp.NewStreamableHTTPHandler(a.mcpServer, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	for _, rt := range a.routes() {
		pattern := rt.pattern
		if rt.method != "" {
			pattern = rt.method + " " + rt.pattern
		}
		mux.Handle(pattern, a.wrap(rt))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "not_found", "no such endpoint", "See /api/v1/openapi.json")
	})
	return a.recoverPanics(a.logRequests(mux))
}

func (a *api) routes() []route {
	const q, m, ad = auth.ScopeQuery, auth.ScopeManage, auth.ScopeAdmin
	return []route{
		{"GET", "/healthz", "", false, false, a.healthz},
		{"GET", "/readyz", "", false, false, a.readyz},
		{"GET", "/api/v1/openapi.json", "", false, false, serveOpenAPI},
		{"", "/mcp", q, true, true, a.mcp.ServeHTTP},
		{"GET", "/api/v1/connections", q, true, false, a.listConnections},
		{"POST", "/api/v1/connections", m, true, false, a.createConnection},
		{"POST", "/api/v1/connections/test", m, true, false, a.testInput},
		{"GET", "/api/v1/connections/{name}", q, true, false, a.getConnection},
		{"PATCH", "/api/v1/connections/{name}", m, true, false, a.updateConnection},
		{"DELETE", "/api/v1/connections/{name}", m, true, false, a.deleteConnection},
		{"POST", "/api/v1/connections/{name}/test", m, true, false, a.testConnection},
		{"GET", "/api/v1/connections/{name}/schema", q, true, true, a.getSchema},
		{"POST", "/api/v1/connections/{name}/schema/refresh", m, true, true, a.refreshSchema},
		{"POST", "/api/v1/connections/{name}/query", q, true, true, a.query},
		{"GET", "/api/v1/workspaces", ad, false, false, a.listWorkspaces},
		{"POST", "/api/v1/workspaces", ad, false, false, a.createWorkspace},
		{"DELETE", "/api/v1/workspaces/{name}", ad, false, false, a.deleteWorkspace},
		{"GET", "/api/v1/keys", m, false, false, a.listKeys},
		{"POST", "/api/v1/keys", m, false, false, a.createKey},
		{"DELETE", "/api/v1/keys/{id}", m, false, false, a.deleteKey},
	}
}

type (
	principalKey struct{}
	workspaceKey struct{}
	infoKey      struct{}
)

type requestInfo struct{ key string }

func principal(r *http.Request) *auth.Principal {
	p, _ := r.Context().Value(principalKey{}).(*auth.Principal)
	return p
}

func workspace(r *http.Request) string {
	ws, _ := r.Context().Value(workspaceKey{}).(string)
	return ws
}

func (a *api) authenticate(r *http.Request) (*auth.Principal, error) {
	if a.cfg.Auth == nil {
		return auth.Local(), nil
	}
	return a.cfg.Auth.Authenticate(r.Context(), r.Header.Get("Authorization"))
}

func (a *api) resolveWorkspace(ctx context.Context, p *auth.Principal, header string) (string, error) {
	ws, err := auth.ResolveWorkspace(p, header)
	if err != nil {
		return "", err
	}
	ok, err := a.cfg.Service.WorkspaceExists(ctx, ws)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", qerr.New(qerr.UnknownSource, fmt.Sprintf("no workspace named %q", ws))
	}
	return ws, nil
}

// wrap applies the body limit, authentication, scope check, workspace
// resolution and the per-key limit that rt asks for.
func (a *api) wrap(rt route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if rt.scope == "" {
			rt.handle(w, r)
			return
		}
		ctx := r.Context()
		p, err := a.authenticate(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if info, ok := ctx.Value(infoKey{}).(*requestInfo); ok {
			info.key = p.Prefix
		}
		if !p.Has(rt.scope) {
			a.fail(w, r, qerr.New(qerr.Forbidden, "this API key lacks the "+rt.scope+" scope"))
			return
		}
		ctx = context.WithValue(ctx, principalKey{}, p)
		if rt.workspace {
			ws, err := a.resolveWorkspace(ctx, p, r.Header.Get("X-QH-Workspace"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			ctx = context.WithValue(ctx, workspaceKey{}, ws)
		}
		if rt.limited {
			release, err := a.limiter.acquire(ctx, p.KeyID)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			defer release()
		}
		rt.handle(w, r.WithContext(ctx))
	})
}

type limiter struct {
	per   int
	wait  time.Duration
	mu    sync.Mutex
	slots map[string]chan struct{}
}

func newLimiter(per int, wait time.Duration) *limiter {
	return &limiter{per: per, wait: wait, slots: map[string]chan struct{}{}}
}

func (l *limiter) acquire(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	ch := l.slots[key]
	if ch == nil {
		ch = make(chan struct{}, l.per)
		l.slots[key] = ch
	}
	l.mu.Unlock()
	release := func() { <-ch }
	select {
	case ch <- struct{}{}:
		return release, nil
	default:
	}
	t := time.NewTimer(l.wait)
	defer t.Stop()
	select {
	case ch <- struct{}{}:
		return release, nil
	case <-t.C:
		return nil, qerr.New(qerr.Busy, fmt.Sprintf("this API key already has %d requests running", l.per)).WithHint("Retry shortly")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var statusOf = map[qerr.Category]int{
	qerr.Auth: http.StatusUnauthorized, qerr.Forbidden: http.StatusForbidden, qerr.UnknownSource: http.StatusNotFound,
	qerr.Invalid: http.StatusBadRequest, qerr.ReadOnly: http.StatusForbidden, qerr.Timeout: http.StatusGatewayTimeout,
	qerr.ConnectionFailed: http.StatusBadGateway, qerr.QueryError: http.StatusUnprocessableEntity, qerr.Busy: http.StatusTooManyRequests,
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Status int    `json:"status"`
}

func oneLine(s string) string { return strings.TrimRight(strings.Join(strings.Fields(s), " "), ".") }

func writeProblem(w http.ResponseWriter, status int, typ, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: typ, Title: oneLine(title), Detail: oneLine(detail), Status: status})
}

// fail writes err as a problem. Errors qh did not classify are logged and
// reported as a generic 500 so internal details never reach the client.
func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *qerr.Error
	if !errors.As(err, &e) {
		a.cfg.Logger.Error("internal error", "method", r.Method, "path", r.URL.Path, "error", err.Error())
		writeProblem(w, http.StatusInternalServerError, "internal", "internal server error", "")
		return
	}
	status := statusOf[e.Category]
	if status == 0 {
		status = http.StatusInternalServerError
	}
	switch status {
	case http.StatusUnauthorized:
		w.Header().Set("WWW-Authenticate", "Bearer")
	case http.StatusTooManyRequests:
		w.Header().Set("Retry-After", "1")
	}
	writeProblem(w, status, string(e.Category), e.Message, e.Hint)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeText(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, s)
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// decode reads a JSON body, rejecting unknown fields and oversized bodies.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return qerr.New(qerr.Invalid, "request body is larger than 1 MiB")
		}
		return qerr.New(qerr.Invalid, "invalid JSON body: "+err.Error())
	}
	return nil
}

func (a *api) healthz(w http.ResponseWriter, _ *http.Request) { writeText(w, "ok\n") }

func (a *api) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.cfg.Store.Ping(ctx); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "unavailable", "the store is not reachable", "")
		return
	}
	writeText(w, "ok\n")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// logRequests logs one line per request. It never logs bodies or headers.
func (a *api) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &requestInfo{}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), infoKey{}, info)))
		a.cfg.Logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(), "key", info.key)
	})
}

func (a *api) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				a.cfg.Logger.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(v))
				writeProblem(w, http.StatusInternalServerError, "internal", "internal server error", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
