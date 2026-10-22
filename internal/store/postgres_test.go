package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/store/storetest"
)

// pgServer starts a Postgres container and returns the admin DSN.
func pgServer(t *testing.T, image string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, image, tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("qh"), tcpostgres.WithPassword("store-pw-123"), tcpostgres.BasicWaitStrategies())
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

var dbSeq atomic.Int64

// freshDB creates an empty database on the server and returns its DSN.
func freshDB(t *testing.T, admin string) string {
	t.Helper()
	db, err := sql.Open("pgx", admin)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	name := fmt.Sprintf("qh_test_%d", dbSeq.Add(1))
	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	return u.String()
}

func openPG(t *testing.T, dsn string) *store.Postgres {
	t.Helper()
	s, err := store.OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPostgresConformance(t *testing.T) {
	for _, image := range []string{"postgres:13-alpine", "postgres:17-alpine"} {
		t.Run(image, func(t *testing.T) {
			admin := pgServer(t, image)
			storetest.Run(t, func(t *testing.T) store.Store { return openPG(t, freshDB(t, admin)) })
		})
	}
}

func TestPostgresConcurrentFirstOpen(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := store.OpenPostgres(context.Background(), dsn)
			if err != nil {
				errs <- err
				return
			}
			s.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	db, _ := sql.Open("pgx", dsn)
	defer db.Close()
	var n, ws int
	db.QueryRow(`SELECT count(*) FROM qh.schema_migrations`).Scan(&n)
	db.QueryRow(`SELECT count(*) FROM qh.workspaces`).Scan(&ws)
	if n != 1 || ws != 1 {
		t.Fatalf("migrations applied %d times, %d workspaces", n, ws)
	}
}

func TestPostgresNonUTCSessionReturnsUTC(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	s := openPG(t, dsn+"&timezone=America/Sao_Paulo")
	ctx := context.Background()
	c := &store.Connection{ID: store.NewID(), WorkspaceID: store.DefaultWorkspace, Name: "x", Engine: "postgres",
		Secret: []byte{1}, SecretKeyID: 1, Mode: "ro", MaxRows: 1, TimeoutMS: 1, PoolSize: 1}
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConnection(ctx, store.DefaultWorkspace, "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.CreatedAt.Location() != time.UTC || !got.CreatedAt.Equal(c.CreatedAt) {
		t.Fatalf("created %v (loc %v), wrote %v", got.CreatedAt, got.CreatedAt.Location(), c.CreatedAt)
	}
}

func TestPostgresErrorsHideThePassword(t *testing.T) {
	store.SetPGOpenRetry(t, 300*time.Millisecond)
	_, err := store.OpenPostgres(context.Background(), "postgres://qh:hunter2-secret@127.0.0.1:1/qh?sslmode=disable&connect_timeout=1")
	if err == nil || strings.Contains(err.Error(), "hunter2-secret") {
		t.Fatalf("%v", err)
	}
	_, err = store.OpenPostgres(context.Background(), "postgres://qh:hunter2-secret@[::1/bad")
	if err == nil || strings.Contains(err.Error(), "hunter2-secret") {
		t.Fatalf("%v", err)
	}
}

func TestPostgresResealBlocksConcurrentUpdate(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	a, b := openPG(t, dsn), openPG(t, dsn)
	ctx := context.Background()
	c := &store.Connection{ID: store.NewID(), WorkspaceID: store.DefaultWorkspace, Name: "x", Engine: "postgres",
		Secret: []byte{1}, SecretKeyID: 1, Mode: "ro", MaxRows: 1, TimeoutMS: 1, PoolSize: 1}
	if err := a.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	inFn := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := a.ResealConnections(ctx, func(*store.Connection) ([]byte, uint32, error) {
			close(inFn)
			<-release
			return []byte{9}, 2, nil
		})
		done <- err
	}()
	<-inFn
	updated := make(chan error, 1)
	go func() {
		u := *c
		u.Description = "changed"
		updated <- b.UpdateConnection(ctx, &u)
	}()
	select {
	case err := <-updated:
		t.Fatalf("update did not wait for the reseal: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-updated; err != nil {
		t.Fatalf("update after reseal: %v", err)
	}
	// The update ran after the reseal committed: the reseal kept the version,
	// so the update was not refused, and it wrote its own secret (key 1) last.
	// (Core seals with the current key on every update, so it writes key 2.)
	got, _ := a.GetConnection(ctx, store.DefaultWorkspace, "x")
	if got.SecretKeyID != 1 || string(got.Secret) != "\x01" || got.Description != "changed" || got.Version != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestOpenChoosesTheStore(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(context.Background(), "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*store.SQLite); !ok {
		t.Fatalf("empty DSN opened %T", s)
	}
	s.Close()
	if _, err := store.Open(context.Background(), "mysql://x", dir); err != store.ErrInvalidDSN {
		t.Fatalf("mysql DSN: %v", err)
	}
}

func TestPostgresPasswordFile(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	u, _ := url.Parse(dsn)
	pw, _ := u.User.Password()
	u.User = url.User(u.User.Username())
	f := t.TempDir() + "/pw"
	if err := writeFile(f, pw+"\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QH_STORE_PASSWORD_FILE", f)
	openPG(t, u.String()).Close()
}

func TestPostgresPasswordFileUnreadable(t *testing.T) {
	missing := t.TempDir() + "/no-such-pw"
	t.Setenv("QH_STORE_PASSWORD_FILE", missing)
	_, err := store.OpenPostgres(context.Background(), "postgres://qh@127.0.0.1:1/qh")
	// The cause's wording differs by OS, so check its identity, not its text.
	if err == nil || !strings.Contains(err.Error(), missing) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want the path and the cause, got %v", err)
	}
}

// A role that owns a pre-created qh schema but cannot create schemas in the
// database must still be able to open the store.
func TestPostgresOpenWithPrecreatedSchema(t *testing.T) {
	admin := pgServer(t, "postgres:17-alpine")
	dsn := freshDB(t, admin)
	u, _ := url.Parse(dsn)
	dbName := strings.TrimPrefix(u.Path, "/")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE ROLE app LOGIN PASSWORD 'app-pw-123'`,
		`REVOKE CREATE ON DATABASE ` + dbName + ` FROM PUBLIC`,
		`REVOKE CREATE ON DATABASE ` + dbName + ` FROM app`,
		`CREATE SCHEMA qh AUTHORIZATION app`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	u.User = url.UserPassword("app", "app-pw-123")
	s := openPG(t, u.String())
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func writeFile(path, s string) error { return os.WriteFile(path, []byte(s), 0o600) }

func TestPostgresOpenBoundsEachAttempt(t *testing.T) {
	store.SetPGOpenRetry(t, 2*time.Second)
	start := time.Now()
	_, err := store.OpenPostgres(context.Background(), "postgres://qh:unroutable-pw@10.255.255.1:5432/qh?sslmode=disable")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("open took %v", elapsed)
	}
	if err == nil || strings.Contains(err.Error(), "unroutable-pw") {
		t.Fatalf("%v", err)
	}
}

func TestPostgresOpenFailsFastOnWrongPassword(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	store.SetPGOpenRetry(t, 30*time.Second)
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword(u.User.Username(), "wrong-pw-456")
	start := time.Now()
	_, err := store.OpenPostgres(context.Background(), u.String())
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("open took %v, want a fast failure", elapsed)
	}
	if err == nil || strings.Contains(err.Error(), "wrong-pw-456") {
		t.Fatalf("%v", err)
	}
}

func TestPostgresCreateKeyIfNoneRace(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	var wg sync.WaitGroup
	var created atomic.Int64
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := store.OpenPostgres(context.Background(), dsn)
			if err != nil {
				t.Error(err)
				return
			}
			defer s.Close()
			ok, err := s.CreateKeyIfNone(context.Background(), &store.APIKey{ID: store.NewID(), Prefix: fmt.Sprintf("qh_race%04d", i), Hash: []byte{1}, Scopes: []string{"admin"}})
			if err != nil {
				t.Error(err)
			}
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created %d keys", created.Load())
	}
}

func TestPostgresInvalidations(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	a, b := openPG(t, dsn), openPG(t, dsn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chA, err := a.Invalidations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chB, err := b.Invalidations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recv := func(ch <-chan string) string {
		t.Helper()
		select {
		case id := <-ch:
			return id
		case <-time.After(10 * time.Second):
			t.Fatal("no notification")
			return ""
		}
	}
	if err := a.Invalidate(ctx, "conn-1"); err != nil {
		t.Fatal(err)
	}
	if got := recv(chB); got != "conn-1" {
		t.Fatalf("got %q", got)
	}
	// a's own notice is not echoed back to a: notices arrive in order, so the
	// first thing a sees must be b's.
	if err := b.Invalidate(ctx, "conn-b"); err != nil {
		t.Fatal(err)
	}
	if got := recv(chA); got != "conn-b" {
		t.Fatalf("a got %q, want b's \"conn-b\" (its own notice was echoed)", got)
	}
	// Kill the listening sessions: the listeners reconnect and send "".
	db, _ := sql.Open("pgx", dsn)
	defer db.Close()
	if _, err := db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND datname = current_database() AND query LIKE 'LISTEN%'`); err != nil {
		t.Fatal(err)
	}
	if got := recv(chB); got != "" {
		t.Fatalf("after reconnect got %q, want \"\"", got)
	}
	if got := recv(chA); got != "" {
		t.Fatalf("a after reconnect got %q, want \"\"", got)
	}
	if err := a.Invalidate(ctx, "conn-2"); err != nil {
		t.Fatal(err)
	}
	if got := recv(chB); got != "conn-2" {
		t.Fatalf("after reconnect got %q", got)
	}
	cancel()
	for range chA { // must close
	}
	for range chB {
	}
}

