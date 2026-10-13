package harness

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	var ds []time.Duration
	for i := 1; i <= 100; i++ {
		ds = append(ds, time.Duration(i)*time.Millisecond)
	}
	if Percentile(ds, 0.50) != 50*time.Millisecond || Percentile(ds, 0.95) != 95*time.Millisecond || Percentile(nil, 0.5) != 0 {
		t.Fatalf("%v %v", Percentile(ds, 0.50), Percentile(ds, 0.95))
	}
}

func TestMicroQH(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	ctx := context.Background()
	gen, stop, err := StartGenerated(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	bin, cleanup, err := BuildQH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	start := func(ctx context.Context) (Server, error) { return StartQH(ctx, bin, []Conn{ConnFor(gen)}) }
	prefix, args := SchemaCall("qh", gen)
	d, err := ColdSchema(ctx, start, prefix, args)
	if err != nil || d <= 0 {
		t.Fatalf("cold schema: %v %v", d, err)
	}
	t.Logf("qh cold schema, 50 tables: %v", d)
	srv, err := start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if out, _ := srv.Call(ctx, "schema", args); !strings.Contains(out, "t_0049") {
		t.Fatalf("generated tables missing: %q", out[:min(len(out), 300)])
	}
	qp, argsFor := QueryCall("qh", gen)
	p50, p95, err := QueryLatency(ctx, srv, FindTool(srv.Tools(), qp), argsFor, []string{"SELECT count(*) FROM t_0001"}, 1, 5)
	if err != nil || p50 <= 0 || p95 < p50 {
		t.Fatalf("latency: %v %v %v", p50, p95, err)
	}
	t.Logf("qh query latency: p50 %v p95 %v", p50, p95)
	var probe map[string]any
	if json.Unmarshal(argsFor("SELECT 1"), &probe) != nil || probe["query"] != "SELECT 1" {
		t.Fatal("qh query args")
	}
}

func TestMicroDBHub(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not installed")
	}
	ctx := context.Background()
	gen, stop, err := StartGenerated(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	start := func(ctx context.Context) (Server, error) {
		return StartDBHub(ctx, DefaultDBHubVersion, []Conn{ConnFor(gen)})
	}
	prefix, args := SchemaCall("dbhub", gen)
	d, err := ColdSchema(ctx, start, prefix, args)
	if err != nil || d <= 0 {
		t.Fatalf("cold schema: %v %v", d, err)
	}
	t.Logf("dbhub cold schema, 50 tables: %v", d)
	srv, err := start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	out, isErr := srv.Call(ctx, FindTool(srv.Tools(), prefix), args)
	if isErr || !strings.Contains(out, "t_0049") {
		t.Fatalf("generated tables missing: %v %q", isErr, out[:min(len(out), 300)])
	}
	qp, argsFor := QueryCall("dbhub", gen)
	p50, p95, err := QueryLatency(ctx, srv, FindTool(srv.Tools(), qp), argsFor, []string{"SELECT count(*) FROM t_0001"}, 1, 5)
	if err != nil || p50 <= 0 || p95 < p50 {
		t.Fatalf("latency: %v %v %v", p50, p95, err)
	}
	t.Logf("dbhub query latency: p50 %v p95 %v", p50, p95)
}

func TestMongoCalls(t *testing.T) {
	ds := &Dataset{Name: "pagila-docs", Engine: "mongodb", DSN: "mongodb://127.0.0.1:1/pagila?directConnection=true"}
	twin := `{"collection":"films","pipeline":[{"$match":{"rating":"PG"}},{"$count":"n"}]}`

	name, args := SchemaCall("mongodb-mcp", ds)
	if name != "list-collections" || string(args) != `{"connectionId":"preconfigured","database":"pagila"}` {
		t.Errorf("schema call: %s %s", name, args)
	}
	if name, _ := SchemaCall("qh", ds); name != "schema" {
		t.Errorf("qh schema tool %q", name)
	}

	name, argsFor := QueryCall("mongodb-mcp", ds)
	var got struct {
		ConnectionID, Database, Collection string
		Pipeline                           []map[string]any
	}
	if err := json.Unmarshal(argsFor(twin), &got); err != nil || name != "aggregate" ||
		got.ConnectionID != "preconfigured" || got.Database != "pagila" || got.Collection != "films" || len(got.Pipeline) != 2 {
		t.Errorf("mongodb-mcp query: %s %s %v", name, argsFor(twin), err)
	}

	name, argsFor = QueryCall("qh", ds)
	var q map[string]string
	if err := json.Unmarshal(argsFor(twin), &q); err != nil || name != "query" || q["source"] != "pagila-docs" ||
		q["query"] != `db["films"].aggregate([{"$match":{"rating":"PG"}},{"$count":"n"}])` {
		t.Errorf("qh mongo query: %s %v", argsFor(twin), err)
	}
}
