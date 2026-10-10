package core

import (
	"context"
	"fmt"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

// RotateMasterKey re-seals every connection, in every workspace, with the
// keyring's primary key. Connections already sealed with it are counted in
// current and left alone. Everything happens in one store transaction: if a
// connection cannot be opened with the keyring's keys, nothing changes.
func (s *Service) RotateMasterKey(ctx context.Context) (resealed, current int, err error) {
	primary := s.keys.PrimaryID()
	resealed, err = s.store.ResealConnections(ctx, func(c *store.Connection) ([]byte, uint32, error) {
		if c.SecretKeyID == primary {
			current++
			return nil, 0, nil
		}
		pt, err := s.keys.Open(c.Secret, []byte(c.ID))
		if err != nil {
			return nil, 0, qerr.New(qerr.Invalid, fmt.Sprintf("connection %q in workspace %q cannot be opened with the current or the old master key", c.Name, c.WorkspaceID)).
				WithHint("Pass the key it was sealed with as --old-key-file; nothing was changed")
		}
		sealed, err := s.keys.Seal(pt, []byte(c.ID))
		return sealed, primary, err
	})
	if err != nil {
		return 0, 0, err
	}
	return resealed, current, nil
}
