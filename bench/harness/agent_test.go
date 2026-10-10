package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServer answers every call with out (or errOut when isErr).
type fakeServer struct {
	out   string
	isErr bool
	calls int
}

func (f *fakeServer) Name() string { return "fake" }
func (f *fakeServer) Tools() []*mcp.Tool {
	return []*mcp.Tool{{Name: "query", Description: "run SQL", InputSchema: map[string]any{"type": "object"}}}
}
func (f *fakeServer) Call(context.Context, string, json.RawMessage) (string, bool) {
	f.calls++
	return f.out, f.isErr
}
func (f *fakeServer) Close() error { return nil }

// script replays replies and records what it was sent.
type script struct {
	replies []*Reply
	n       int
	tools   []ToolDef
	sent    [][]ToolResult
}

func (s *script) next() (*Reply, error) {
	if s.n >= len(s.replies) {
		return &Reply{Calls: []ToolCall{{ID: "x", Name: "query", Input: json.RawMessage(`{}`)}}, Usage: Usage{Input: 1, Output: 1}}, nil
	}
	s.n++
	return s.replies[s.n-1], nil
}
func (s *script) Start(_ context.Context, _, _ string, tools []ToolDef) (*Reply, error) {
	s.tools = tools
	return s.next()
}
func (s *script) Continue(_ context.Context, r []ToolResult) (*Reply, error) {
	s.sent = append(s.sent, r)
	return s.next()
}

