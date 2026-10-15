package harness

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// answerModel submits the right answer for chinook-01 on the first turn.
type answerModel struct{}

func (answerModel) Start(context.Context, string, string, []ToolDef) (*Reply, error) {
	return &Reply{Calls: []ToolCall{{ID: "s", Name: "submit_answer", Input: json.RawMessage(`{"rows":[[3503]]}`)}}, Usage: Usage{Input: 10, Output: 5}}, nil
}
func (answerModel) Continue(context.Context, []ToolResult) (*Reply, error) {
	return nil, errors.New("unused")
}

func fakeDeps(failDBHub bool) Deps {
	return Deps{
		Models: func() Model { return answerModel{} },
		StartDataset: func(ctx context.Context, name string) (*Dataset, func(), error) {
			return OpenChinook(ctx)
		},
		StartServer: func(_ context.Context, server string, _ []Conn) (Server, error) {
			if server == "dbhub" && failDBHub {
				return nil, errors.New("start dbhub: npx is not installed")
			}
			return &namedServer{fakeServer: fakeServer{out: "ok"}, name: server}, nil
		},
		CountTokens: func(context.Context, []ToolDef, string) (int64, error) { return 123, nil },
	}
}

type namedServer struct {
	fakeServer
	name string
}

func TestMicroUsesSelectedEngine(t *testing.T) {
	for _, selection := range []string{"postgres", "mysql", "mongodb", "default-sql", "default-mongo"} {
		engine := selection
		if selection == "default-sql" {
			engine = "postgres"
		}
		if selection == "default-mongo" {
			engine = "mongodb"
		}
		t.Run(selection, func(t *testing.T) {
			cfg := Config{MicroEngine: engine, Servers: []string{"qh"}, SkipAgent: true, SkipTokens: true}
			if strings.HasPrefix(selection, "default-") {
				cfg.MicroEngine = ""
			}
			latency := "pagila"
			if engine == "mysql" {
				latency = "sakila"
			}
			if engine == "mongodb" {
				cfg.Rival = "mongodb-mcp"
				latency = "pagila-docs"
			}
			deps := Deps{
				StartGenerated: func(_ context.Context, got string, n int) (*Dataset, func(), error) {
					if got != engine || n != 1000 {
						t.Fatalf("generator: %s %d", got, n)
					}
					return &Dataset{Name: "gen", Engine: engine}, func() {}, nil
				},
				StartDataset: func(_ context.Context, name string) (*Dataset, func(), error) {
					if name != latency {
						t.Fatalf("latency dataset %q want %q", name, latency)
					}
					return &Dataset{Name: name, Engine: engine}, func() {}, nil
				},
				StartServer: func(_ context.Context, _ string, conns []Conn) (Server, error) {
					if len(conns) != 1 || conns[0].Engine != engine {
						t.Fatalf("connections: %+v", conns)
					}
					return &microServer{fakeServer{out: "ok"}}, nil
				},
			}
			rep, err := RunBenchmark(context.Background(), cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			_, cold := rep.ColdSchema["qh"]
			_, latencyMeasured := rep.LatencyP50["qh"]
			if rep.MicroEngine != engine || !cold || !latencyMeasured {
				t.Fatalf("report %+v", rep)
			}
		})
	}
}

func TestMicroRejectsIncompatibleEngine(t *testing.T) {
	for _, cfg := range []Config{{MicroEngine: "sqlite"}, {MicroEngine: "mongodb", Rival: "toolbox"}, {MicroEngine: "mysql", Rival: "mongodb-mcp"}} {
		if _, err := RunBenchmark(context.Background(), cfg, Deps{}); err == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
}

func (n *namedServer) Name() string { return n.name }

func TestRunnerServerStartFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads Chinook")
	}
	var raw bytes.Buffer
	cfg := Config{Model: "fake", Datasets: []string{"chinook"}, Servers: []string{"qh", "dbhub"}, Runs: 2, Limit: 1,
		Parallel: 2, MaxTurns: 5, SkipMicro: true, Raw: &raw}
	rep, err := RunBenchmark(context.Background(), cfg, fakeDeps(true))
	if err != nil {
		t.Fatal(err)
	}
	var ok, failed int
	for _, r := range rep.Results {
		switch {
		case r.Server == "qh" && r.Correct:
			ok++
		case r.Server == "dbhub" && strings.Contains(r.Error, "npx is not installed"):
			failed++
		}
	}
	if ok != 2 || failed != 2 {
		t.Fatalf("ok %d failed %d: %+v", ok, failed, rep.Results)
	}
	if rep.ToolTokens["qh"][4] != 123 || len(rep.ToolTokens["qh"]) != 5 {
		t.Fatalf("tool tokens: %v", rep.ToolTokens)
	}
}

