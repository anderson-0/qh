package httpapi

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (a *api) mcpServer(*http.Request) *mcp.Server { return nil }
