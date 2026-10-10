// Package mcpserver exposes the core service as three MCP tools.
package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/qerr"
)

const instructions = "Use sources to pick a connection, schema to learn tables, columns, keys and joins, then query. " +
	"Prefer schema with a pattern over guessing names."

// WorkspaceFunc returns the workspace a request belongs to.
type WorkspaceFunc func(ctx context.Context) string

// SchemaArgs is the schema tool's input.
type SchemaArgs struct {
	Source  string `json:"source" jsonschema:"connection name from sources"`
	Pattern string `json:"pattern,omitempty" jsonschema:"name filter; * is a wildcard, e.g. *order*"`
	Object  string `json:"object,omitempty" jsonschema:"exact table, e.g. orders or public.orders"`
}

// QueryArgs is the query tool's input.
type QueryArgs struct {
	Source  string `json:"source" jsonschema:"connection name from sources"`
	Query   string `json:"query" jsonschema:"one statement in the source's language (SQL)"`
	MaxRows int    `json:"max_rows,omitempty" jsonschema:"row cap, at most the connection's limit"`
	Explain bool   `json:"explain,omitempty" jsonschema:"show the plan without running"`
}

// New returns an MCP server with the sources, schema and query tools.
func New(svc *core.Service, ws WorkspaceFunc, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "qh", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	closed := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "sources",
		Description: "List database connections: name, engine, ro/rw, description.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return text(svc.Sources(ctx, ws(ctx)))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "schema",
		Description: "Describe tables. No pattern/object: overview with row estimates. " +
			"With pattern or object: columns, types, PK, NN (not null), FKs (→ references, ← referenced by), enum values, indexes.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a SchemaArgs) (*mcp.CallToolResult, any, error) {
		return text(svc.Schema(ctx, ws(ctx), core.SchemaInput{Source: a.Source, Pattern: a.Pattern, Object: a.Object}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "query",
		Description: "Run one statement; returns TSV (default cap 100 rows; the footer says when rows were cut). " +
			"Read-only connections reject writes. explain=true returns the plan without running.",
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a QueryArgs) (*mcp.CallToolResult, any, error) {
		return text(svc.Query(ctx, ws(ctx), core.QueryInput{Source: a.Source, Query: a.Query, MaxRows: a.MaxRows, Explain: a.Explain}))
	})
	return s
}

// text turns a service result into tool content; errors become isError results
// so the agent can read them and retry.
func text(out string, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: qerr.From(err).Error()}}}, nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil, nil
}
