package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