// killOthers terminates every other client session on dsn's database and
// waits until they are gone. admin must hold a single connection.
func killOthers(t *testing.T, admin *sql.DB) {
	t.Helper()
	const others = `FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND datname = current_database() AND backend_type = 'client backend'`
	if _, err := admin.Exec(`SELECT pg_terminate_backend(pid) ` + others); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		var n int
		if err := admin.QueryRow(`SELECT count(*) ` + others).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions survived pg_terminate_backend", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// adminDB opens a pool of conns connections to dsn. killOthers needs 1;
// killInFlight needs 2 (one holds the lock, one watches).
func adminDB(t *testing.T, dsn string, conns int) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(conns)
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	return db
}

// killInFlight runs call while admin holds an ACCESS EXCLUSIVE lock on table,
// so call's statement (matching the LIKE pattern query) waits for it on a live
// session; it terminates that session up to kills times, each time waiting for
// the next attempt to queue, then releases the lock. It returns call's error
// and how many sessions it terminated. No pool ping can step in between: each
// statement is already running when its session ends.
func killInFlight(t *testing.T, admin *sql.DB, table, query string, kills int, call func() error) (error, int) {
	t.Helper()
	tx, err := admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`LOCK TABLE ` + table + ` IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- call() }()
	killed := 0
	for killed < kills {
		var pid int64
		for deadline := time.Now().Add(10 * time.Second); ; {
			select {
			case err := <-done: // call gave up before all the kills
				return err, killed
			default:
			}
			err := admin.QueryRow(`SELECT pid FROM pg_stat_activity WHERE datname = current_database()
				AND wait_event_type = 'Lock' AND query LIKE $1`, query).Scan(&pid)
			if err == nil {
				break
			}
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
			if time.Now().After(deadline) {
				t.Fatalf("no statement like %q waits for %s", query, table)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if _, err := admin.Exec(`SELECT pg_terminate_backend($1)`, pid); err != nil {
			t.Fatal(err)
		}
		// Wait until it is gone, so releasing the lock cannot let it finish.
		for deadline := time.Now().Add(5 * time.Second); ; {
			var n int
			if err := admin.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("session %d survived pg_terminate_backend", pid)
			}
			time.Sleep(5 * time.Millisecond)
		}
		killed++
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		return err, killed
	case <-time.After(10 * time.Second):
		t.Fatal("the call did not return after the lock was released")
		return nil, killed
	}
}

func isAdminShutdown(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "57P01"
}

// When the server ends the session a read is running on (a restart, a
// failover, an administrator), the read is retried and succeeds.
func TestPostgresReadsSurviveTerminatedConnections(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	s := openPG(t, dsn)
	admin := adminDB(t, dsn, 2)
	ctx := context.Background()
	c := &store.Connection{ID: store.NewID(), WorkspaceID: store.DefaultWorkspace, Name: "x", Engine: "postgres",
		Secret: []byte{1}, SecretKeyID: 1, Mode: "ro", MaxRows: 1, TimeoutMS: 1, PoolSize: 1}
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		name, table string
		fn          func() error
	}{
		{"GetConnection", "qh.connections", func() error { _, err := s.GetConnection(ctx, store.DefaultWorkspace, "x"); return err }},
		{"ListConnections", "qh.connections", func() error { _, err := s.ListConnections(ctx, store.DefaultWorkspace); return err }},
		{"ListWorkspaces", "qh.workspaces", func() error { _, err := s.ListWorkspaces(ctx); return err }},
		{"GetKeyByPrefix", "qh.api_keys", func() error {
			_, err := s.GetKeyByPrefix(ctx, "qh_none")
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}},
		{"ListKeys", "qh.api_keys", func() error { _, err := s.ListKeys(ctx, store.DefaultWorkspace); return err }},
		{"ListAllKeys", "qh.api_keys", func() error { _, err := s.ListAllKeys(ctx); return err }},
		{"CountKeys", "qh.api_keys", func() error { _, err := s.CountKeys(ctx); return err }},
		{"ListQueryLog", "qh.query_log", func() error {
			_, err := s.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: store.DefaultWorkspace})
			return err
		}},
	} {
		t.Run(r.name, func(t *testing.T) {
			if err, _ := killInFlight(t, admin, r.table, "SELECT %"+r.table+"%", 1, r.fn); err != nil {
				t.Fatalf("after its session was terminated: %v", err)
			}
		})
	}
	// Ping takes no lock, so it only gets a resilience smoke test: end every
	// pooled session, then ping. database/sql or pgx's ResetSession may recover
	// Ping on their own, so this does not prove read's retry.
	t.Run("Ping", func(t *testing.T) {
		killOthers(t, adminDB(t, dsn, 1))
		if err := s.Ping(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

// After a restart several pooled connections may be dead: the read keeps
// trying, one attempt per pooled connection plus a fresh one, and no more.
func TestPostgresReadsSurviveSeveralDeadConnections(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	s := openPG(t, dsn)
	admin := adminDB(t, dsn, 2)
	ctx := context.Background()
	count := func() error { _, err := s.CountKeys(ctx); return err }
	if err, n := killInFlight(t, admin, "qh.api_keys", "SELECT %qh.api_keys%", 4, count); err != nil || n != 4 {
		t.Fatalf("after %d dead sessions: %v", n, err)
	}
	err, n := killInFlight(t, admin, "qh.api_keys", "SELECT %qh.api_keys%", store.PGMaxConns+2, count)
	if !isAdminShutdown(err) || n != store.PGMaxConns+1 {
		t.Fatalf("a read retried %d times and returned %v; want %d attempts, then 57P01", n, err, store.PGMaxConns+1)
	}
}

// A write is never retried: its first attempt may have committed.
func TestPostgresWritesAreNotRetried(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	s := openPG(t, dsn)
	admin := adminDB(t, dsn, 2)
	ctx := context.Background()
	err, _ := killInFlight(t, admin, "qh.workspaces", "INSERT INTO qh.workspaces%", 1, func() error {
		_, err := s.CreateWorkspace(ctx, "w1")
		return err
	})
	if !isAdminShutdown(err) {
		t.Fatalf("a write whose session was terminated returned %v, want 57P01", err)
	}
	ws, err := s.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.ID == "w1" {
			t.Fatal("the failed write was applied (retried)")
		}
	}
	if _, err := s.CreateWorkspace(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
}

// tcpProxy forwards TCP connections to a target. freeze makes the open
// connections drop all traffic both ways without closing (a half-open link);
// refuse makes new connections close at once (refused counts them); kill
// closes the open ones.
type tcpProxy struct {
	ln      net.Listener
	target  string
	refuse  atomic.Bool
	refused atomic.Int64
	mu      sync.Mutex
	links   []*proxyLink
}

type proxyLink struct {
	client, server net.Conn
	frozen         atomic.Bool
}

func startProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &tcpProxy{ln: ln, target: target}
	go p.serve()
	t.Cleanup(func() {
		ln.Close()
		p.kill()
	})
	return p
}

// via returns dsn pointed at the proxy.
func (p *tcpProxy) via(dsn string) string {
	u, _ := url.Parse(dsn)
	u.Host = p.ln.Addr().String()
	return u.String()
}

func (p *tcpProxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		if p.refuse.Load() {
			go p.refuseConn(c)
			continue
		}
		s, err := net.Dial("tcp", p.target)
		if err != nil {
			c.Close()
			continue
		}
		l := &proxyLink{client: c, server: s}
		p.mu.Lock()
		p.links = append(p.links, l)
		p.mu.Unlock()
		go l.pipe(s, c)
		go l.pipe(c, s)
	}
}

// cancelRequestCode opens a CancelRequest packet (pgx sends one for a lost
// connection); it is not a connection attempt.
const cancelRequestCode = 80877102

// refuseConn counts c as refused, unless it is a cancel request, and then
// closes it: the count is updated before the client sees the close.
func (p *tcpProxy) refuseConn(c net.Conn) {
	defer c.Close()
	var head [8]byte
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, head[:]); err == nil && binary.BigEndian.Uint32(head[4:]) == cancelRequestCode {
		return
	}
	p.refused.Add(1)
}

func (l *proxyLink) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if l.frozen.Load() {
			if err != nil {
				return // a frozen link never passes on a close either
			}
			continue
		}
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	dst.Close()
	src.Close()
}

func (p *tcpProxy) freeze() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.links {
		l.frozen.Store(true)
	}
}

func (p *tcpProxy) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.links {
		l.client.Close()
		l.server.Close()
	}
	p.links = nil
}

func recvWithin(t *testing.T, ch <-chan string, d time.Duration) string {
	t.Helper()
	select {
	case id, ok := <-ch:
		if !ok {
			t.Fatal("the invalidation channel closed")
		}
		return id
	case <-time.After(d):
		t.Fatal("no notification")
		return ""
	}
}

// A listener with nothing to hear pings its connection after each quiet
// interval; it keeps that connection and sends no "drop everything".
func TestPostgresQuietListenerStaysConnected(t *testing.T) {
	store.SetListenTiming(t, 200*time.Millisecond, 2*time.Second)
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	a, b := openPG(t, dsn), openPG(t, dsn)
	admin := adminDB(t, dsn, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := a.Invalidations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listener := func() (pid int64, query string) {
		t.Helper()
		if err := admin.QueryRow(`SELECT pid, query FROM pg_stat_activity WHERE pid <> pg_backend_pid()
			AND datname = current_database() AND (query LIKE 'LISTEN%' OR query LIKE '-- ping%')`).Scan(&pid, &query); err != nil {
			t.Fatal(err)
		}
		return pid, query
	}
	before, _ := listener()
	select {
	case id := <-ch:
		t.Fatalf("a quiet listener sent %q", id)
	case <-time.After(1500 * time.Millisecond): // about seven quiet intervals
	}
	after, query := listener()
	if after != before {
		t.Fatalf("the listener reconnected (backend %d, then %d)", before, after)
	}
	if !strings.HasPrefix(query, "-- ping") {
		t.Fatalf("the listener's last statement is %q, want its ping", query)
	}
	if err := b.Invalidate(ctx, "conn-1"); err != nil {
		t.Fatal(err)
	}
	if got := recvWithin(t, ch, 10*time.Second); got != "conn-1" {
		t.Fatalf("got %q", got)
	}
	// A terminated listener still reconnects and drops everything.
	killOthers(t, admin)
	if got := recvWithin(t, ch, 10*time.Second); got != "" {
		t.Fatalf("after reconnect got %q, want \"\"", got)
	}
}

// A connection that silently stops answering (a half-open TCP link) fails the
// ping; the listener reconnects and drops everything.
func TestPostgresListenerDetectsHalfOpenConnection(t *testing.T) {
	store.SetListenTiming(t, 200*time.Millisecond, 500*time.Millisecond)
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	u, _ := url.Parse(dsn)
	px := startProxy(t, u.Host)
	a, b := openPG(t, px.via(dsn)), openPG(t, dsn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := a.Invalidations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	px.freeze()
	if got := recvWithin(t, ch, 10*time.Second); got != "" {
		t.Fatalf("got %q, want \"\"", got)
	}
	if err := b.Invalidate(ctx, "conn-1"); err != nil {
		t.Fatal(err)
	}
	if got := recvWithin(t, ch, 10*time.Second); got != "conn-1" {
		t.Fatalf("after reconnect got %q", got)
	}
}

// syncBuffer is a bytes.Buffer safe for a logger and a test to share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A listener that cannot reconnect says so once (not once per attempt), never
// with the password, and says when it is back.
func TestPostgresListenerLogsReconnectFailures(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	u, _ := url.Parse(dsn)
	pw, _ := u.User.Password()
	px := startProxy(t, u.Host)
	a := openPG(t, px.via(dsn))
	var buf syncBuffer
	a.SetLogger(slog.New(slog.NewJSONHandler(&buf, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := a.Invalidations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	px.refuse.Store(true)
	px.kill()
	// Let at least two reconnects fail: once the first failure is logged, the
	// next refusal is the second attempt (cancel requests are not counted).
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); !cond(); {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s:\n%s", what, buf.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("the first failed reconnect", func() bool { return strings.Contains(buf.String(), `"attempt":1`) })
	after := px.refused.Load()
	waitFor("a second failed reconnect", func() bool { return px.refused.Load() > after })
	px.refuse.Store(false)
	if got := recvWithin(t, ch, 10*time.Second); got != "" {
		t.Fatalf("got %q, want \"\"", got)
	}
	out := buf.String()
	if strings.Contains(out, pw) {
		t.Fatalf("the password was logged:\n%s", out)
	}
	var lost, failed, back int
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var r struct {
			Level    string
			Msg      string
			Attempt  int
			Attempts int
			Error    string
		}
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		switch {
		case r.Level == "WARN" && r.Attempt == 0 && r.Error != "":
			lost++
		case r.Level == "WARN" && r.Attempt > 0 && r.Error != "":
			failed++
		case r.Level == "INFO" && r.Attempts >= 3:
			back++
		default:
			t.Errorf("unexpected record %s", line)
		}
	}
	if lost != 1 || failed != 1 || back != 1 {
		t.Fatalf("logged %d losses, %d failed attempts, %d reconnects; want 1 each:\n%s", lost, failed, back, out)
	}
}
