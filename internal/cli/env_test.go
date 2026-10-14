package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/testutil"
)

// clearQHEnv unsets the variables that change what qh mcp serves.
func clearQHEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"QH_DSN", "QH_ENGINE", "QH_NAME", "QH_DESCRIPTION", "QH_MODE", "QH_STORE_DSN", "QH_SERVER", "QH_WORKSPACE", "QH_MASTER_KEY"} {
		t.Setenv(k, "")
	}
}

// envMCP runs `qh mcp` in-process over pipes and returns a connected client
// and a function that closes the session and returns the exit code and stderr.
func envMCP(t *testing.T, home string, args ...string) (*mcp.ClientSession, func() (int, string)) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var errOut strings.Builder
	done := make(chan int, 1)
	go func() {
		code := Execute("test", append([]string{"--home", home, "mcp"}, args...), inR, outW, &errOut)
		outW.Close()
		done <- code
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "env-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		inW.Close()
		t.Fatalf("connect: %v; stderr: %s", err, errOut.String())
	}
	return session, func() (int, string) {
		session.Close()
		inW.Close()
		select {
		case code := <-done:
			return code, errOut.String()
		case <-time.After(15 * time.Second):
			t.Fatal("qh mcp did not exit")
			return 0, ""
		}
	}
}

// ephemeralTmp points TMPDIR at a fresh directory, where the ephemeral store
// goes, and returns it.
func ephemeralTmp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("%s is not empty after exit: %v", dir, entries)
	}
}

func callText(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestEnvMCPServesOneConnection(t *testing.T) {
	clearQHEnv(t)
	db := testutil.SQLiteFixture(t)
	t.Setenv("QH_DSN", db)
	t.Setenv("QH_NAME", "shop")
	t.Setenv("QH_DESCRIPTION", "Orders and customers")
	home := filepath.Join(t.TempDir(), "home")
	tmp := ephemeralTmp(t)

	s, finish := envMCP(t, home)
	if entries, _ := os.ReadDir(tmp); len(entries) != 1 {
		t.Errorf("expected one ephemeral store directory while serving: %v", entries)
	}
	in := s.InitializeResult().Instructions
	if !strings.Contains(in, "shop (sqlite, ro): Orders and customers") {
		t.Errorf("instructions: %q", in)
	}
	if out := callText(t, s, "query", map[string]any{"query": "SELECT count(*) AS n FROM orders"}); !strings.HasPrefix(out, "n\n250\n-- 1 row · ") {
		t.Errorf("query: %q", out)
	}
	code, errOut := finish()
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "serving one connection from QH_DSN; stored connections are not used") || strings.Contains(errOut, db) {
		t.Errorf("stderr: %q", errOut)
	}
	assertEmptyDir(t, tmp)
	// Nothing is kept on disk: not even the data directory exists.
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(home)
		t.Errorf("home was created (%v): %v", err, entries)
	}
}

