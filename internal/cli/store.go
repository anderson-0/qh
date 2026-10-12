package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

func storeCmd(home *string) *cobra.Command {
	c := &cobra.Command{Use: "store", Short: "Manage the store that holds connections and keys"}
	var to string
	var withLog bool
	cp := &cobra.Command{
		Use:   "copy",
		Short: "Copy everything from the current store to an empty Postgres store",
		Long: "Copies workspaces, connections and API keys (and, with --with-query-log, the query log) from the current\n" +
			"store (--store, or the SQLite file in the data directory) to the empty Postgres store --to. Sealed connection\n" +
			"settings are copied as they are: keep using the same master key with the new store. Stop other writers (such as qh\n" +
			"serve instances using the source store) before copying.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := refuseRemote(cmd); err != nil {
				return err
			}
			if to == "" {
				return qerr.New(qerr.Invalid, "--to is required").WithHint("Give the postgres:// URL of the new store")
			}
			dir, err := homeDir(*home)
			if err != nil {
				return err
			}
			if err := requireLocalStore(dir); err != nil {
				return err
			}
			ctx := cmd.Context()
			src, err := store.Open(ctx, storeDSN(), dir)
			if err != nil {
				return storeOpenError(err, storeDSN() != "")
			}
			defer src.Close()
			dst, err := store.Open(ctx, to, "")
			if errors.Is(err, store.ErrInvalidDSN) {
				return qerr.New(qerr.Invalid, "--to must be a postgres:// URL")
			}
			if err != nil {
				return qerr.Wrap(qerr.ConnectionFailed, "cannot open the target store: "+err.Error(), err).
					WithHint("Check --to, and that the database is reachable")
			}
			defer dst.Close()
			if err := requireEmpty(ctx, dst); err != nil {
				return err
			}
			n, err := copyStore(ctx, src, dst, withLog)
			if err != nil {
				return qerr.Wrap(qerr.ConnectionFailed, "copy failed: "+err.Error(), err).
					WithHint("The target may be partly filled; drop and recreate that database before trying again")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "copied %d workspaces, %d connections, %d keys, %d query-log entries\n",
				n.workspaces, n.connections, n.keys, n.log)
			fmt.Fprintln(cmd.OutOrStdout(), "use the same master key with the new store")
			return nil
		},
	}
	cp.Flags().StringVar(&to, "to", "", "postgres:// URL of the empty store to copy into")
	cp.Flags().BoolVar(&withLog, "with-query-log", false, "copy the query log too")
	c.AddCommand(cp)
	return c
}

// requireEmpty fails unless dst holds only the default workspace and no
// connections, keys or query-log entries.
func requireEmpty(ctx context.Context, dst store.Store) error {
	ws, err := dst.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	keys, err := dst.ListAllKeys(ctx)
	if err != nil {
		return err
	}
	conns, err := dst.ListConnections(ctx, store.DefaultWorkspace)
	if err != nil {
		return err
	}
	logged, err := dst.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: store.DefaultWorkspace, Limit: 1})
	if err != nil {
		return err
	}
	if len(ws) != 1 || len(keys) != 0 || len(conns) != 0 || len(logged) != 0 {
		return qerr.New(qerr.Invalid, "the target store is not empty").
			WithHint("Copy into a new, empty database")
	}
	return nil
}

type copied struct{ workspaces, connections, keys, log int }

func copyStore(ctx context.Context, src, dst store.Store, withLog bool) (copied, error) {
	var n copied
	wss, err := src.ListWorkspaces(ctx)
	if err != nil {
		return n, err
	}
	for _, w := range wss {
		if err := dst.ImportWorkspace(ctx, w); err != nil {
			return n, fmt.Errorf("workspace %s: %w", w.ID, err)
		}
		n.workspaces++
		conns, err := src.ListConnections(ctx, w.ID)
		if err != nil {
			return n, err
		}
		for _, c := range conns {
			if err := dst.ImportConnection(ctx, c); err != nil {
				return n, fmt.Errorf("connection %s/%s: %w", w.ID, c.Name, err)
			}
			n.connections++
		}
		if withLog {
			// A zero Limit means no limit, so this is the whole log, newest first.
			entries, err := src.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: w.ID})
			if err != nil {
				return n, err
			}
			for i := len(entries) - 1; i >= 0; i-- { // oldest first, so new ids keep the order
				e := *entries[i]
				e.ID = 0
				if err := dst.AppendQueryLog(ctx, &e); err != nil {
					return n, err
				}
				n.log++
			}
		}
	}
	keys, err := src.ListAllKeys(ctx)
	if err != nil {
		return n, err
	}
	for _, k := range keys {
		if err := dst.ImportKey(ctx, k); err != nil {
			return n, fmt.Errorf("key %s: %w", k.Prefix, err)
		}
		n.keys++
	}
	return n, nil
}
