// Package mcpserver exposes the core service as two MCP tools.
package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/qerr"
)

// instructions is the fixed part of the initialize instructions; the
// workspace's connections follow it (see connectionsLine).
//
// Every word here and in the tool definitions is paid on every model turn,
// so they stay terse.
const instructions = "Query directly if you know the names; else call schema first. Errors on unknown names suggest close matches."

// maxListed caps the connections named in the instructions.
const maxListed = 20

// maxDescription caps each connection description in the instructions, in
// characters; the full text stays in the schema connection list.
const maxDescription = 60

// clipDescription collapses whitespace and cuts s to maxDescription
// characters, ending a cut one with "…".
func clipDescription(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxDescription {
		return s
	}
	return string(r[:maxDescription-1]) + "…"
}

// connectionsLine names the workspace's connections so an agent can query
// without a schema call first.
func connectionsLine(list []*core.ConnInfo) string {
	describe := func(c *core.ConnInfo) string {
		s := fmt.Sprintf("%s (%s, %s)", c.Name, c.Engine, c.Mode)
		if c.Description != "" {
			s += ": " + clipDescription(c.Description)
		}
		return s
	}
	switch {
	case len(list) == 0:
		return "No connections exist yet."
	case len(list) == 1:
		return "Connection: " + describe(list[0]) + ". source may be omitted."
	}
	names := make([]string, 0, min(len(list), maxListed))
	for _, c := range list[:min(len(list), maxListed)] {
		names = append(names, describe(c))
	}
	line := "Connections (pass one as source): " + strings.Join(names, "; ")
	if len(list) > maxListed {
		line += fmt.Sprintf("; and %d more (schema lists all)", len(list)-maxListed)
	}
	return line + "."
}

// withConnections adds the workspace's connections to the instructions of
// each initialize (and discover) result, so they are current per session.
func withConnections(svc *core.Service, ws WorkspaceFunc) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil {
				return res, err
			}
			var in *string
			switch r := res.(type) {
			case *mcp.InitializeResult:
				in = &r.Instructions
			case *mcp.DiscoverResult:
				in = &r.Instructions
			default:
				return res, nil
			}
			if list, lerr := svc.ListConnections(ctx, ws(ctx)); lerr == nil {
				*in += " " + connectionsLine(list)
			}
			return res, nil
		}
	}
}

// WorkspaceFunc returns the workspace a request belongs to.
type WorkspaceFunc func(ctx context.Context) string

// SchemaArgs is the schema tool's input.
type SchemaArgs struct {
	Source  string `json:"source,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	Object  string `json:"object,omitempty"`
}

// QueryArgs is the query tool's input.
type QueryArgs struct {
	Source  string `json:"source,omitempty"`
	Query   string `json:"query"`
	MaxRows int    `json:"max_rows,omitempty"`
	Explain bool   `json:"explain,omitempty" jsonschema:"plan only"`
}

// severalHeader introduces the connection list that schema returns, with no
// arguments, in a workspace that does not have exactly one connection.
const severalHeader = "-- several connections; pass one as source\n"

// New returns an MCP server with the schema and query tools.
func New(svc *core.Service, ws WorkspaceFunc, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "qh", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	closed := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}
	s.AddReceivingMiddleware(withConnections(svc, ws))

	mcp.AddTool(s, &mcp.Tool{
		Name: "schema",
		Description: "No arguments: tables with columns, PKs and FK targets (names only for large schemas). " +
			"pattern (* wildcard) or object: types, NN (not null), FKs (→ references, ← referenced by), enums, indexes.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a SchemaArgs) (*mcp.CallToolResult, any, error) {
		w := ws(ctx)
		if a.Source == "" && a.Pattern == "" && a.Object == "" {
			list, err := svc.ListConnections(ctx, w)
			if err != nil {
				return text("", err)
			}
			if len(list) != 1 {
				out, err := svc.Sources(ctx, w)
				if err == nil && len(list) > 1 {
					out = severalHeader + out
				}
				return text(out, err)
			}
		}
		return text(svc.Schema(ctx, w, core.SchemaInput{Source: a.Source, Pattern: a.Pattern, Object: a.Object}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "query",
		Description: "Run one SQL statement. Returns TSV, at most 100 rows unless max_rows is set.",
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
