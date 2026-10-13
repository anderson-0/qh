package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongodb "github.com/testcontainers/testcontainers-go/modules/mongodb"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
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
// schema overview; dbhub's search_objects for tables at summary detail;
// mongodb-mcp-server's list-collections. mongodb-mcp-server 3.0.5 requires a
// connectionId on every call; "preconfigured" is the connection string given
// to it in its environment.
// dbhub caps search_objects at 100 results unless limit is given (1 to 1000),
// so limit 1000 makes it list every table. qh's overview caps itself at 200
// tables with a pattern hint while dbhub lists all of them; the difference is
// intentional and the report notes it.
func SchemaCall(server string, ds *Dataset) (string, json.RawMessage) {
	switch server {
	case "qh":
		return "schema", json.RawMessage(fmt.Sprintf(`{"source":%q}`, ds.Name))
	case "mongodb-mcp":
		return "list-collections", json.RawMessage(fmt.Sprintf(`{"connectionId":"preconfigured","database":%q}`, mongoDB(ds)))
	}
	return "search_objects", json.RawMessage(`{"object_type":"table","detail_level":"summary","limit":1000}`)
}

// QueryCall is the query tool and argument shape for a server. On a MongoDB
// dataset q is a question's twin ({"collection", "pipeline"}): qh gets it as
// db.<collection>.aggregate(<pipeline>), mongodb-mcp-server through its
// aggregate tool.
func QueryCall(server string, ds *Dataset) (string, func(q string) json.RawMessage) {
	switch {
	case server == "qh" && ds.Engine == "mongodb":
		return "query", func(q string) json.RawMessage {
			var t struct {
				Collection string          `json:"collection"`
				Pipeline   json.RawMessage `json:"pipeline"`
			}
			_ = json.Unmarshal([]byte(q), &t)
			b, _ := json.Marshal(map[string]string{"source": ds.Name, "query": fmt.Sprintf("db[%q].aggregate(%s)", t.Collection, t.Pipeline)})
			return b
		}
	case server == "qh":
		return "query", func(q string) json.RawMessage {
			b, _ := json.Marshal(map[string]string{"source": ds.Name, "query": q})
			return b
		}
	case server == "mongodb-mcp":
		return "aggregate", func(q string) json.RawMessage {
			var t struct {
				Collection string          `json:"collection"`
				Pipeline   json.RawMessage `json:"pipeline"`
			}
			_ = json.Unmarshal([]byte(q), &t)
			b, _ := json.Marshal(map[string]any{"connectionId": "preconfigured", "database": mongoDB(ds), "collection": t.Collection, "pipeline": t.Pipeline})
			return b
		}
	}
	return "execute_sql", func(q string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"sql": q})
		return b
	}
}

// mongoDB is the database named in a MongoDB dataset's connection string.
func mongoDB(ds *Dataset) string {
	u, err := url.Parse(ds.DSN)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

// StartGeneratedMongo creates n collections t0001… of 100 documents each,
// with a mix of scalar, nested and array fields, in database "gen".
func StartGeneratedMongo(ctx context.Context, n int) (*Dataset, func(), error) {
	ctr, err := tcmongodb.Run(ctx, mongoImage)
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return nil, nil, err
	}
	base, err := ctr.ConnectionString(ctx)
	if err != nil {
		stop()
		return nil, nil, err
	}
	u, _ := url.Parse(base)
	u.Path = "/gen"
	client, err := mongo.Connect(options.Client().ApplyURI(u.String()))
	if err != nil {
		stop()
		return nil, nil, err
	}
	defer client.Disconnect(context.Background())
	sem := make(chan struct{}, 8)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			docs := make([]any, 100)
			for k := range docs {
				docs[k] = bson.D{{Key: "_id", Value: int32(k + 1)}, {Key: "name", Value: fmt.Sprintf("item %d", k)},
					{Key: "status", Value: []string{"new", "open", "closed"}[k%3]}, {Key: "amount", Value: float64(k) * 1.25},
					{Key: "created_at", Value: bson.NewDateTimeFromTime(time.Unix(int64(1700000000+k), 0))},
					{Key: "owner", Value: bson.D{{Key: "name", Value: "o"}, {Key: "email", Value: "o@example.com"}}},
					{Key: "tags", Value: bson.A{"a", "b"}}}
			}
			if _, err := client.Database("gen").Collection(fmt.Sprintf("t%04d", i)).InsertMany(ctx, docs); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		stop()
		return nil, nil, err
	}
	return &Dataset{Name: "gen", Engine: "mongodb", DSN: u.String()}, stop, nil
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