func TestRunnerWritesRawIncrementally(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads Chinook")
	}
	var raw bytes.Buffer
	cfg := Config{Model: "fake", Datasets: []string{"chinook"}, Servers: []string{"qh"}, Runs: 3, Limit: 2,
		Parallel: 3, MaxTurns: 5, SkipMicro: true, SkipTokens: true, Raw: &raw}
	rep, err := RunBenchmark(context.Background(), cfg, fakeDeps(false))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(raw.String()), "\n")
	if len(lines) != 6 || len(rep.Results) != 6 {
		t.Fatalf("%d raw lines, %d results", len(lines), len(rep.Results))
	}
	for _, l := range lines {
		var r Result
		if err := json.Unmarshal([]byte(l), &r); err != nil || r.Server != "qh" {
			t.Fatalf("raw line %q: %v", l, err)
		}
	}
}

// blockModel never answers: it waits for the context to end.
type blockModel struct{}

func (blockModel) Start(ctx context.Context, _, _ string, _ []ToolDef) (*Reply, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (blockModel) Continue(context.Context, []ToolResult) (*Reply, error) {
	return nil, errors.New("unused")
}

// cancelOnWrite cancels the run when the first raw line is written.
type cancelOnWrite struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (c *cancelOnWrite) Write(p []byte) (int, error) {
	n, err := c.Buffer.Write(p)
	c.cancel()
	return n, err
}

func TestRunnerStopsCleanlyOnInterrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads Chinook")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := &cancelOnWrite{cancel: cancel}
	deps := fakeDeps(false)
	var started atomic.Int32
	deps.Models = func() Model {
		if started.Add(1) == 1 {
			return answerModel{}
		}
		return blockModel{}
	}
	cfg := Config{Model: "fake", Datasets: []string{"chinook", "pagila"}, Servers: []string{"qh", "dbhub"}, Runs: 3, Limit: 3,
		Parallel: 2, MaxTurns: 5, Raw: raw}
	done := make(chan struct{})
	var rep *Report
	var err error
	go func() { rep, err = RunBenchmark(ctx, cfg, deps); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunBenchmark did not return after the interrupt")
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 || strings.Count(raw.String(), "\n") != 1 {
		t.Fatalf("%d results, raw %q", len(rep.Results), raw.String())
	}
	if len(rep.ToolTokens) != 0 || len(rep.ColdSchema) != 0 {
		t.Fatalf("later phases ran: %+v %+v", rep.ToolTokens, rep.ColdSchema)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "interrupted; results are partial") {
		t.Fatalf("notes: %v", rep.Notes)
	}
}

func TestRunnerReportsAbortedRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads Chinook")
	}
	cfg := Config{Model: "fake", Datasets: []string{"chinook"}, Servers: []string{"qh", "dbhub"}, Runs: 2, Limit: 1,
		Parallel: 1, MaxTurns: 5, SkipMicro: true, SkipTokens: true}
	rep, err := RunBenchmark(context.Background(), cfg, fakeDeps(true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "0 qh and 2 dbhub runs were aborted") {
		t.Fatalf("notes: %v", rep.Notes)
	}
	for _, c := range Criteria(rep) {
		if c.Known {
			t.Errorf("criterion known although dbhub never ran: %+v", c)
		}
	}
}