func TestEnvMCPDefaultName(t *testing.T) {
	clearQHEnv(t)
	t.Setenv("QH_DSN", "sqlite://"+testutil.SQLiteFixture(t))
	s, finish := envMCP(t, t.TempDir())
	if in := s.InitializeResult().Instructions; !strings.Contains(in, "db (sqlite, ro)") {
		t.Errorf("instructions: %q", in)
	}
	if code, errOut := finish(); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestEnvMCPModeRW(t *testing.T) {
	clearQHEnv(t)
	t.Setenv("QH_DSN", testutil.SQLiteFixture(t))
	t.Setenv("QH_MODE", "rw")
	tmp := ephemeralTmp(t)
	s, finish := envMCP(t, t.TempDir())
	if in := s.InitializeResult().Instructions; !strings.Contains(in, "db (sqlite, rw)") {
		t.Errorf("instructions: %q", in)
	}
	if out := callText(t, s, "query", map[string]any{"query": "DELETE FROM order_items WHERE order_id = 1"}); !strings.HasPrefix(out, "-- 2 rows affected") {
		t.Errorf("write: %q", out)
	}
	if code, errOut := finish(); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	assertEmptyDir(t, tmp)
}

func TestEnvMCPReadOnlyByDefault(t *testing.T) {
	clearQHEnv(t)
	t.Setenv("QH_DSN", testutil.SQLiteFixture(t))
	s, finish := envMCP(t, t.TempDir())
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"query": "DELETE FROM users"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "read_only") {
		t.Errorf("write on ro: %+v", res)
	}
	if code, errOut := finish(); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestEnvRefusals(t *testing.T) {
	db := testutil.SQLiteFixture(t)
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"store dsn", map[string]string{"QH_DSN": db, "QH_STORE_DSN": "postgres://qh@127.0.0.1:1/qh"}, nil, "error[invalid]: QH_DSN cannot be used with --store or QH_STORE_DSN"},
		{"store flag", map[string]string{"QH_DSN": db}, []string{"--store", "postgres://qh@127.0.0.1:1/qh"}, "error[invalid]: QH_DSN cannot be used with --store or QH_STORE_DSN"},
		{"server flag", map[string]string{"QH_DSN": db}, []string{"--server", "http://127.0.0.1:1"}, "error[invalid]: QH_DSN cannot be used with --server"},
		{"bad mode", map[string]string{"QH_DSN": db, "QH_MODE": "xx"}, nil, "error[invalid]: QH_MODE must be ro or rw"},
		{"bad name", map[string]string{"QH_DSN": db, "QH_NAME": "Not Valid"}, nil, `error[invalid]: invalid connection name "Not Valid"`},
		{"no engine", map[string]string{"QH_DSN": "user:pw@tcp(host)/db"}, nil, "error[invalid]: cannot tell the engine from QH_DSN; set QH_ENGINE"},
		{"unknown engine", map[string]string{"QH_DSN": db, "QH_ENGINE": "oracle"}, nil, `error[invalid]: unknown engine "oracle"`},
		{"server env", map[string]string{"QH_DSN": db, "QH_SERVER": "http://127.0.0.1:1"}, nil, "error[invalid]: QH_DSN cannot be used with --server"},
		{"workspace env", map[string]string{"QH_DSN": db, "QH_WORKSPACE": "other"}, nil, "error[invalid]: QH_DSN serves the default workspace only"},
		{"workspace", map[string]string{"QH_DSN": db}, []string{"--workspace", "other"}, "error[invalid]: QH_DSN serves the default workspace only"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearQHEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			home := filepath.Join(t.TempDir(), "home")
			tmp := ephemeralTmp(t)
			defer assertEmptyDir(t, tmp)
			var args []string
			args = append(args, c.args...)
			args = append(args, "mcp")
			_, errOut, code := run(t, home, "", args...)
			if code != 1 || !strings.HasPrefix(errOut, c.want) {
				t.Fatalf("%d %q, want prefix %q", code, errOut, c.want)
			}
			if strings.Contains(errOut, "tcp(host)") {
				t.Errorf("error echoes the DSN: %q", errOut)
			}
			if _, err := os.Stat(home); err == nil {
				t.Errorf("home was created")
			}
		})
	}
}

