package cli

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/httpapi"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func remoteServer(t *testing.T) (string, string) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, secret.KeySize)
	rand.Read(key)
	keys, _ := secret.NewKeyring(key)
	pools := pool.New(core.OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	admin, _, err := auth.Bootstrap(context.Background(), st, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Config{Service: core.New(st, keys, pools, catalog.New(time.Minute)),
		Store: st, Auth: auth.NewAuthenticator(st), Version: "test"}))
	t.Cleanup(srv.Close)
	return srv.URL, admin
}

func TestRemoteMode(t *testing.T) {
	url, admin := remoteServer(t)
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	as := func(key string, args ...string) (string, string, int) {
		return run(t, home, "", append([]string{"--server", url, "--key", key}, args...)...)
	}
	if out, errOut, code := as(admin, "workspace", "create", "team"); code != 0 || out != "created workspace team\n" {
		t.Fatalf("workspace: %d %q %q", code, out, errOut)
	}
	out, errOut, code := as(admin, "--workspace", "team", "key", "create", "--scopes", "query,manage")
	tok := regexp.MustCompile(`(?m)^qh_\S+$`).FindString(out)
	if code != 0 || tok == "" || !strings.Contains(out, "workspace team") {
		t.Fatalf("key: %d %q %q", code, out, errOut)
	}
	if out, errOut, code := as(tok, "conn", "add", "shop", "--engine", "sqlite", "--database", db); code != 0 || !strings.HasPrefix(out, "added shop (sqlite, ro) · SQLite ") {
		t.Fatalf("add: %d %q %q", code, out, errOut)
	}
	if out, _, _ := as(tok, "conn", "ls"); !strings.Contains(out, "shop") {
		t.Fatalf("ls: %q", out)
	}
	if out, _, _ := as(tok, "query", "shop", "SELECT count(*) AS n FROM orders"); !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query: %q", out)
	}
	if out, _, _ := as(tok, "schema", "shop", "users"); !strings.Contains(out, "main.users") {
		t.Fatalf("schema: %q", out)
	}
	if out, _, _ := as(tok, "log"); !strings.Contains(out, "shop") || !strings.Contains(out, "SELECT count(*) AS n FROM orders") {
		t.Fatalf("log: %q", out)
	}
	if out, errOut, code := as(tok, "key", "create", "--scopes", "query"); code != 0 || !strings.Contains(out, "workspace team") {
		t.Fatalf("manage key without --workspace: %d %q %q", code, out, errOut)
	}
	if _, errOut, code := as(admin, "key", "create", "--scopes", "query"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]") || strings.Contains(errOut, "default") {
		t.Fatalf("admin key without --workspace: %d %q", code, errOut)
	}
	if _, errOut, code := as(tok, "conn", "show", "nope"); code != 1 || !strings.HasPrefix(errOut, `error[unknown_source]: no connection named "nope"`) {
		t.Fatalf("unknown: %d %q", code, errOut)
	}
	if _, errOut, code := as(admin, "conn", "ls"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: admin keys must name a workspace") {
		t.Fatalf("admin without --workspace: %d %q", code, errOut)
	}
	if _, errOut, code := as(admin, "mcp"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: qh mcp runs locally only") {
		t.Fatalf("mcp: %d %q", code, errOut)
	}
	if _, errOut, code := as(admin, "masterkey", "rotate", "--old-key-file", "x"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: qh masterkey rotate runs locally only") {
		t.Fatalf("masterkey: %d %q", code, errOut)
	}
	t.Setenv("QH_SERVER", url)
	t.Setenv("QH_KEY", tok)
	if out, _, _ := run(t, home, "", "conn", "ls"); !strings.Contains(out, "shop") {
		t.Fatalf("QH_SERVER and QH_KEY: %q", out)
	}
}

