package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

// WorkspaceInfo is what may be shown about a workspace.
type WorkspaceInfo struct {
	Name      string
	CreatedAt time.Time
}

func noWorkspace(name string) error {
	return qerr.New(qerr.UnknownSource, fmt.Sprintf("no workspace named %q", name))
}

// CreateWorkspace creates an empty workspace.
func (s *Service) CreateWorkspace(ctx context.Context, name string) (*WorkspaceInfo, error) {
	if !nameRe.MatchString(name) {
		return nil, qerr.New(qerr.Invalid, fmt.Sprintf("invalid workspace name %q", name)).
			WithHint("Use lowercase letters, digits, _ and -, starting with a letter or digit, at most 64 characters")
	}
	w, err := s.store.CreateWorkspace(ctx, name)
	if errors.Is(err, store.ErrExists) {
		return nil, qerr.New(qerr.Invalid, fmt.Sprintf("a workspace named %q already exists", name))
	}
	if err != nil {
		return nil, err
	}
	return &WorkspaceInfo{Name: w.ID, CreatedAt: w.CreatedAt}, nil
}

// ListWorkspaces returns every workspace ordered by name.
func (s *Service) ListWorkspaces(ctx context.Context) ([]*WorkspaceInfo, error) {
	list, err := s.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*WorkspaceInfo, len(list))
	for i, w := range list {
		out[i] = &WorkspaceInfo{Name: w.ID, CreatedAt: w.CreatedAt}
	}
	return out, nil
}

// WorkspaceExists reports whether a workspace exists.
func (s *Service) WorkspaceExists(ctx context.Context, name string) (bool, error) {
	list, err := s.store.ListWorkspaces(ctx)
	if err != nil {
		return false, err
	}
	for _, w := range list {
		if w.ID == name {
			return true, nil
		}
	}
	return false, nil
}

// DeleteWorkspace removes a workspace with its connections and keys, and
// closes the connections' pools.
func (s *Service) DeleteWorkspace(ctx context.Context, name string) error {
	if name == store.DefaultWorkspace {
		return qerr.New(qerr.Invalid, "the default workspace cannot be deleted")
	}
	conns, err := s.store.ListConnections(ctx, name)
	if err != nil {
		return err
	}
	if err := s.store.DeleteWorkspace(ctx, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return noWorkspace(name)
		}
		return err
	}
	for _, c := range conns {
		s.pools.Drop(c.ID)
		s.invalidate(ctx, c.ID)
		s.forget(name, c.ID)
	}
	return nil
}
