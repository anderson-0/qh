package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/anderson-0/qh/internal/core"
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
	t.Setenv("QH_MASTER_KEY", testMasterKey(t))
	_, errOut, code := run(t, t.TempDir(), "", "--store", "mysql://x", "conn", "ls")
	if code == 0 || !strings.Contains(errOut, "postgres://") {
		t.Fatalf("%d %q", code, errOut)
	}
	_, errOut, code = run(t, t.TempDir(), "", "--store", "postgres://x@127.0.0.1:1/qh", "--server", "http://127.0.0.1:1", "conn", "ls")
	if code == 0 || !strings.Contains(errOut, "--store and --server cannot be used together") ||
		!strings.Contains(errOut, "QH_SERVER") || !strings.Contains(errOut, "QH_STORE_DSN") {
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

// A shared store never takes its master key from (or creates) master.key: a
// per-host key file would seal fleet data under a key no other instance has.
func TestSharedStoreNeedsMasterKey(t *testing.T) {
	t.Setenv("QH_MASTER_KEY", "")
	t.Setenv("QH_STORE_DSN", "")
	unreachable := "postgres://qh@127.0.0.1:1/qh" // the key check must come first
	old := filepath.Join(t.TempDir(), "old.key")
	if err := os.WriteFile(old, []byte(testMasterKey(t)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--store", unreachable, "conn", "ls"},
		{"--store", unreachable, "mcp"},
		{"--store", unreachable, "serve"},
		{"--store", unreachable, "masterkey", "rotate", "--old-key-file", old},
		{"--store", unreachable, "masterkey", "rotate", "--generate", "--old-key-file", old},
	} {
		home := t.TempDir()
		_, errOut, code := run(t, home, "", args...)
		if code != 1 || !strings.HasPrefix(errOut, "error[invalid]: a shared store needs QH_MASTER_KEY") ||
			!strings.Contains(errOut, "master.key") {
			t.Fatalf("%v: %d %q", args, code, errOut)
		}
		if _, err := os.Stat(filepath.Join(home, "master.key")); err == nil {
			t.Fatalf("%v created master.key", args)
		}
	}
	// An existing master.key is not used either.
	home := t.TempDir()
	if _, errOut, code := run(t, home, "", "conn", "ls"); code != 0 { // creates master.key locally
		t.Fatal(errOut)
	}
	t.Setenv("QH_STORE_DSN", unreachable)
	if _, errOut, code := run(t, home, "", "conn", "ls"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: a shared store needs QH_MASTER_KEY") {
		t.Fatalf("existing master.key: %d %q", code, errOut)
	}
}

// Local commands read QH_MASTER_KEY_OLD like serve and mcp, so an admin host
// works between the steps of a master-key rotation.
func TestLocalCommandsReadOldMasterKey(t *testing.T) {
	t.Setenv("QH_STORE_DSN", "")
	oldKey, newKey := testMasterKey(t), testMasterKey(t)
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	t.Setenv("QH_MASTER_KEY", oldKey)
	t.Setenv("QH_MASTER_KEY_OLD", "")
	if _, errOut, code := run(t, home, "", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db); code != 0 {
		t.Fatal(errOut)
	}
	t.Setenv("QH_MASTER_KEY", newKey)
	if _, errOut, code := run(t, home, "", "query", "shop", "SELECT 1"); code == 0 {
		t.Fatalf("opened a connection sealed with an unknown key: %q", errOut)
	}
	t.Setenv("QH_MASTER_KEY_OLD", oldKey)
	if out, errOut, code := run(t, home, "", "query", "shop", "SELECT 1"); code != 0 {
		t.Fatalf("old key ignored: %q %q", out, errOut)
	}
	t.Setenv("QH_MASTER_KEY_OLD", "not-base64")
	if _, errOut, code := run(t, home, "", "conn", "ls"); code != 1 || !strings.Contains(errOut, "QH_MASTER_KEY_OLD must hold") {
		t.Fatalf("bad old key: %d %q", code, errOut)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The service's logger reaches a shared store, so a lost invalidation
// listener is reported where serve and mcp log.
func TestSharedStoreLogsToTheServiceLogger(t *testing.T) {
	dsn := pgStoreDSN(t)
	t.Setenv("QH_MASTER_KEY", testMasterKey(t))
	t.Setenv("QH_STORE_DSN", dsn)
	var buf lockedBuffer
	a, err := openAppWith(context.Background(), t.TempDir(), core.Options{Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.svc.Start(ctx)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(buf.String(), "reconnected"); {
		if time.Now().After(deadline) {
			t.Fatalf("no reconnect was logged:\n%s", buf.String())
		}
		if _, err := db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid <> pg_backend_pid()
			AND datname = current_database() AND query LIKE 'LISTEN%'`); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), "lost the connection") {
		t.Fatalf("the lost connection was not logged:\n%s", buf.String())
	}
}
