package cli

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

var cliDBSeq atomic.Int64

func pgStoreDSN(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("qh"), tcpostgres.WithPassword("store-pw-123"), tcpostgres.BasicWaitStrategies())
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := ctr.ConnectionString(ctx, "sslmode=disable")
	db, _ := sql.Open("pgx", admin)
	defer db.Close()
	name := fmt.Sprintf("qh_cli_%d", cliDBSeq.Add(1))
	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	return u.String()
}

// testMasterKey returns a fresh base64 32-byte master key.
func testMasterKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestStoreFlagSelectsPostgres(t *testing.T) {
	dsn := pgStoreDSN(t)
	homeA, homeB := t.TempDir(), t.TempDir() // two hosts sharing one store and one master key
	t.Setenv("QH_MASTER_KEY", testMasterKey(t))
	db := testutil.SQLiteFixture(t)
	if _, errOut, code := run(t, homeA, "", "--store", dsn, "conn", "add", "shop", "--engine", "sqlite", "--dsn", db); code != 0 {
		t.Fatal(errOut)
	}
	t.Setenv("QH_STORE_DSN", dsn)
	out, errOut, code := run(t, homeB, "", "conn", "ls")
	if code != 0 || !strings.Contains(out, "shop") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if _, err := os.Stat(homeB + "/qh.db"); err == nil {
		t.Fatal("a SQLite store was created although --store was set")
	}
}

func TestStoreFlagErrors(t *testing.T) {
	_, errOut, code := run(t, t.TempDir(), "", "--store", "mysql://x", "conn", "ls")
	if code == 0 || !strings.Contains(errOut, "postgres://") {
		t.Fatalf("%d %q", code, errOut)
	}
	_, errOut, code = run(t, t.TempDir(), "", "--store", "postgres://x@127.0.0.1:1/qh", "--server", "http://127.0.0.1:1", "conn", "ls")
	if code == 0 || !strings.Contains(errOut, "--store and --server cannot be used together") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestStoreOpenError(t *testing.T) {
	err := storeOpenError(fmt.Errorf("open: %w", store.ErrInvalidDSN), true)
	var qe *qerr.Error
	if !errors.As(err, &qe) || qe.Category != qerr.Invalid || !strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("invalid dsn: %v", err)
	}
	err = storeOpenError(errors.New("dial tcp 127.0.0.1:1: connection refused"), true)
	if !errors.As(err, &qe) || qe.Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "cannot open the store") ||
		!strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("other error: %v", err)
	}
	if storeOpenError(nil, true) != nil {
		t.Fatal("nil error changed")
	}
	local := errors.New("unable to open database file")
	if got := storeOpenError(local, false); got != local {
		t.Fatalf("local SQLite error relabelled: %v", got)
	}
}

func TestOldMasterKeys(t *testing.T) {
	if _, err := oldMasterKeys("not-base64"); err == nil {
		t.Fatal("bad QH_MASTER_KEY_OLD accepted")
	}
	keys, err := oldMasterKeys(testMasterKey(t) + ", " + testMasterKey(t))
	if err != nil || len(keys) != 2 {
		t.Fatalf("%d %v", len(keys), err)
	}
	if keys, err := oldMasterKeys(""); err != nil || len(keys) != 0 {
		t.Fatalf("empty: %v %v", keys, err)
	}
}
