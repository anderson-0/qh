package core

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

// docEngine is a non-SQL engine: it classifies "write …" as a write and
// everything else as a read, and records what reaches its connection.
type docEngine struct{}

var (
	docQueries atomic.Int64
	docSpec    atomic.Pointer[engine.ConnSpec]
)

func (docEngine) Name() string { return "qhtest-doc" }
func (docEngine) Inspect(string) (engine.DSNInfo, error) {
	return engine.DSNInfo{Redacted: "doc://x"}, nil
}
func (docEngine) Classify(q string) (engine.Access, error) {
	if strings.HasPrefix(q, "write") {
		return engine.Write, nil
	}
	return engine.Read, nil
}
func (docEngine) ReferencedObjects(string) []string { return nil }
func (docEngine) Open(_ context.Context, s engine.ConnSpec) (engine.Conn, error) {
	docSpec.Store(&s)
	return docConn{}, nil
}

type docConn struct{}

func (docConn) Query(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	docQueries.Add(1)
	return &engine.Result{Columns: []engine.Column{{Name: "x"}}, Rows: [][]engine.Value{{{Text: "1"}}}, RowsAffected: -1}, nil
}
func (docConn) Explain(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	return nil, nil
}
func (docConn) Introspect(context.Context) (*engine.Catalog, error) { return &engine.Catalog{}, nil }
func (docConn) Ping(context.Context) (string, error)                { return "doc 1", nil }
func (docConn) Close() error                                        { return nil }

func init() { engine.Register(docEngine{}) }

func TestEngineClassifiesItsQueries(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "doc", Engine: "qhtest-doc", DSN: "doc://x",
		Mode: engine.ModeRO, AllowedNamespaces: []string{"shop"}}, false); err != nil {
		t.Fatal(err)
	}
	before := docQueries.Load()
	_, err := h.svc.Query(ctx, ws, QueryInput{Source: "doc", Query: "write something"})
	if e := qerr.From(err); err == nil || e.Category != qerr.ReadOnly {
		t.Fatalf("write on a read-only doc connection: %v", err)
	}
	if docQueries.Load() != before {
		t.Fatal("the refused write reached the connection")
	}
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "doc", Query: "db.x.find()"}); err != nil {
		t.Fatal(err)
	}
	if s := docSpec.Load(); s == nil || len(s.Namespaces) != 1 || s.Namespaces[0] != "shop" {
		t.Fatalf("ConnSpec.Namespaces = %+v", s)
	}
}