func TestRemoteRefusesPlainHTTP(t *testing.T) {
	_, errOut, code := run(t, t.TempDir(), "", "--server", "http://db.example.com:8765", "--key", "qh_abcdefgh_secret", "conn", "ls")
	if code != 1 || !strings.HasPrefix(errOut, "error[invalid]: refusing to send an API key over plain http to db.example.com:8765") ||
		strings.Contains(errOut, "qh_abcdefgh_secret") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestRemotePasswordEdit(t *testing.T) {
	url, admin := remoteServer(t)
	home := t.TempDir()
	if _, _, code := run(t, home, "", "--server", url, "--key", admin, "workspace", "create", "passwords"); code != 0 {
		t.Fatal("create test workspace failed")
	}
	testPasswordEditCommands(t, func(stdin string, args ...string) (string, string, int) {
		return run(t, home, stdin, append([]string{"--server", url, "--key", admin, "--workspace", "passwords"}, args...)...)
	})
}

// With --server, key rm prints the same success line and stderr notice as
// locally; failures print neither, and a foreign key looks like a missing one.
func TestRemoteKeyRevokeWarning(t *testing.T) {
	url, admin := remoteServer(t)
	home := t.TempDir()
	as := func(key string, args ...string) (string, string, int) {
		return run(t, home, "", append([]string{"--server", url, "--key", key}, args...)...)
	}
	for _, ws := range []string{"team", "other"} {
		if out, errOut, code := as(admin, "workspace", "create", ws); code != 0 {
			t.Fatalf("workspace %s: %d %q %q", ws, code, out, errOut)
		}
	}
	tokenRE := regexp.MustCompile(`(?m)^qh_\S+$`)
	create := func(name, key string, args ...string) (tok, id string) {
		t.Helper()
		out, errOut, code := as(key, append([]string{"key", "create", "--name", name}, args...)...)
		m := createdKeyID.FindStringSubmatch(out)
		tok = tokenRE.FindString(out)
		if code != 0 || m == nil || tok == "" {
			t.Fatalf("create %s: %d %q", name, code, errOut) // out holds the key; do not print it
		}
		return tok, m[1]
	}
	team, teamID := create("team", admin, "--workspace", "team", "--scopes", "query,manage")
	child, childID := create("child", team, "--scopes", "query")
	stranger, _ := create("stranger", admin, "--workspace", "other", "--scopes", "query,manage")
	childWorks := func(when string) {
		t.Helper()
		if _, errOut, code := as(child, "conn", "ls"); code != 0 {
			t.Fatalf("child %s: %d %q", when, code, errOut)
		}
	}

	if out, errOut, code := as(admin, "key", "rm", teamID); code != 0 || out != "revoked key "+teamID+"\n" || errOut != auth.RevocationWarning+"\n" {
		t.Fatalf("revoke team: %d %q %q", code, out, errOut)
	}
	childWorks("after its creator was revoked")

	fails := func(name, wantPrefix, key string, args ...string) string {
		t.Helper()
		out, errOut, code := as(key, args...)
		if code != 1 || out != "" || !strings.HasPrefix(errOut, wantPrefix) || strings.Contains(errOut, auth.RevocationWarning) {
			t.Fatalf("%s: %d %q %q", name, code, out, errOut)
		}
		return errOut
	}
	fails("revoke team twice", `error[unknown_source]: no API key with id "`+teamID+`"`, admin, "key", "rm", teamID)
	fails("query key revokes", "error[forbidden]: this API key lacks the manage scope", child, "key", "rm", childID)
	fails("revoked key revokes", "error[auth]: invalid API key", team, "key", "rm", childID)
	missing := store.NewID()
	foreign := fails("stranger revokes child", `error[unknown_source]: no API key with id "`+childID+`"`, stranger, "key", "revoke", childID)
	absent := fails("stranger revokes missing id", `error[unknown_source]: no API key with id "`+missing+`"`, stranger, "key", "revoke", missing)
	if strings.ReplaceAll(foreign, childID, "ID") != strings.ReplaceAll(absent, missing, "ID") {
		t.Fatalf("foreign and missing keys differ: %q %q", foreign, absent)
	}
	childWorks("after a foreign revocation attempt")
}
