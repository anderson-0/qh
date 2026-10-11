package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// maxTokens leaves room for a long submit_answer and for adaptive thinking at
// the configured effort.
const maxTokens = 16000

// DefaultEffort is the output_config effort every request is sent with unless -effort says otherwise.
const DefaultEffort = "high"

type anthropicModel struct {
	client   anthropic.Client
	model    string
	effort   string
	system   string
	tools    []anthropic.ToolUnionParam
	messages []anthropic.MessageParam
}

// NewAnthropic returns a factory of conversations with model.
func NewAnthropic(apiKey, model, effort string) ModelFactory {
	client := anthropic.NewClient(clientOptions(apiKey, 6)...)
	return func() Model { return &anthropicModel{client: client, model: model, effort: effort} }
}

// toolParam converts a ToolDef. Schema keys other than type, properties and
// required (additionalProperties, $defs, ...) go through ExtraFields, so the
// model sees the schema an MCP client would show it.
func toolParam(t ToolDef) anthropic.ToolParam {
	p := anthropic.ToolParam{Name: t.Name, Description: anthropic.String(t.Description)}
	p.InputSchema.Properties = t.Schema["properties"]
	switch req := t.Schema["required"].(type) {
	case []any:
		for _, r := range req {
			if s, ok := r.(string); ok {
				p.InputSchema.Required = append(p.InputSchema.Required, s)
			}
		}
	case []string:
		p.InputSchema.Required = append(p.InputSchema.Required, req...)
	}
	for k, v := range t.Schema {
		switch k {
		case "type", "properties", "required":
		default:
			if p.InputSchema.ExtraFields == nil {
				p.InputSchema.ExtraFields = map[string]any{}
			}
			p.InputSchema.ExtraFields[k] = v
		}
	}
	return p
}

// newParams builds the request for one turn. Every request of a benchmark
// uses the same parameters.
func (m *anthropicModel) newParams() anthropic.MessageNewParams {
	return anthropic.MessageNewParams{
		Model:     anthropic.Model(m.model),
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: m.system}},
		Messages:  m.messages,
		Tools:     m.tools,
		// Cache the whole prefix (tools, system, messages so far): every turn re-sends it.
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(m.effort)},
	}
}

func (m *anthropicModel) Start(ctx context.Context, system, user string, tools []ToolDef) (*Reply, error) {
	m.begin(system, user, tools)
	return m.send(ctx)
}

// begin opens a conversation without sending it.
func (m *anthropicModel) begin(system, user string, tools []ToolDef) {
	m.system = system
	m.tools = nil
	for _, t := range tools {
		p := toolParam(t)
		m.tools = append(m.tools, anthropic.ToolUnionParam{OfTool: &p})
	}
	m.messages = []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))}
}

func (m *anthropicModel) Continue(ctx context.Context, results []ToolResult) (*Reply, error) {
	blocks := make([]anthropic.ContentBlockParamUnion, len(results))
	for i, r := range results {
		blocks[i] = anthropic.NewToolResultBlock(r.ID, r.Content, r.IsError)
	}
	m.messages = append(m.messages, anthropic.NewUserMessage(blocks...))
	return m.send(ctx)
}

func (m *anthropicModel) send(ctx context.Context) (*Reply, error) {
	msg, err := m.client.Messages.New(ctx, m.newParams())
	if err != nil {
		return nil, err
	}
	m.messages = append(m.messages, msg.ToParam())
	u := msg.Usage
	r := &Reply{StopReason: string(msg.StopReason), Usage: Usage{Input: u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens, Output: u.OutputTokens}}
	for _, b := range msg.Content {
		if tu, ok := b.AsAny().(anthropic.ToolUseBlock); ok {
			r.Calls = append(r.Calls, ToolCall{ID: tu.ID, Name: tu.Name, Input: json.RawMessage(tu.Input)})
		}
	}
	return r, nil
}

