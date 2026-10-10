package harness

import (
	"context"
	"encoding/json"

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
	client := anthropic.NewClient(option.WithAPIKey(apiKey), option.WithMaxRetries(6))
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
		Model:        anthropic.Model(m.model),
		MaxTokens:    maxTokens,
		System:       []anthropic.TextBlockParam{{Text: m.system}},
		Messages:     m.messages,
		Tools:        m.tools,
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(m.effort)},
	}
}

func (m *anthropicModel) Start(ctx context.Context, system, user string, tools []ToolDef) (*Reply, error) {
	m.system = system
	m.tools = nil
	for _, t := range tools {
		p := toolParam(t)
		m.tools = append(m.tools, anthropic.ToolUnionParam{OfTool: &p})
	}
	m.messages = []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))}
	return m.send(ctx)
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

// CountToolTokens measures what the tool definitions add to every request.
func CountToolTokens(ctx context.Context, apiKey, model string, tools []ToolDef) (int64, error) {
	client := anthropic.NewClient(option.WithAPIKey(apiKey), option.WithMaxRetries(6))
	count := func(defs []ToolDef) (int64, error) {
		var params []anthropic.MessageCountTokensToolUnionParam
		for _, t := range defs {
			p := toolParam(t)
			params = append(params, anthropic.MessageCountTokensToolUnionParam{OfTool: &p})
		}
		res, err := client.Messages.CountTokens(ctx, anthropic.MessageCountTokensParams{
			Model:    anthropic.Model(model),
			Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
			Tools:    params,
		})
		if err != nil {
			return 0, err
		}
		return res.InputTokens, nil
	}
	base, err := count([]ToolDef{noopTool})
	if err != nil {
		return 0, err
	}
	with, err := count(append([]ToolDef{noopTool}, tools...))
	if err != nil {
		return 0, err
	}
	return with - base, nil
}
