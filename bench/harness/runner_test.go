package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