// noopTool is the baseline for CountToolTokens: the API adds a fixed
// tool-use preamble whenever any tool is present, so tool cost is measured
// as count(noop + tools) - count(noop).
var noopTool = ToolDef{Name: "noop", Description: "Does nothing.", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}

// countParams is a count_tokens request for defs. A non-empty system
// (a server's initialize instructions) goes into the system prompt.
func countParams(model string, defs []ToolDef, system string) anthropic.MessageCountTokensParams {
	var params []anthropic.MessageCountTokensToolUnionParam
	for _, t := range defs {
		p := toolParam(t)
		params = append(params, anthropic.MessageCountTokensToolUnionParam{OfTool: &p})
	}
	p := anthropic.MessageCountTokensParams{
		Model:    anthropic.Model(model),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
		Tools:    params,
	}
	if system != "" {
		p.System = anthropic.MessageCountTokensParamsSystemUnion{OfString: anthropic.String(system)}
	}
	return p
}

// CountToolTokens measures what the tool definitions and the server's
// instructions add to every request. The instructions go in the system prompt
// of the measured call only, so they are part of the difference.
func CountToolTokens(ctx context.Context, apiKey, model string, tools []ToolDef, instructions string) (int64, error) {
	client := anthropic.NewClient(clientOptions(apiKey, 6)...)
	count := func(defs []ToolDef, system string) (int64, error) {
		res, err := client.Messages.CountTokens(ctx, countParams(model, defs, system))
		if err != nil {
			return 0, err
		}
		return res.InputTokens, nil
	}
	base, err := count([]ToolDef{noopTool}, "")
	if err != nil {
		return 0, err
	}
	with, err := count(append([]ToolDef{noopTool}, tools...), instructions)
	if err != nil {
		return 0, err
	}
	return with - base, nil
}

// IsTransient reports whether err from the model is worth excluding a run
// over rather than counting against the server: rate limits (429), server
// errors (5xx, including 529 overloaded) once the SDK's retries are spent, and
// network or context errors. Other API errors (400 "prompt is too long",
// invalid requests, 401, 404) are properties of the request, so the run failed.
func IsTransient(err error) bool {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 429 || apiErr.StatusCode >= 500
	}
	return true
}

// preflightModel is the conversation Preflight sends: the real request shape
// (max_tokens, effort, cache control, a system prompt, a tool) with a trivial
// prompt.
func preflightModel(client anthropic.Client, model, effort string) *anthropicModel {
	m := &anthropicModel{client: client, model: model, effort: effort}
	m.begin("You are a connectivity check.", "Reply with OK", []ToolDef{noopTool})
	return m
}

// Preflight makes the two cheapest real requests of the benchmark, a
// count_tokens call and one message built by the same newParams as every
// conversation, so a bad key, an unknown model or a rejected parameter fails
// before hundreds of conversations do. Any reply is accepted.
func Preflight(ctx context.Context, apiKey, model, effort string) error {
	if _, err := CountToolTokens(ctx, apiKey, model, []ToolDef{{Name: "ping", Description: "Does nothing.", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}}, ""); err != nil {
		return fmt.Errorf("count_tokens: %w", err)
	}
	client := anthropic.NewClient(clientOptions(apiKey, 2)...)
	if _, err := preflightModel(client, model, effort).send(ctx); err != nil {
		return fmt.Errorf("messages: %w", err)
	}
	return nil
}

// clientOptions are the options of every Anthropic client the benchmark
// creates. Keys not scoped to a workspace need the workspace named on each
// request; ANTHROPIC_WORKSPACE_ID, when set, is sent as anthropic-workspace-id.
func clientOptions(apiKey string, retries int) []option.RequestOption {
	opts := []option.RequestOption{option.WithAPIKey(apiKey), option.WithMaxRetries(retries), option.WithRequestTimeout(requestTimeout)}
	if ws := os.Getenv("ANTHROPIC_WORKSPACE_ID"); ws != "" {
		opts = append(opts, option.WithHeader("anthropic-workspace-id", ws))
	}
	return opts
}
