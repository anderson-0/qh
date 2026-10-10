package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

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
			a, err := openAppWith(dir, core.Options{NoQueryLog: lf.off})
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := cmd.Context()
			a.pools.Start(ctx)
			if !lf.off {
				pruneQueryLog(ctx, a.svc, retention)
			}
			ws, _ := workspaceOf(cmd)
			if err := requireWorkspace(ctx, a, ws); err != nil {
				return err
			}
			srv := mcpserver.New(a.svc, func(context.Context) string { return ws }, version)
			err = srv.Run(ctx, &mcp.StdioTransport{})
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	lf.register(cmd)
	return cmd
}
