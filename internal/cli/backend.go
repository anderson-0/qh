package cli

import (
	"context"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/core"
)

// backend is what the connection, query, log, workspace and key commands
// act on: the local store (localBackend), or a qh server (client.Client).
type backend interface {
	ListConnections(ctx context.Context) ([]*core.ConnInfo, error)
	GetConnection(ctx context.Context, name string) (*core.ConnInfo, error)
	CreateConnection(ctx context.Context, in core.ConnInput, test bool) (*core.ConnInfo, *core.TestResult, error)
	UpdateConnection(ctx context.Context, name string, p core.ConnPatch, test bool) (*core.ConnInfo, *core.TestResult, error)
	DeleteConnection(ctx context.Context, name string) error
	TestConnection(ctx context.Context, name string) (*core.TestResult, error)
	Query(ctx context.Context, in core.QueryInput) (string, error)
	Schema(ctx context.Context, in core.SchemaInput) (string, error)
	QueryLog(ctx context.Context, f core.QueryLogFilter) ([]*core.QueryLogEntry, error)
	CreateWorkspace(ctx context.Context, name string) (*core.WorkspaceInfo, error)
	ListWorkspaces(ctx context.Context) ([]*core.WorkspaceInfo, error)
	DeleteWorkspace(ctx context.Context, name string) error
	CreateKey(ctx context.Context, in auth.KeyInput) (string, *auth.KeyInfo, error)
	ListKeys(ctx context.Context) ([]*auth.KeyInfo, error)
	DeleteKey(ctx context.Context, id string) error
}

// localBackend works on the local store, in one workspace.
type localBackend struct {
	a  *app
	ws string
}

func (b *localBackend) ListConnections(ctx context.Context) ([]*core.ConnInfo, error) {
	return b.a.svc.ListConnections(ctx, b.ws)
}

func (b *localBackend) GetConnection(ctx context.Context, name string) (*core.ConnInfo, error) {
	return b.a.svc.GetConnection(ctx, b.ws, name)
}

func (b *localBackend) CreateConnection(ctx context.Context, in core.ConnInput, test bool) (*core.ConnInfo, *core.TestResult, error) {
	return b.a.svc.CreateConnection(ctx, b.ws, in, test)
}

func (b *localBackend) UpdateConnection(ctx context.Context, name string, p core.ConnPatch, test bool) (*core.ConnInfo, *core.TestResult, error) {
	return b.a.svc.UpdateConnection(ctx, b.ws, name, p, test)
}

func (b *localBackend) DeleteConnection(ctx context.Context, name string) error {
	return b.a.svc.DeleteConnection(ctx, b.ws, name)
}

func (b *localBackend) TestConnection(ctx context.Context, name string) (*core.TestResult, error) {
	return b.a.svc.TestConnection(ctx, b.ws, name)
}

func (b *localBackend) Query(ctx context.Context, in core.QueryInput) (string, error) {
	return b.a.svc.Query(ctx, b.ws, in)
}

func (b *localBackend) Schema(ctx context.Context, in core.SchemaInput) (string, error) {
	return b.a.svc.Schema(ctx, b.ws, in)
}

func (b *localBackend) QueryLog(ctx context.Context, f core.QueryLogFilter) ([]*core.QueryLogEntry, error) {
	return b.a.svc.QueryLog(ctx, b.ws, f)
}

func (b *localBackend) CreateWorkspace(ctx context.Context, name string) (*core.WorkspaceInfo, error) {
	return b.a.svc.CreateWorkspace(ctx, name)
}

func (b *localBackend) ListWorkspaces(ctx context.Context) ([]*core.WorkspaceInfo, error) {
	return b.a.svc.ListWorkspaces(ctx)
}

func (b *localBackend) DeleteWorkspace(ctx context.Context, name string) error {
	return b.a.svc.DeleteWorkspace(ctx, name)
}

func (b *localBackend) CreateKey(ctx context.Context, in auth.KeyInput) (string, *auth.KeyInfo, error) {
	return auth.CreateKey(ctx, b.a.st, in)
}

func (b *localBackend) ListKeys(ctx context.Context) ([]*auth.KeyInfo, error) {
	return auth.ListKeys(ctx, b.a.st, "", true)
}

func (b *localBackend) DeleteKey(ctx context.Context, id string) error {
	return auth.DeleteKey(ctx, b.a.st, id)
}
