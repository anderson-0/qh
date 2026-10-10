// Package cli implements the qh command line.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/client"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

// Execute runs the qh command line and returns the process exit code.
func Execute(version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRoot(version)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

var _ backend = (*client.Client)(nil)

// withBackend wraps a command so it runs against a backend.
type withBackend func(fn func(cmd *cobra.Command, b backend, args []string) error) func(*cobra.Command, []string) error

// workspaceOf returns the workspace a command acts on, and whether the user
// named it (--workspace or $QH_WORKSPACE) rather than taking the default.
func workspaceOf(cmd *cobra.Command) (string, bool) {
	ws, _ := cmd.Flags().GetString("workspace")
	if cmd.Flags().Changed("workspace") {
		return ws, true
	}
	if v := os.Getenv("QH_WORKSPACE"); v != "" {
		return v, true
	}
	return ws, false
}

// refuseRemote stops a local-only command from ignoring --server.
func refuseRemote(cmd *cobra.Command) error {
	if s, _ := cmd.Flags().GetString("server"); s != "" {
		return qerr.New(qerr.Invalid, cmd.CommandPath()+" runs locally only").WithHint("Remove --server")
	}
	return nil
}

func newRoot(version string) *cobra.Command {
	var home string
	root := &cobra.Command{
		Use:           "qh",
		Short:         "Token-efficient database server for AI agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&home, "home", "", "data directory (default $QH_HOME, else $XDG_CONFIG_HOME/qh or ~/.config/qh)")
	pf.String("workspace", store.DefaultWorkspace, "workspace to act on (default $QH_WORKSPACE, else default)")
	var server, key string
	pf.StringVar(&server, "server", "", "qh server URL; commands then use its REST API (default $QH_SERVER)")
	pf.StringVar(&key, "key", "", "API key for --server (default $QH_KEY)")
	with := func(fn func(cmd *cobra.Command, b backend, args []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			srv := server
			if srv == "" {
				srv = os.Getenv("QH_SERVER")
			}
			if srv != "" {
				k := key
				if k == "" {
					k = os.Getenv("QH_KEY")
				}
				header := ""
				if ws, explicit := workspaceOf(cmd); explicit {
					header = ws // sent only when named, so admin keys never act on default by accident
				}
				c, err := client.New(srv, k, header)
				if err != nil {
					return err
				}
				return fn(cmd, c, args)
			}
			ws, _ := workspaceOf(cmd)
			dir, err := homeDir(home)
			if err != nil {
				return err
			}
			a, err := openApp(dir)
			if err != nil {
				return err
			}
			defer a.Close()
			return fn(cmd, &localBackend{a: a, ws: ws}, args)
		}
	}
	root.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				fmt.Fprintln(cmd.OutOrStdout(), version)
				return nil
			},
		},
		mcpCmd(&home, version),
		connCmd(with),
		queryCmd(with),
		schemaCmd(with),
		logCmd(with),
		serveCmd(&home, version),
		workspaceCmd(with),
		keyCmd(with),
		masterkeyCmd(&home),
	)
	return root
}
