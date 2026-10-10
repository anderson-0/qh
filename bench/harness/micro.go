package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// StartGenerated starts Postgres 17 with n generated tables t_0000…, each
// with 7 columns, a foreign key to the previous table and a comment.
func StartGenerated(ctx context.Context, n int) (*Dataset, func(), error) {
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("gen"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("bench-pw"),
		tcpostgres.BasicWaitStrategies())
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return nil, nil, err
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return nil, nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		stop()
		return nil, nil, err
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		fk := ""
		if i > 0 {
			fk = fmt.Sprintf(" REFERENCES t_%04d(id)", i-1)
		}
		fmt.Fprintf(&b, "CREATE TABLE t_%04d (id bigint PRIMARY KEY, name text NOT NULL, status text, amount numeric(12,2), "+
			"created_at timestamptz DEFAULT now(), parent_id bigint%s, note text);\n", i, fk)
		fmt.Fprintf(&b, "COMMENT ON TABLE t_%04d IS 'generated table %d';\n", i, i)
	}
	if _, err := db.ExecContext(ctx, b.String()); err != nil {
		db.Close()
		stop()
		return nil, nil, fmt.Errorf("generate tables: %w", err)
	}
	if _, err := db.ExecContext(ctx, "ANALYZE"); err != nil {
		db.Close()
		stop()
		return nil, nil, err
	}
	return &Dataset{Name: "gen", Engine: "postgres", DSN: dsn, DB: db,
			Description: fmt.Sprintf("Generated schema with %d tables", n)},
		func() { db.Close(); stop() }, nil
}

// SchemaCall is the schema-discovery call measured for a server: qh's
// schema overview, dbhub's search_objects for tables at summary detail.
// dbhub caps search_objects at 100 results unless limit is given (1 to 1000),
// so limit 1000 makes it list every table. qh's overview caps itself at 200
// tables with a pattern hint while dbhub lists all of them; the difference is
// intentional and the report notes it.
func SchemaCall(server, source string) (string, json.RawMessage) {
	if server == "qh" {
		return "schema", json.RawMessage(fmt.Sprintf(`{"source":%q}`, source))
	}
	return "search_objects", json.RawMessage(`{"object_type":"table","detail_level":"summary","limit":1000}`)
}

// QueryCall is the query tool and argument shape for a server.
func QueryCall(server, source string) (string, func(string) json.RawMessage) {
	if server == "qh" {
		return "query", func(q string) json.RawMessage {
			b, _ := json.Marshal(map[string]string{"source": source, "query": q})
			return b
		}
	}
	return "execute_sql", func(q string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"sql": q})
		return b
	}
}

// ColdSchema starts a fresh server and times its first schema call.
func ColdSchema(ctx context.Context, start func(context.Context) (Server, error), toolPrefix string, args json.RawMessage) (time.Duration, error) {
	srv, err := start(ctx)
	if err != nil {
		return 0, err
	}
	defer srv.Close()
	tool := FindTool(srv.Tools(), toolPrefix)
	if tool == "" {
		return 0, fmt.Errorf("%s has no %s* tool", srv.Name(), toolPrefix)
	}
	t0 := time.Now()
	out, isErr := srv.Call(ctx, tool, args)
	d := time.Since(t0)
	if isErr {
		return 0, fmt.Errorf("%s %s: %s", srv.Name(), tool, out)
	}
	return d, nil
}

// QueryLatency runs each query warm times untimed, then n times timed, and
// returns the p50 and p95 of all timed calls.
func QueryLatency(ctx context.Context, srv Server, tool string, argsFor func(string) json.RawMessage, queries []string, warm, n int) (time.Duration, time.Duration, error) {
	if tool == "" {
		return 0, 0, fmt.Errorf("%s has no query tool", srv.Name())
	}
	var ds []time.Duration
	for _, q := range queries {
		for i := 0; i < warm+n; i++ {
			t0 := time.Now()
			out, isErr := srv.Call(ctx, tool, argsFor(q))
			d := time.Since(t0)
			if isErr {
				return 0, 0, fmt.Errorf("%s: %s", srv.Name(), out)
			}
			if i >= warm {
				ds = append(ds, d)
			}
		}
	}
	return Percentile(ds, 0.50), Percentile(ds, 0.95), nil
}

// Percentile returns the nearest-rank percentile p (0..1) of ds.
func Percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}
