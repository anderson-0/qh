package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
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
		CountTokens: func(context.Context, []ToolDef) (int64, error) { return 123, nil },
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
