package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/mcpserver"
)

func queryCmd(with withBackend) *cobra.Command {
	var maxRows int
	var explain bool
	cmd := &cobra.Command{
		Use:   "query <connection> <statement|->",
		Short: "Run one statement and print TSV (- reads the statement from stdin)",
		Args:  cobra.ExactArgs(2),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			q := args[1]
			if q == "-" {
				raw, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				q = string(raw)
			}
			out, err := b.Query(cmd.Context(), core.QueryInput{Source: args[0], Query: q, MaxRows: maxRows, Explain: explain})
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		}),
	}
	cmd.Flags().IntVar(&maxRows, "max-rows", 0, "row cap (at most the connection's limit)")
	cmd.Flags().BoolVar(&explain, "explain", false, "show the plan without running")
	return cmd
}

func schemaCmd(with withBackend) *cobra.Command {
	var object string
	cmd := &cobra.Command{
		Use:   "schema <connection> [pattern]",
		Short: "Show the schema overview, or tables matching a pattern",
		Args:  cobra.RangeArgs(1, 2),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			in := core.SchemaInput{Source: args[0], Object: object}
			if len(args) == 2 {
				in.Pattern = args[1]
			}
			out, err := b.Schema(cmd.Context(), in)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		}),
	}
	cmd.Flags().StringVar(&object, "object", "", "exact table name")
	return cmd
}

func mcpCmd(home *string, version string) *cobra.Command {
	var lf queryLogFlags
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve MCP over stdio (local store)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := refuseRemote(cmd); err != nil {
				return err
			}
			retention, err := lf.retention()
			if err != nil {
				return err
			}
			dir, err := homeDir(*home)
			if err != nil {
				return err
			}
			old, err := oldMasterKeys(os.Getenv("QH_MASTER_KEY_OLD"))
			if err != nil {
				return err
			}
			a, err := openAppWith(cmd.Context(), dir, core.Options{NoQueryLog: lf.off}, old...)
			if err != nil {
				return err
			}
			defer a.Close()
			// Cancelled before a.Close runs, so background work never outlives the store.
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			a.pools.Start(ctx)
			a.svc.Start(ctx)
			pruneQueryLog(ctx, a.svc, retention) // retention applies even when logging is off
			ws, _ := workspaceOf(cmd)
			if err := requireWorkspace(ctx, a, ws); err != nil {
				return err
			}
			srv := mcpserver.New(a.svc, func(context.Context) string { return ws }, version)
			// The command's own streams, not os.Stdin/os.Stdout: main passes
			// those, and tests pass pipes.
			err = srv.Run(ctx, &mcp.IOTransport{Reader: readCloser(cmd.InOrStdin()), Writer: nopWriteCloser{cmd.OutOrStdout()}})
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	lf.register(cmd)
	return cmd
}

// readCloser returns r as an io.ReadCloser, closing it only if it can be closed.
func readCloser(r io.Reader) io.ReadCloser {
	if rc, ok := r.(io.ReadCloser); ok {
		return rc
	}
	return io.NopCloser(r)
}

// nopWriteCloser leaves the writer open when the MCP session closes it, so
// stdout stays usable for the error the command may print.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
