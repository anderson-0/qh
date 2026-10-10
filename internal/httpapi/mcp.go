package httpapi

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/mcpserver"
)

// mcpServer returns the MCP server for the request's workspace, built once
// per workspace. The route has already authenticated the caller and
// resolved the workspace.
func (a *api) mcpServer(r *http.Request) *mcp.Server {
	ws := workspace(r)
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	s, ok := a.servers[ws]
	if !ok {
		s = mcpserver.New(a.cfg.Service, func(context.Context) string { return ws }, a.cfg.Version)
		a.servers[ws] = s
	}
	return s
}