func call(id, name, input string) ToolCall {
	return ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

var q1 = Question{ID: "chinook-01", Kind: "lookup", Question: "How many tracks? Return: count.", SQL: "SELECT count(*) FROM Track"}

func TestRunSubmitsCorrectAnswer(t *testing.T) {
	s := &script{replies: []*Reply{
		{Calls: []ToolCall{call("a", "query", `{"query":"SELECT count(*) FROM Track"}`)}, Usage: Usage{Input: 100, Output: 20}},
		{Calls: []ToolCall{call("b", "submit_answer", `{"rows":[[3503]]}`)}, Usage: Usage{Input: 150, Output: 10}},
	}}
	srv := &fakeServer{out: "n\n3503\n"}
	r := Run(context.Background(), s, srv, "chinook", q1, [][]any{{int64(3503)}}, 1, 10)
	if !r.Correct || !r.Submitted || r.ToolCalls != 1 || r.Turns != 2 || r.InputTokens != 250 || r.OutputTokens != 30 || r.Error != "" {
		t.Fatalf("%+v", r)
	}
	if r.Dataset != "chinook" || r.Server != "fake" || r.Question != "chinook-01" || r.Run != 1 || r.WallMS < 0 {
		t.Fatalf("labels: %+v", r)
	}
	if len(s.tools) != 2 || s.tools[len(s.tools)-1].Name != "submit_answer" {
		t.Fatalf("tools offered: %+v", s.tools)
	}
}

func TestRunWrongAnswer(t *testing.T) {
	s := &script{replies: []*Reply{{Calls: []ToolCall{call("b", "submit_answer", `{"rows":[[3502]]}`)}}}}
	if r := Run(context.Background(), s, &fakeServer{}, "chinook", q1, [][]any{{int64(3503)}}, 1, 10); r.Correct || !r.Submitted {
		t.Fatalf("%+v", r)
	}
}

func TestRunStopsWithoutSubmit(t *testing.T) {
	s := &script{replies: []*Reply{{}}}
	r := Run(context.Background(), s, &fakeServer{}, "chinook", q1, [][]any{{int64(1)}}, 1, 10)
	if r.Correct || r.Submitted || r.Error != "the model stopped without calling submit_answer" {
		t.Fatalf("%+v", r)
	}
}

func TestRunTurnLimit(t *testing.T) {
	s := &script{} // always calls query
	r := Run(context.Background(), s, &fakeServer{out: "x"}, "chinook", q1, [][]any{{int64(1)}}, 1, 3)
	if r.Correct || r.Turns != 3 || r.Error != "turn limit (3) reached without submit_answer" {
		t.Fatalf("%+v", r)
	}
}

func TestRunCapsToolOutput(t *testing.T) {
	s := &script{replies: []*Reply{
		{Calls: []ToolCall{call("a", "query", `{}`)}},
		{Calls: []ToolCall{call("b", "submit_answer", `{"rows":[[1]]}`)}},
	}}
	r := Run(context.Background(), s, &fakeServer{out: strings.Repeat("x", 250000)}, "chinook", q1, [][]any{{int64(1)}}, 1, 10)
	got := s.sent[0][0].Content
	if r.Truncations != 1 || len(got) != OutputCap+len(TruncationNote) || !strings.HasSuffix(got, TruncationNote) {
		t.Fatalf("truncations %d, len %d", r.Truncations, len(got))
	}
}

func TestRunPassesToolErrorsAndBadSubmits(t *testing.T) {
	s := &script{replies: []*Reply{
		{Calls: []ToolCall{call("a", "query", `{}`)}},
		{Calls: []ToolCall{call("b", "submit_answer", `{"rows":"3503"}`)}},
		{Calls: []ToolCall{call("c", "submit_answer", `{"rows":[[3503]]}`)}},
	}}
	r := Run(context.Background(), s, &fakeServer{out: "error[query_error]: no such column", isErr: true}, "chinook", q1, [][]any{{int64(3503)}}, 1, 10)
	if !r.Correct || !s.sent[0][0].IsError || !s.sent[1][0].IsError || !strings.Contains(s.sent[1][0].Content, "rows must be an array of arrays") {
		t.Fatalf("%+v / %+v", r, s.sent)
	}
}

func TestCapOutputKeepsRunesWhole(t *testing.T) {
	in := strings.Repeat("x", OutputCap-1) + "é" + "tail" // é straddles the cap
	got, cut := capOutput(in)
	if !cut || !utf8.ValidString(got) || len(got) > OutputCap+len(TruncationNote) || !strings.HasSuffix(got, TruncationNote) {
		t.Fatalf("cut=%v valid=%v len=%d", cut, utf8.ValidString(got), len(got))
	}
}

// slowModel sleeps in Start, then submits.
type slowModel struct{ script }

func (s *slowModel) Start(ctx context.Context, sys, user string, tools []ToolDef) (*Reply, error) {
	time.Sleep(20 * time.Millisecond)
	return s.script.Start(ctx, sys, user, tools)
}

func TestRunRecordsWallTime(t *testing.T) {
	m := &slowModel{script{replies: []*Reply{{Calls: []ToolCall{call("b", "submit_answer", `{"rows":[[1]]}`)}}}}}
	if r := Run(context.Background(), m, &fakeServer{}, "chinook", q1, [][]any{{int64(1)}}, 1, 10); r.WallMS < 20 {
		t.Fatalf("WallMS %d", r.WallMS)
	}
}

func TestRunRejectsSubmitWithoutRows(t *testing.T) {
	for _, in := range []string{`{}`, `{"rows":null}`} {
		s := &script{replies: []*Reply{
			{Calls: []ToolCall{call("a", "submit_answer", in)}},
			{Calls: []ToolCall{call("b", "submit_answer", `{"rows":[[1]]}`)}},
		}}
		r := Run(context.Background(), s, &fakeServer{}, "chinook", q1, [][]any{{int64(1)}}, 1, 10)
		if !s.sent[0][0].IsError || !strings.Contains(s.sent[0][0].Content, "rows must be an array of arrays") || !r.Correct || r.Turns != 2 {
			t.Fatalf("%s: %+v / %+v", in, r, s.sent)
		}
	}
	// a lone bad submit must not count as submitted
	s := &script{replies: []*Reply{{Calls: []ToolCall{call("a", "submit_answer", `{}`)}}, {}}}
	if r := Run(context.Background(), s, &fakeServer{}, "chinook", q1, [][]any{{int64(1)}}, 1, 10); r.Submitted || r.Correct {
		t.Fatalf("%+v", r)
	}
}

type errModel struct{}

func (errModel) Start(context.Context, string, string, []ToolDef) (*Reply, error) {
	return nil, errors.New("529 overloaded")
}
func (errModel) Continue(context.Context, []ToolResult) (*Reply, error) {
	return nil, errors.New("unused")
}

func TestRunModelErrorIsAborted(t *testing.T) {
	r := Run(context.Background(), errModel{}, &fakeServer{}, "chinook", q1, nil, 1, 5)
	if !r.Aborted || !strings.Contains(r.Error, "529") {
		t.Fatalf("%+v", r)
	}
	// Model behaviour failures are not aborts.
	s := &script{replies: []*Reply{{Calls: nil, Usage: Usage{Input: 1, Output: 1}}}}
	if r := Run(context.Background(), s, &fakeServer{}, "chinook", q1, nil, 1, 5); r.Aborted || r.Error == "" {
		t.Fatalf("%+v", r)
	}
}
