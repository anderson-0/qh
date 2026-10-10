package postgres

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/engine/enginetest"
	"github.com/anderson-0/qh/internal/format"
	"github.com/anderson-0/qh/internal/qerr"
)

const fixture = `
CREATE TYPE order_status AS ENUM ('pending', 'paid', 'shipped');
CREATE TABLE users (id bigint PRIMARY KEY, email text NOT NULL UNIQUE, name text, bio text, avatar bytea, created_at timestamp);
COMMENT ON TABLE users IS 'App users';
COMMENT ON COLUMN users.email IS 'Login email';
CREATE TABLE orders (id bigserial PRIMARY KEY, user_id bigint NOT NULL REFERENCES users(id),
  status order_status NOT NULL DEFAULT 'pending', total numeric(12,2), note text);
CREATE INDEX orders_user_id ON orders (user_id);
CREATE TABLE order_items (order_id bigint NOT NULL REFERENCES orders(id), sku text NOT NULL, qty int NOT NULL,
  PRIMARY KEY (order_id, sku));
CREATE VIEW paid_orders AS SELECT id, user_id, total FROM orders WHERE status = 'paid';
CREATE TABLE "AuditLog" (id bigint PRIMARY KEY, "EventName" text);
INSERT INTO users VALUES
  (1, 'alice@example.com', 'Alice', E'line1\nline2\tx', '\xdeadbeef', '2025-03-14 10:22:31'),
  (2, 'zoe@example.com', 'Zoë 🚀', NULL, NULL, NULL),
  (3, 'nobody@example.com', NULL, NULL, NULL, NULL);
INSERT INTO orders (id, user_id, status, total)
  SELECT i, (i % 3) + 1, (CASE WHEN i % 2 = 0 THEN 'paid' ELSE 'pending' END)::order_status, i * 1.5
  FROM generate_series(1, 250) i;
SELECT setval('orders_id_seq', 250);
INSERT INTO order_items SELECT i, 'sku-1', 1 FROM generate_series(1, 10) i
  UNION ALL SELECT i, 'sku-2', 2 FROM generate_series(1, 10) i;
ANALYZE;
`

func startPostgres(t *testing.T, image string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("qh"), tcpostgres.WithUsername("qh"), tcpostgres.WithPassword("secret-pw-123"),
		tcpostgres.BasicWaitStrategies())
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return dsn
}

func specs(dsn string) (engine.ConnSpec, engine.ConnSpec) {
	return engine.ConnSpec{ID: "ro", Version: 1, Engine: "postgres", DSN: dsn, Mode: engine.ModeRO, PoolSize: 3},
		engine.ConnSpec{ID: "rw", Version: 1, Engine: "postgres", DSN: dsn, Mode: engine.ModeRW, PoolSize: 3}
}

func TestConformance(t *testing.T) {
	for _, image := range []string{"postgres:13-alpine", "postgres:17-alpine"} {
		t.Run(image, func(t *testing.T) {
			enginetest.Run(t, enginetest.Harness{
				Engine:    Engine{},
				Setup:     func(t *testing.T) (engine.ConnSpec, engine.ConnSpec) { return specs(startPostgres(t, image)) },
				SlowQuery: "SELECT pg_sleep(5)",
				ReadOnlyEscapes: []string{
					"SELECT nextval('orders_id_seq')",
					"WITH d AS (DELETE FROM order_items RETURNING *) SELECT count(*) FROM d",
					"DO $$ BEGIN DELETE FROM order_items; END $$",
					"SELECT * INTO evil FROM users",
					"CREATE TEMP TABLE evil_tmp (x int)",
					"SET TRANSACTION READ WRITE",
					"SELECT 1; COMMIT; DELETE FROM order_items",
				},
				GoldenPrefix: "postgres",
			})
		})
	}
}

func TestMixedCaseIdentifiers(t *testing.T) {
	ro, _ := specs(startPostgres(t, "postgres:17-alpine"))
	c, err := Engine{}.Open(context.Background(), ro)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out, n := format.Schema(cat, format.SchemaOpts{Pattern: "audit"}); n != 1 || !strings.Contains(out, "public.AuditLog") || !strings.Contains(out, "EventName text") {
		t.Fatalf("n=%d out=%q", n, out)
	}
	_, err = c.Query(context.Background(), `SELECT eventname FROM "AuditLog"`, engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 5 * time.Second})
	if e := qerr.From(err); e.UnknownColumn != "eventname" {
		t.Fatalf("err = %#v", e)
	}
}

func TestBusy(t *testing.T) {
	ro, _ := specs(startPostgres(t, "postgres:17-alpine"))
	ro.PoolSize = 1
	c, err := Engine{}.Open(context.Background(), ro)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	old := slotWait
	slotWait = 200 * time.Millisecond
	defer func() { slotWait = old }()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.Query(context.Background(), "SELECT pg_sleep(2)", engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 5 * time.Second})
	}()
	time.Sleep(300 * time.Millisecond)
	_, err = c.Query(context.Background(), "SELECT 1", engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 5 * time.Second})
	if e := qerr.From(err); err == nil || e.Category != qerr.Busy {
		t.Fatalf("err = %v", err)
	}
	wg.Wait()
}

func TestConnectionFailed(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost"} {
		_, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: "postgres://u:hunter22@" + host + ":1/db?connect_timeout=2", Mode: engine.ModeRO, PoolSize: 1})
		if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed {
			t.Fatalf("%s: err = %v", host, err)
		}
		if strings.Contains(err.Error(), "\n") || strings.Contains(err.Error(), "hunter22") {
			t.Fatalf("%s: err = %q", host, err.Error())
		}
	}
}

func TestAuthFailed(t *testing.T) {
	dsn := startPostgres(t, "postgres:17-alpine")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("qh", "wrong-pw-456")
	_, err = Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: u.String(), Mode: engine.ModeRO, PoolSize: 1})
	if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed || !strings.Contains(e.Message, "authentication failed") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "wrong-pw-456") {
		t.Fatalf("password leaked: %q", err.Error())
	}
}

func TestInspect(t *testing.T) {
	info, err := Engine{}.Inspect("postgres://bob:hunter22@db.example.com:6543/shop?sslmode=require")
	if err != nil {
		t.Fatal(err)
	}
	if info.Host != "db.example.com" || info.Port != 6543 || info.Database != "shop" || info.User != "bob" || info.Password != "hunter22" {
		t.Fatalf("info = %+v", info)
	}
	if info.Redacted != "postgres://bob:***@db.example.com:6543/shop?sslmode=require" {
		t.Fatalf("redacted = %q", info.Redacted)
	}
	info, err = Engine{}.Inspect("host=db user=bob password=hunter22 dbname=shop")
	if err != nil || info.Redacted != "host=db user=bob password=*** dbname=shop" {
		t.Fatalf("keyword form = %+v, %v", info, err)
	}
	if _, err := (Engine{}).Inspect("postgres://bob:hunter22@[bad"); err == nil || strings.Contains(err.Error(), "hunter22") {
		t.Fatalf("bad dsn err = %v", err)
	}
}

func TestShortType(t *testing.T) {
	cases := map[string]string{
		"timestamp without time zone": "timestamp",
		"timestamp(3) with time zone": "timestamptz(3)",
		"time with time zone":         "timetz",
		"character varying(255)":      "varchar(255)",
		"double precision":            "float8",
		"numeric(12,2)":               "numeric(12,2)",
	}
	for in, want := range cases {
		if got := shortType(in); got != want {
			t.Errorf("shortType(%q) = %q, want %q", in, got, want)
		}
	}
}
