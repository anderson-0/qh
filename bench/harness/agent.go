package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultModel is the Claude model the benchmark drives unless -model says otherwise.
const DefaultModel = "claude-sonnet-5-5"

// ToolDef is a tool offered to the model.
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // JSON schema of the input
}

// ToolCall is a tool use requested by the model.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers one ToolCall.
type ToolResult struct {
	ID      string
	Content string
	IsError bool
}

// Usage is the token usage of one model reply (input includes cache reads and writes).
type Usage struct{ Input, Output int64 }

// Reply is one model turn.
type Reply struct {
	Calls []ToolCall
	Usage Usage
}

// Model is one conversation with a model. Start opens it; Continue sends
// the results of the previous reply's tool calls.
type Model interface {
	Start(ctx context.Context, system, user string, tools []ToolDef) (*Reply, error)
	Continue(ctx context.Context, results []ToolResult) (*Reply, error)
}

// ModelFactory returns a fresh conversation.
type ModelFactory func() Model

const (
	// OutputCap is the most tool output, in bytes, passed to the model.
	OutputCap = 100_000
	// TruncationNote ends a capped tool output.
	TruncationNote = "\n[truncated by the benchmark harness]"
)

// SubmitTool is how the model returns its answer.
var SubmitTool = ToolDef{
	Name:        "submit_answer",
	Description: "Submit the final answer. Call it exactly once, when you are done.",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"rows": map[string]any{
				"type":        "array",
				"description": "One array per result row, with the columns in the order the question asks for.",
				"items":       map[string]any{"type": "array", "items": map[string]any{}},
			},
		},
		"required": []any{"rows"},
	},
}

// ToolDefs converts MCP tools into tool definitions for the model.
func ToolDefs(tools []*mcp.Tool) []ToolDef {
	out := make([]ToolDef, 0, len(tools))
	for _, t := range tools {
		schema := map[string]any{"type": "object"}
		if b, err := json.Marshal(t.InputSchema); err == nil {
			_ = json.Unmarshal(b, &schema)
		}
		out = append(out, ToolDef{Name: t.Name, Description: t.Description, Schema: schema})
	}
	return out
}

// Result is one run of one question against one server.
type Result struct {
	Dataset      string  `json:"dataset"`
	Server       string  `json:"server"`
	Question     string  `json:"question"`
	Kind         string  `json:"kind"`
	Run          int     `json:"run"`
	Correct      bool    `json:"correct"`
	Submitted    bool    `json:"submitted"`
	Answer       [][]any `json:"answer,omitempty"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	ToolCalls    int     `json:"tool_calls"`
	Turns        int     `json:"turns"`
	Truncations  int     `json:"truncations"`
	WallMS       int64   `json:"wall_ms"`
	Error        string  `json:"error,omitempty"`
}

// capOutput cuts out to at most OutputCap bytes without splitting a UTF-8
// character, and appends TruncationNote when it cut.
func capOutput(out string) (string, bool) {
	if len(out) <= OutputCap {
		return out, false
	}
	cut := OutputCap
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut] + TruncationNote, true
}

// Run asks one question. The model may call the server's tools until it
// calls submit_answer, stops, or reaches maxTurns replies.
func Run(ctx context.Context, m Model, srv Server, ds string, q Question, truth [][]any, run, maxTurns int) (r Result) {
	r = Result{Dataset: ds, Server: srv.Name(), Question: q.ID, Kind: q.Kind, Run: run}
	start := time.Now()
	defer func() { r.WallMS = time.Since(start).Milliseconds() }()
	tools := append(ToolDefs(srv.Tools()), SubmitTool)
	reply, err := m.Start(ctx, SystemPrompt, PromptFor(q), tools)
	for {
		if err != nil {
			r.Error = "model: " + err.Error()
			return r
		}
		r.Turns++
		r.InputTokens += reply.Usage.Input
		r.OutputTokens += reply.Usage.Output
		if len(reply.Calls) == 0 {
			r.Error = "the model stopped without calling submit_answer"
			return r
		}
		var results []ToolResult
		for _, c := range reply.Calls {
			if c.Name == SubmitTool.Name {
				var in struct{ Rows [][]any }
				if err := json.Unmarshal(c.Input, &in); err != nil {
					results = append(results, ToolResult{ID: c.ID, IsError: true, Content: "rows must be an array of arrays: " + err.Error()})
					continue
				}
				if in.Rows == nil { // rows missing or null, e.g. input cut off at max_tokens
					results = append(results, ToolResult{ID: c.ID, IsError: true, Content: "rows must be an array of arrays: rows is missing or null"})
					continue
				}
				r.Submitted, r.Answer = true, in.Rows
				r.Correct = Equal(truth, in.Rows, q.Ordered)
				results = append(results, ToolResult{ID: c.ID, Content: "answer recorded"})
				continue
			}
			r.ToolCalls++
			out, isErr := srv.Call(ctx, c.Name, c.Input)
			out, cut := capOutput(out)
			if cut {
				r.Truncations++
			}
			results = append(results, ToolResult{ID: c.ID, Content: out, IsError: isErr})
		}
		if r.Submitted {
			return r
		}
		if r.Turns >= maxTurns {
			r.Error = fmt.Sprintf("turn limit (%d) reached without submit_answer", maxTurns)
			return r
		}
		reply, err = m.Continue(ctx, results)
	}
}