func TestEnvBadDSNDoesNotLeakPassword(t *testing.T) {
	for _, c := range []struct{ dsn, engine string }{
		{"postgres://reader:hunter2pw@db.example.com:notaport/shop", ""},
		{"mysql://reader:hunter2pw@db.example.com:notaport/shop", ""},
		{"mongodb://reader:hunter2pw@db.example.com:notaport/shop", ""},
		// A MySQL string in the go-sql-driver form that ends in .db is not SQLite.
		{"reader:hunter2pw@tcp(db.example.com)/x.db", ""},
		// The wrong QH_ENGINE must not echo the string as a missing file.
		{"reader:hunter2pw@tcp(db.example.com)/x.db", "sqlite"},
		{"postgres://reader:hunter2pw@db.example.com/shop", "sqlite"},
		{"reader:hunter2pw@tcp(db.example.com)/shop", "postgres"},
		// key=value text that happens to end in .db, and a prefix hiding another engine's string.
		{"host=h user=u password=hunter2pw dbname=app.db", ""},
		{"sqlite://reader:hunter2pw@tcp(db.example.com)/x.db", ""},
	} {
		clearQHEnv(t)
		t.Setenv("QH_DSN", c.dsn)
		t.Setenv("QH_ENGINE", c.engine)
		home := filepath.Join(t.TempDir(), "home")
		tmp := ephemeralTmp(t)
		out, errOut, code := run(t, home, "", "mcp")
		if code != 1 || !strings.HasPrefix(errOut, "error[") {
			t.Errorf("%s: %d %q", c.dsn, code, errOut)
		}
		if strings.Contains(out+errOut, "hunter2pw") {
			t.Errorf("%s leaks the password: %q %q", c.dsn, out, errOut)
		}
		if _, err := os.Stat(home); err == nil {
			t.Errorf("%s: home was created", c.dsn)
		}
		assertEmptyDir(t, tmp)
	}
}

// Strings that reach the sqlite engine by a wrong QH_ENGINE and pass its checks
// fail when the file is opened; that error must not show the string either.
func TestEnvSQLiteMisconfigurationDoesNotLeak(t *testing.T) {
	for _, dsn := range []string{
		"host=h user=u password=hunter2pw dbname=app.db", "u:hunter2pw/x@h/x.db", "//u:hunter2pw@h/x.db",
		"mongodb+srv:/u:hunter2pw@h/db",
	} {
		clearQHEnv(t)
		t.Setenv("QH_DSN", dsn)
		t.Setenv("QH_ENGINE", "sqlite")
		s, finish := envMCP(t, t.TempDir())
		res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"query": "SELECT 1"}})
		if err != nil {
			t.Fatal(err)
		}
		if text := res.Content[0].(*mcp.TextContent).Text; !res.IsError || strings.Contains(text, "hunter2pw") {
			t.Errorf("%s: %+v", dsn, res)
		}
		if code, errOut := finish(); code != 0 || strings.Contains(errOut, "hunter2pw") {
			t.Errorf("%s: exit %d: %s", dsn, code, errOut)
		}
	}
}

