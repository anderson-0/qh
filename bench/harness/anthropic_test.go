package harness

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// TestToolDefinitionBudget is spec §9.1: qh's three tool definitions cost at
// most 800 tokens.
func TestToolDefinitionBudget(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	ds := chinook(t)
	ctx := context.Background()
	bin, cleanup, err := BuildQH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	srv, err := StartQH(ctx, bin, []Conn{ConnFor(ds)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	n, err := CountToolTokens(ctx, key, DefaultModel, ToolDefs(srv.Tools()), srv.Instructions())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("qh tool definitions: %d tokens", n)
	if n <= 0 || n > 800 {
		t.Fatalf("qh tool definitions cost %d tokens, budget 800", n)
	}
}

// TestAnthropicRequestIsWellFormed builds the request offline and checks the
// tool schemas survive the SDK's JSON encoding, extra keys included.
func TestAnthropicRequestIsWellFormed(t *testing.T) {
	m := &anthropicModel{model: DefaultModel, system: "sys", effort: "high"}
	defs := []ToolDef{
		{Name: "query", Description: "run SQL", Schema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"sql": map[string]any{"type": "string"}},
			"required":             []any{"sql"},
			"additionalProperties": false,
		}},
		SubmitTool,
	}
	for _, d := range defs {
		p := toolParam(d)
		m.tools = append(m.tools, anthropic.ToolUnionParam{OfTool: &p})
	}
	m.messages = []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))}
	b, err := json.Marshal(m.newParams())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Model        string `json:"model"`
		MaxTokens    int    `json:"max_tokens"`
		CacheControl struct {
			Type string `json:"type"`
		} `json:"cache_control"`
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
		System []struct{ Text string }
		Tools  []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema struct {
				Type                 string                     `json:"type"`
				Properties           map[string]json.RawMessage `json:"properties"`
				Required             []string                   `json:"required"`
				AdditionalProperties *bool                      `json:"additionalProperties"`
			} `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	if got.Model != DefaultModel || got.MaxTokens != maxTokens || len(got.System) != 1 || got.System[0].Text != "sys" || len(got.Tools) != 2 {
		t.Fatalf("%s", b)
	}
	if got.MaxTokens != 16000 || got.OutputConfig.Effort != "high" {
		t.Fatalf("max_tokens %d, effort %q: %s", got.MaxTokens, got.OutputConfig.Effort, b)
	}
	if got.CacheControl.Type != "ephemeral" {
		t.Fatalf("no top-level cache_control: %s", b)
	}
	q := got.Tools[0]
	if q.Name != "query" || q.Description != "run SQL" || q.InputSchema.Type != "object" ||
		q.InputSchema.Properties["sql"] == nil || len(q.InputSchema.Required) != 1 || q.InputSchema.Required[0] != "sql" ||
		q.InputSchema.AdditionalProperties == nil || *q.InputSchema.AdditionalProperties {
		t.Fatalf("query tool: %s", b)
	}
	s := got.Tools[1]
	if s.Name != "submit_answer" || s.InputSchema.Properties["rows"] == nil || len(s.InputSchema.Required) != 1 || s.InputSchema.Required[0] != "rows" {
		t.Fatalf("submit tool: %s", b)
	}
}

// TestCountParamsPutInstructionsInSystem checks, offline, that the measured
// count_tokens call carries the server instructions as the system prompt and
// the baseline does not.
func TestCountParamsPutInstructionsInSystem(t *testing.T) {
	system := func(instructions string) string {
		b, err := json.Marshal(countParams(DefaultModel, []ToolDef{noopTool, SubmitTool}, instructions))
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			System json.RawMessage `json:"system"`
			Tools  []struct{ Name string }
		}
		if err := json.Unmarshal(b, &got); err != nil || len(got.Tools) != 2 {
			t.Fatalf("%v: %s", err, b)
		}
		return string(got.System)
	}
	if got := system(""); got != "" {
		t.Fatalf("system without instructions: %s", got)
	}
	if got := system("Use sources."); !strings.Contains(got, "Use sources.") {
		t.Fatalf("system with instructions: %s", got)
	}
}

// TestPreflightSendsTheRealRequestShape marshals the preflight request offline.
func TestPreflightSendsTheRealRequestShape(t *testing.T) {
	m := preflightModel(anthropic.Client{}, DefaultModel, "high")
	b, err := json.Marshal(m.newParams())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		MaxTokens    int `json:"max_tokens"`
		CacheControl struct {
			Type string `json:"type"`
		} `json:"cache_control"`
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
		System   []struct{ Text string }
		Tools    []struct{ Name string }
		Messages []struct {
			Content []struct{ Text string }
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.MaxTokens != 16000 || got.CacheControl.Type != "ephemeral" || got.OutputConfig.Effort != "high" ||
		len(got.System) != 1 || got.System[0].Text == "" || len(got.Tools) != 1 || got.Tools[0].Name != noopTool.Name ||
		len(got.Messages) != 1 || got.Messages[0].Content[0].Text != "Reply with OK" {
		t.Fatalf("%s", b)
	}
}
