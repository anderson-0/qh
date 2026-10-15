package postgres

import (
	"context"
	"errors"
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
		if msg := err.Error(); !(strings.Contains(msg, "refused") || strings.Contains(msg, "dial")) || strings.HasSuffix(msg, ":") {
			t.Fatalf("%s: message lacks a cause: %q", host, msg)
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
	info, err = Engine{}.Inspect("postgres://bob:hunter22@db/shop?sslmode=require&sslpassword=keypass99&Password=other77")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(info.Redacted, "keypass99") || strings.Contains(info.Redacted, "other77") || strings.Contains(info.Redacted, "hunter22") ||
		!strings.Contains(info.Redacted, "sslmode=require") || !strings.Contains(info.Redacted, "sslpassword=***") {
		t.Fatalf("query-parameter passwords not redacted: %q", info.Redacted)
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

// Session state set by a user statement must not leak to the next user of the
// pooled connection (pool size 1 forces reuse).
func TestSessionStateDoesNotLeak(t *testing.T) {
	dsn := startPostgres(t, "postgres:17-alpine")
	ro, _ := specs(dsn)
	ro.PoolSize = 1
	ctx := context.Background()
	c, err := Engine{}.Open(ctx, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	opts := engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 5 * time.Second}

	if _, err := c.Query(ctx, `SELECT set_config('search_path', 'nowhere', false)`, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(ctx, `SELECT count(*) FROM users`, opts); err != nil {
		t.Fatalf("search_path leaked: %v", err)
	}

	if _, err := c.Query(ctx, `SELECT set_config('bytea_output', 'escape', false)`, opts); err != nil {
		t.Fatal(err)
	}
	res, err := c.Query(ctx, `SELECT avatar FROM users WHERE id = 1`, opts)
	if err != nil {
		t.Fatal(err)
	}
	if v := res.Rows[0][0]; !v.Binary || v.Size != 4 {
		t.Fatalf("bytea_output leaked: %+v", v)
	}

	if _, err := c.Query(ctx, `SELECT pg_advisory_lock(42)`, opts); err != nil {
		t.Fatal(err)
	}
	other, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(ctx)
	// Run another statement so the pooled connection is released and reset.
	if _, err := c.Query(ctx, `SELECT 1`, opts); err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := other.QueryRow(ctx, `SELECT pg_try_advisory_lock(42)`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("advisory lock still held by the pooled session")
	}
}

// A failed statement leaves its transaction aborted; the pipeline's ROLLBACK
// and DISCARD ALL must leave the single pooled connection usable.
func TestPipelineRecoversAfterErrors(t *testing.T) {
	dsn := startPostgres(t, "postgres:17-alpine")
	_, rw := specs(dsn)
	rw.PoolSize = 1
	ctx := context.Background()
	c, err := Engine{}.Open(ctx, rw)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	opts := engine.QueryOpts{MaxRows: 10, Timeout: 5 * time.Second}
	short := engine.QueryOpts{MaxRows: 10, Timeout: 100 * time.Millisecond}
	for _, bad := range []struct {
		q    string
		o    engine.QueryOpts
		code qerr.Category
	}{
		{`SELECT nope FROM users`, opts, qerr.QueryError},
		{`SELECT 1/0`, opts, qerr.QueryError},
		{`SELECT pg_sleep(2)`, short, qerr.Timeout},
		{`INSERT INTO users (id, email) VALUES (1, 'dup@example.com')`, opts, qerr.QueryError},
		{`COPY users TO STDOUT`, opts, qerr.Invalid},
	} {
		_, err := c.Query(ctx, bad.q, bad.o)
		var qe *qerr.Error
		if !errors.As(err, &qe) || qe.Category != bad.code {
			t.Fatalf("%s: got %v, want %s", bad.q, err, bad.code)
		}
		res, err := c.Query(ctx, `SELECT count(*) FROM users`, opts)
		if err != nil {
			t.Fatalf("after %s: %v", bad.q, err)
		}
		if got := res.Rows[0][0].Text; got != "3" {
			t.Fatalf("after %s: count = %s", bad.q, got)
		}
	}
	res, err := c.Query(ctx, `UPDATE users SET name = name WHERE id <= 2`, opts)
	if err != nil || res.RowsAffected != 2 {
		t.Fatalf("update: %+v, %v", res, err)
	}
}

func TestCancellationAndRecovery(t *testing.T) {
	for _, image := range []string{"postgres:13-alpine", "postgres:17-alpine"} {
		t.Run(image, func(t *testing.T) {
			dsn := startPostgres(t, image)
			_, spec := specs(dsn)
			spec.PoolSize = 1
			ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			c, err := Engine{}.Open(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			observer, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close(context.Background())
			for _, kind := range []string{"context cancel", "external cancel", "statement timeout", "context deadline"} {
				t.Run(kind, func(t *testing.T) {
					qctx, cancel := context.WithCancel(ctx)
					defer cancel()
					opts := engine.QueryOpts{MaxRows: 1, Timeout: 5 * time.Second}
					want := qerr.QueryError
					switch kind {
					case "statement timeout":
						opts.Timeout = 100 * time.Millisecond
						want = qerr.Timeout
					case "context deadline":
						var deadlineCancel context.CancelFunc
						qctx, deadlineCancel = context.WithTimeout(qctx, 100*time.Millisecond)
						defer deadlineCancel()
						want = qerr.Timeout
					}
					done := make(chan error, 1)
					go func() {
						_, err := c.Query(qctx, "SELECT pg_sleep(10)", opts)
						done <- err
					}()
					if want == qerr.QueryError {
						// Wait for the actual query to sleep before canceling it.
						// The observer is outside the one-connection engine pool.
						var pid int
						for {
							err := observer.QueryRow(ctx, `SELECT pid FROM pg_stat_activity
WHERE application_name = 'qh' AND state = 'active' AND wait_event = 'PgSleep'`).Scan(&pid)
							if err == nil {
								break
							}
							if !errors.Is(err, pgx.ErrNoRows) {
								t.Fatal(err)
							}
							select {
							case err := <-done:
								t.Fatalf("query ended before cancellation: %v", err)
							case <-ctx.Done():
								t.Fatal(ctx.Err())
							case <-time.After(10 * time.Millisecond):
							}
						}
						if kind == "context cancel" {
							cancel()
						} else {
							var sent bool
							if err := observer.QueryRow(ctx, "SELECT pg_cancel_backend($1)", pid).Scan(&sent); err != nil || !sent {
								t.Fatalf("cancel request sent = %v: %v", sent, err)
							}
						}
					}
					var queryErr error
					select {
					case queryErr = <-done:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					var qe *qerr.Error
					if !errors.As(queryErr, &qe) || qe.Category != want {
						t.Errorf("query error = %v; want %s", queryErr, want)
					} else if want == qerr.QueryError && (qe.Hint != "" || strings.Contains(qe.Message, "timeout")) {
						t.Errorf("cancellation contains timeout advice: %v", queryErr)
					}
					res, err := c.Query(ctx, "SELECT 1", engine.QueryOpts{MaxRows: 1, Timeout: 5 * time.Second})
					if err != nil {
						t.Fatalf("query after cancellation: %v", err)
					}
					if len(res.Rows) != 1 || res.Rows[0][0].Text != "1" {
						t.Fatalf("query after cancellation = %+v", res)
					}
				})
			}
		})
	}
}

// TestIntrospectKeysAndComments covers what Introspect resolves itself: key
// columns by attribute number (shifted by a dropped column, listed out of
// column order, or on a partition that only a foreign key reaches) and
// comments joined from pg_description. A foreign key to a partitioned table
// is listed once, not again for each partition.
func TestIntrospectKeysAndComments(t *testing.T) {
	dsn := startPostgres(t, "postgres:17-alpine")
	ctx := context.Background()
	pc, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pc.Exec(ctx, `
CREATE SCHEMA edge;
CREATE TABLE edge.p2 (gone int, x int, y int, PRIMARY KEY (y, x));
ALTER TABLE edge.p2 DROP COLUMN gone;
CREATE TABLE edge.child (id int PRIMARY KEY, py int, px int, twice int GENERATED ALWAYS AS (px * 2) STORED,
  made timestamptz DEFAULT now(), FOREIGN KEY (py, px) REFERENCES edge.p2 (y, x));
CREATE TABLE edge.hidden (id int);
CREATE ROLE reader LOGIN PASSWORD 'reader-pw';
GRANT USAGE ON SCHEMA edge TO reader;
GRANT SELECT ON edge.child TO reader;
COMMENT ON TABLE edge.child IS 'Child rows';
COMMENT ON COLUMN edge.child.px IS 'x of the parent';
CREATE TABLE edge.events (id int, region text, PRIMARY KEY (id, region)) PARTITION BY LIST (region);
CREATE TABLE edge.events_eu PARTITION OF edge.events FOR VALUES IN ('eu');
CREATE TABLE edge.eu_ref (id int PRIMARY KEY, rg text, ev int, FOREIGN KEY (ev, rg) REFERENCES edge.events_eu (id, region));
CREATE TABLE edge.any_ref (id int PRIMARY KEY, ev int, rg text, FOREIGN KEY (ev, rg) REFERENCES edge.events (id, region));`)
	pc.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ro, _ := specs(dsn)
	c, err := Engine{}.Open(ctx, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cat, err := c.Introspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	objs := map[string]*engine.Object{}
	for _, ns := range cat.Namespaces {
		for _, o := range ns.Objects {
			objs[o.Namespace+"."+o.Name] = o
		}
	}
	join := func(s []string) string { return strings.Join(s, ",") }
	fk := func(o *engine.Object) string {
		if o == nil || len(o.ForeignKeys) != 1 {
			return "no single foreign key"
		}
		f := o.ForeignKeys[0]
		return join(f.Columns) + "->" + f.RefNamespace + "." + f.RefObject + "(" + join(f.RefColumns) + ")"
	}
	if o := objs["edge.p2"]; o == nil || join(o.PrimaryKey) != "y,x" || len(o.Fields) != 2 || o.Fields[0].Name != "x" {
		t.Errorf("p2: %+v", o)
	}
	if got := fk(objs["edge.child"]); got != "py,px->edge.p2(y,x)" {
		t.Errorf("child: %s", got)
	}
	if o := objs["edge.child"]; o.Comment != "Child rows" || o.Fields[0].Comment != "" || o.Fields[2].Comment != "x of the parent" ||
		o.Fields[3].Default != "(px * 2)" || o.Fields[4].Default != "now()" {
		t.Errorf("child comments and defaults: %q %+v", o.Comment, o.Fields)
	}
	if objs["edge.hidden"] == nil {
		t.Error("the owner does not see edge.hidden")
	}
	// A user who owns nothing sees only what it was granted.
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("reader", "reader-pw")
	ro.DSN = u.String()
	rc, err := Engine{}.Open(ctx, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	rcat, err := rc.Introspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, ns := range rcat.Namespaces {
		for _, o := range ns.Objects {
			if o.Namespace == "edge" {
				seen = append(seen, o.Name)
			}
		}
	}
	if join(seen) != "child" {
		t.Errorf("reader sees %v", seen)
	}
	if o := objs["edge.events"]; o == nil || join(o.PrimaryKey) != "id,region" {
		t.Errorf("events: %+v", o)
	}
	if objs["edge.events_eu"] != nil {
		t.Error("a partition is listed")
	}
	if got := fk(objs["edge.eu_ref"]); got != "ev,rg->edge.events_eu(id,region)" {
		t.Errorf("eu_ref: %s", got)
	}
	if got := fk(objs["edge.any_ref"]); got != "ev,rg->edge.events(id,region)" {
		t.Errorf("any_ref: %s", got)
	}
	if o := objs["public.users"]; o == nil || o.Comment != "App users" || o.Fields[1].Comment != "Login email" {
		t.Errorf("users: %+v", o)
	}
}