// Upper-case schemes are valid (RFC 3986); each must reach its own engine.
func TestEnvUpperCaseScheme(t *testing.T) {
	clearQHEnv(t)
	t.Setenv("QH_DSN", "SQLITE://"+testutil.SQLiteFixture(t))
	s, finish := envMCP(t, t.TempDir())
	if in := s.InitializeResult().Instructions; !strings.Contains(in, "db (sqlite, ro)") {
		t.Errorf("instructions: %q", in)
	}
	if out := callText(t, s, "query", map[string]any{"query": "SELECT count(*) AS n FROM users"}); !strings.HasPrefix(out, "n\n3\n") {
		t.Errorf("query: %q", out)
	}
	if code, errOut := finish(); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	// pgx would read POSTGRES://… as keywords and connect to the local socket
	// as the OS user; the scheme is lower-cased first, so the host is honoured.
	t.Setenv("QH_DSN", "POSTGRES://u:hunter2pw@127.0.0.1:1/db?connect_timeout=1")
	s, finish = envMCP(t, t.TempDir())
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"query": "SELECT 1"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !res.IsError || !strings.Contains(text, "127.0.0.1") || strings.Contains(text, "hunter2pw") {
		t.Errorf("query: %+v", res)
	}
	if code, errOut := finish(); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestNormalizeScheme(t *testing.T) {
	for in, want := range map[string]string{
		"POSTGRES://U:P@H/Db":      "postgres://U:P@H/Db",
		"PostgreSQL://u@h/db":      "postgresql://u@h/db",
		"MongoDB+SRV://u:p@h/db":   "mongodb+srv://u:p@h/db",
		"SQLITE:///Data/App.db":    "sqlite:///Data/App.db",
		"FILE:/Data/App.db":        "file:/Data/App.db",
		"C:/Data/App.db":           "C:/Data/App.db",
		"/Data/App.db":             "/Data/App.db",
		"host=H user=U dbname=D":   "host=H user=U dbname=D",
		"U:P@tcp(H)/Db":            "U:P@tcp(H)/Db",
		"postgres://already/lower": "postgres://already/lower",
	} {
		if got := normalizeScheme(in); got != want {
			t.Errorf("normalizeScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

// With a read-only root filesystem and no writable /tmp the store cannot be
// created; the error says how to fix it.
func TestEnvNeedsWritableTmp(t *testing.T) {
	clearQHEnv(t)
	t.Setenv("QH_DSN", testutil.SQLiteFixture(t))
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	_, errOut, code := run(t, filepath.Join(t.TempDir(), "home"), "", "mcp")
	if code != 1 || !strings.HasPrefix(errOut, "error[") || !strings.Contains(errOut, "cannot create a temporary store") ||
		!strings.Contains(errOut, "--tmpfs /tmp") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestMCPHelpListsEnvironment(t *testing.T) {
	out, _, code := run(t, t.TempDir(), "", "mcp", "--help")
	if code != 0 {
		t.Fatal(out)
	}
	for _, v := range []string{"QH_DSN", "QH_ENGINE", "QH_NAME", "QH_DESCRIPTION", "QH_MODE"} {
		if !strings.Contains(out, v) {
			t.Errorf("help lacks %s: %q", v, out)
		}
	}
}

// Nothing is tested at startup, so an unreachable database does not stop the
// server; the failure comes with each query.
func TestEnvUnreachableDatabaseFailsPerQuery(t *testing.T) {
	clearQHEnv(t)
	t.Setenv("QH_DSN", "postgres://reader:hunter2pw@127.0.0.1:1/shop?connect_timeout=1")
	s, finish := envMCP(t, t.TempDir())
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"query": "SELECT 1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || strings.Contains(res.Content[0].(*mcp.TextContent).Text, "hunter2pw") {
		t.Errorf("query: %+v", res)
	}
	if code, errOut := finish(); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestInferEngine(t *testing.T) {
	for dsn, want := range map[string]string{
		"postgres://u:p@h/db":               "postgres",
		"postgresql://u:p@h/db":             "postgres",
		"POSTGRES://u:p@h/db":               "postgres",
		"mysql://u:p@h/db":                  "mysql",
		"mariadb://u:p@h/db":                "mariadb",
		"mongodb://u:p@h/db":                "mongodb",
		"mongodb+srv://u:p@h/db":            "mongodb",
		"sqlite:///data/app.db":             "sqlite",
		"sqlite://./app":                    "sqlite",
		"file:/data/app":                    "sqlite",
		"/data/app.db":                      "sqlite",
		"./app.sqlite":                      "sqlite",
		"C:/data/App.SQLITE3":               "sqlite",
		"u:p@tcp(host:3306)/db":             "", // go-sql-driver form: needs QH_ENGINE
		"host=h user=u password=p dbname=d": "",
		"":                                  "",
		"/data/app.txt":                     "",
	} {
		if got, _ := inferEngine(dsn); got != want {
			t.Errorf("inferEngine(%q) = %q, want %q", dsn, got, want)
		}
	}
}

func TestEnvEngineOverridesInference(t *testing.T) {
	clearQHEnv(t)
	t.Setenv("QH_DSN", "u:p@tcp(127.0.0.1:1)/db")
	t.Setenv("QH_ENGINE", "mysql")
	s, finish := envMCP(t, t.TempDir())
	if in := s.InitializeResult().Instructions; !strings.Contains(in, "db (mysql, ro)") {
		t.Errorf("instructions: %q", in)
	}
	if code, errOut := finish(); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}