func TestRunWithTimeoutIsAFailedRunNotAnAbort(t *testing.T) {
	cfg := Config{MaxTurns: 5, RunTimeout: 50 * time.Millisecond}
	r := runWithTimeout(context.Background(), cfg, blockModel{}, &fakeServer{}, "chinook", q1, nil, 1)
	if r.Aborted || r.Submitted || r.Error != "run timed out after 50ms" {
		t.Fatalf("%+v", r)
	}
	// an interrupt of the whole benchmark is not a timeout
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg.RunTimeout = time.Hour
	if r := runWithTimeout(ctx, cfg, blockModel{}, &fakeServer{}, "chinook", q1, nil, 1); !r.Aborted || strings.Contains(r.Error, "timed out") {
		t.Fatalf("%+v", r)
	}
	if got := runTimeoutText(10 * time.Minute); got != "10m" {
		t.Fatalf("%q", got)
	}
	if DefaultRunTimeout != 10*time.Minute {
		t.Fatal(DefaultRunTimeout)
	}
}

func TestRunnerPreflightFailureSkipsTheAgentPhase(t *testing.T) {
	deps := fakeDeps(false)
	var touched atomic.Int32
	deps.Preflight = func(context.Context) error { return errors.New("401 invalid x-api-key") }
	deps.Models = func() Model { touched.Add(1); return answerModel{} }
	deps.StartDataset = func(context.Context, string) (*Dataset, func(), error) {
		touched.Add(1)
		return nil, nil, errors.New("unused")
	}
	cfg := Config{Model: "fake", Datasets: []string{"chinook"}, Servers: []string{"qh", "dbhub"}, Runs: 3, Limit: 0, Parallel: 2, MaxTurns: 5}
	rep, err := RunBenchmark(context.Background(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(rep.Notes, "\n")
	if len(rep.Results) != 0 || touched.Load() != 0 || !strings.Contains(notes, "preflight failed") || !strings.Contains(notes, "401 invalid x-api-key") {
		t.Fatalf("%d results, touched %d, notes %v", len(rep.Results), touched.Load(), rep.Notes)
	}
}

func TestRunnerPreflightIsOptionalAndSkippedWithoutTheAgentPhase(t *testing.T) {
	deps := fakeDeps(false)
	deps.Preflight = func(context.Context) error { t.Fatal("preflight ran"); return nil }
	cfg := Config{Model: "fake", Servers: []string{"qh"}, SkipAgent: true, SkipTokens: true, SkipMicro: true}
	if _, err := RunBenchmark(context.Background(), cfg, deps); err != nil {
		t.Fatal(err)
	}
}

// errAfterDeadline returns err once its context is done.
type errAfterDeadline struct{ err error }

func (e errAfterDeadline) Start(ctx context.Context, _, _ string, _ []ToolDef) (*Reply, error) {
	<-ctx.Done()
	return nil, e.err
}
func (e errAfterDeadline) Continue(context.Context, []ToolResult) (*Reply, error) { return nil, e.err }

func TestRunWithTimeoutKeepsATransientErrorAborted(t *testing.T) {
	cfg := Config{MaxTurns: 5, RunTimeout: 30 * time.Millisecond}
	r := runWithTimeout(context.Background(), cfg, errAfterDeadline{apiErr(429)}, &fakeServer{}, "chinook", q1, nil, 1)
	if !r.Aborted || strings.Contains(r.Error, "timed out") || !strings.Contains(r.Error, "429") {
		t.Fatalf("%+v", r)
	}
}

func TestRunnerNotesBudgetAndTimeoutFailures(t *testing.T) {
	rep := &Report{Results: []Result{
		{Server: "qh", Error: "token budget exceeded (1200 tokens)"},
		{Server: "dbhub", Error: "token budget exceeded (1300 tokens)"},
		{Server: "dbhub", Error: "token budget exceeded (1100 tokens)"},
		{Server: "dbhub", Error: "run timed out after 10m"},
		{Server: "qh", Error: "token budget exceeded (1)", Aborted: true},
	}}
	notes := failureNotes(rep.Results, "dbhub")
	got := strings.Join(notes, "\n")
	if !strings.Contains(got, "1 qh and 2 dbhub runs failed with \"token budget exceeded\"") ||
		!strings.Contains(got, "0 qh and 1 dbhub runs failed with \"run timed out\"") {
		t.Fatalf("%q", got)
	}
	if n := failureNotes([]Result{{Server: "qh"}}, "dbhub"); len(n) != 0 {
		t.Fatalf("%v", n)
	}
}

func TestRivalDefaultsToDBHub(t *testing.T) {
	if (Config{}).rival() != "dbhub" || (Config{Rival: "mongodb-mcp"}).rival() != "mongodb-mcp" {
		t.Fatal("Config.rival")
	}
	if (&Report{}).rival() != "dbhub" || (&Report{Rival: "mongodb-mcp"}).rival() != "mongodb-mcp" {
		t.Fatal("Report.rival")
	}
	got := strings.Join(failureNotes([]Result{{Server: "mongodb-mcp", Error: "run timed out after 10m"}, {Server: "dbhub", Error: "run timed out after 10m"}}, "mongodb-mcp"), "\n")
	if !strings.Contains(got, "0 qh and 1 mongodb-mcp runs failed") {
		t.Fatalf("%q", got)
	}
}

// rivalDeps are fake Deps that need no Docker: datasets are an in-memory
// SQLite with just enough of Pagila for the first question's reference query.
type rivalDeps struct {
	Deps
	mu       sync.Mutex
	datasets []string
	started  map[string][]int // server → number of connections of each start
	names    map[string][]string
}

func newRivalDeps(failRival string) *rivalDeps {
	d := &rivalDeps{started: map[string][]int{}, names: map[string][]string{}}
	d.Deps = Deps{
		Models: func() Model { return answerModel{} },
		StartDataset: func(_ context.Context, name string) (*Dataset, func(), error) {
			d.mu.Lock()
			d.datasets = append(d.datasets, name)
			d.mu.Unlock()
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				return nil, nil, err
			}
			db.SetMaxOpenConns(1)
			if _, err := db.Exec("CREATE TABLE film (film_id integer); INSERT INTO film VALUES (1)"); err != nil {
				return nil, nil, err
			}
			return &Dataset{Name: name, Engine: "mongodb", DSN: "mongodb://x/" + name, DB: db}, func() { db.Close() }, nil
		},
		StartServer: func(_ context.Context, server string, conns []Conn) (Server, error) {
			d.mu.Lock()
			d.started[server] = append(d.started[server], len(conns))
			for _, c := range conns {
				d.names[server] = append(d.names[server], c.Name)
			}
			d.mu.Unlock()
			if server == failRival {
				return nil, errors.New("start " + server + ": boom")
			}
			return &namedServer{fakeServer: fakeServer{out: "ok"}, name: server}, nil
		},
		CountTokens: func(context.Context, []ToolDef, string) (int64, error) { return 123, nil },
	}
	return d
}

func TestToolTokensForMongoRival(t *testing.T) {
	d := newRivalDeps("")
	cfg := Config{Model: "fake", Rival: "mongodb-mcp", RivalVersion: "3.0.5", Datasets: []string{"pagila-docs"},
		Servers: []string{"qh", "mongodb-mcp"}, SkipAgent: true, SkipMicro: true}
	rep, err := RunBenchmark(context.Background(), cfg, d.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.ToolTokens["mongodb-mcp"]; len(got) != 1 {
		t.Errorf("mongodb-mcp measured %d times, want 1", len(got))
	}
	if got := rep.ToolTokens["qh"]; len(got) != 5 {
		t.Errorf("qh measured %d times, want 5", len(got))
	}
	if !reflect.DeepEqual(d.datasets, []string{"pagila-docs"}) {
		t.Errorf("datasets requested: %v", d.datasets)
	}
	if !reflect.DeepEqual(d.started["mongodb-mcp"], []int{1}) || !reflect.DeepEqual(d.started["qh"], []int{1, 2, 3, 4, 5}) {
		t.Errorf("starts: %v", d.started)
	}
	if rep.Rival != "mongodb-mcp" || rep.RivalVersion != "3.0.5" {
		t.Errorf("report rival: %q %q", rep.Rival, rep.RivalVersion)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "mongodb-mcp-server serves one connection per process; its tool tokens are measured with one") {
		t.Errorf("notes: %v", rep.Notes)
	}
}

func TestToolTokensForDBHubStayOnChinook(t *testing.T) {
	d := newRivalDeps("")
	cfg := Config{Model: "fake", Datasets: []string{"chinook"}, Servers: []string{"qh", "dbhub"}, SkipAgent: true, SkipMicro: true}
	rep, err := RunBenchmark(context.Background(), cfg, d.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.datasets, []string{"chinook"}) || len(rep.ToolTokens["dbhub"]) != 5 || len(rep.ToolTokens["qh"]) != 5 {
		t.Errorf("datasets %v, tokens %v", d.datasets, rep.ToolTokens)
	}
	if strings.Contains(strings.Join(rep.Notes, "\n"), "one connection per process") {
		t.Errorf("notes: %v", rep.Notes)
	}
	if rep.Rival != "dbhub" {
		t.Errorf("rival %q", rep.Rival)
	}
}

func TestAbortedNoteNamesTheMongoRival(t *testing.T) {
	d := newRivalDeps("mongodb-mcp")
	cfg := Config{Model: "fake", Rival: "mongodb-mcp", Datasets: []string{"pagila-docs"}, Servers: []string{"qh", "mongodb-mcp"},
		Runs: 2, Limit: 1, Parallel: 1, MaxTurns: 5, SkipMicro: true, SkipTokens: true}
	rep, err := RunBenchmark(context.Background(), cfg, d.Deps)
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(rep.Notes, "\n")
	if !strings.Contains(notes, "0 qh and 2 mongodb-mcp runs were aborted") || strings.Contains(notes, "dbhub") {
		t.Fatalf("notes: %v", rep.Notes)
	}
	for _, c := range Criteria(rep) {
		if c.Known && (c.Name == "tokens" || c.Name == "accuracy" || c.Name == "tool calls") {
			t.Errorf("criterion known although mongodb-mcp never ran: %+v", c)
		}
	}
	if !strings.Contains(rep.Markdown(), "# qh vs mongodb-mcp benchmark") {
		t.Error("report title does not name the rival")
	}
}

func TestDefaultDepsDispatch(t *testing.T) {
	deps := DefaultDeps(Config{Model: "fake"}, "/nonexistent/qh")
	if _, err := deps.StartServer(context.Background(), "nope", nil); err == nil || !strings.Contains(err.Error(), `unknown server "nope"`) {
		t.Fatalf("unknown server: %v", err)
	}
	// the rival servers are reached through their own starters, which refuse
	// these connection lists before starting anything.
	if _, err := deps.StartServer(context.Background(), "mongodb-mcp", nil); err == nil || !strings.Contains(err.Error(), "mongodb-mcp-server") {
		t.Fatalf("mongodb-mcp dispatch: %v", err)
	}
}

type microServer struct{ fakeServer }

func (*microServer) Tools() []*mcp.Tool { return []*mcp.Tool{{Name: "schema"}, {Name: "query"}} }
