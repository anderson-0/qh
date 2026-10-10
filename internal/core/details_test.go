package core

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestFieldsBuildTheConnectionString(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	db := testutil.SQLiteFixture(t)
	info, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", Database: db}, true)
	if err != nil || info.Database != db {
		t.Fatalf("sqlite fields: %+v %v", info, err)
	}
	pg, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", Host: "db.example.com", Port: 5433,
		Database: "app", Username: "reader", Password: "p@ss/w:rd", Params: map[string]string{"sslmode": "disable"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if pg.Host != "db.example.com" || pg.Port != 5433 || pg.Database != "app" || pg.User != "reader" ||
		strings.Contains(pg.DSN, "p@ss") || !strings.Contains(pg.DSN, "***") {
		t.Fatalf("postgres fields: %+v", pg)
	}
	for _, c := range []struct {
		in   ConnInput
		want string
	}{
		{ConnInput{Name: "x", Engine: "postgres", DSN: "postgres://h/db", Host: "h"}, "error[invalid]: give either dsn or host, port, database, username, password and params, not both"},
		{ConnInput{Name: "x", Engine: "leaky", Host: "h"}, `error[invalid]: engine "leaky" needs a connection string (dsn)`},
		{ConnInput{Name: "x", Engine: "postgres", Database: "app"}, "error[invalid]: host is required"},
		{ConnInput{Name: "x", Engine: "postgres"}, "error[invalid]: connection string is required"},
	} {
		if _, _, err := h.svc.CreateConnection(ctx, ws, c.in, false); !strings.HasPrefix(errText(err), c.want) {
			t.Errorf("%+v: %q", c.in, errText(err))
		}
	}
}

func TestTLSSettingsAreSealedAndSummarized(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	f := testutil.NewTLSFixture(t)
	tl := &engine.TLS{Mode: engine.TLSVerifyFull, CA: f.CA, Cert: f.ClientCert, Key: f.ClientKey, ServerName: "db.internal"}
	info, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u:pw@db.example.com/app", TLS: tl}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := engine.TLSInfo{Mode: engine.TLSVerifyFull, CA: true, ClientCert: true, ServerName: "db.internal"}
	if info.TLS == nil || *info.TLS != want {
		t.Fatalf("summary: %+v", info.TLS)
	}
	rec, _ := h.st.GetConnection(ctx, ws, "pg")
	if bytes.Contains(rec.Secret, []byte("PRIVATE KEY")) || strings.Contains(rec.TLS, "PRIVATE") || strings.Contains(rec.TLS, "BEGIN") {
		t.Fatalf("TLS material stored in clear: %q", rec.TLS)
	}
	if got, _ := h.svc.GetConnection(ctx, ws, "pg"); got.TLS == nil || *got.TLS != want {
		t.Fatalf("get: %+v", got.TLS)
	}
	tgt, err := h.svc.resolve(ctx, ws, "pg")
	if err != nil || tgt.spec.TLS == nil || tgt.spec.TLS.Key != f.ClientKey || tgt.spec.TLS.ServerName != "db.internal" {
		t.Fatalf("spec TLS: %+v %v", tgt, err)
	}

	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "s", Engine: "sqlite", DSN: testutil.SQLiteFixture(t), TLS: &engine.TLS{Mode: engine.TLSRequire}}, false); !strings.HasPrefix(errText(err), "error[invalid]: tls applies only to network databases") {
		t.Fatalf("sqlite tls: %q", errText(err))
	}
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "b", Engine: "postgres", DSN: "postgres://h/db", TLS: &engine.TLS{Mode: "prefer"}}, false); !strings.HasPrefix(errText(err), `error[invalid]: unknown tls.mode "prefer"`) {
		t.Fatalf("bad mode: %q", errText(err))
	}
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{TLS: tl, ClearTLS: true}, false); !strings.HasPrefix(errText(err), "error[invalid]: give tls or clear it, not both") {
		t.Fatalf("both: %q", errText(err))
	}

	info, _, err = h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{ClearTLS: true}, false)
	if err != nil || info.TLS != nil || info.Version != 2 {
		t.Fatalf("clear: %+v %v", info, err)
	}
	if tgt, _ := h.svc.resolve(ctx, ws, "pg"); tgt.spec.TLS != nil {
		t.Fatal("cleared TLS still applied")
	}
	info, _, err = h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{TLS: &engine.TLS{Mode: engine.TLSRequire}}, false)
	if err != nil || info.TLS == nil || info.TLS.Mode != engine.TLSRequire {
		t.Fatalf("set: %+v %v", info, err)
	}
}

func TestLegacySecretStillOpens(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	db := testutil.SQLiteFixture(t)
	rec := &store.Connection{ID: store.NewID(), WorkspaceID: ws, Name: "old", Engine: "sqlite", Database: db,
		Mode: "ro", MaxRows: 100, TimeoutMS: 30000, PoolSize: 5}
	sealed, err := h.svc.keys.Seal([]byte(db), []byte(rec.ID)) // the pre-Plan-4 format: the raw connection string
	if err != nil {
		t.Fatal(err)
	}
	rec.Secret, rec.SecretKeyID = sealed, h.svc.keys.PrimaryID()
	if err := h.st.CreateConnection(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if out, err := h.svc.Query(ctx, ws, QueryInput{Source: "old", Query: "SELECT count(*) AS n FROM orders"}); err != nil || !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query: %q %v", out, err)
	}
	if info, _ := h.svc.GetConnection(ctx, ws, "old"); info.DSN != db || info.TLS != nil {
		t.Fatalf("info: %+v", info)
	}
	d := "edited"
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "old", ConnPatch{Description: &d}, false); err != nil {
		t.Fatal(err)
	}
	again, _ := h.st.GetConnection(ctx, ws, "old")
	pt, err := h.svc.keys.Open(again.Secret, []byte(again.ID))
	if err != nil || !bytes.HasPrefix(pt, []byte(`{"v":2,`)) {
		t.Fatalf("edit did not re-seal in the new format: %v", err)
	}
}
