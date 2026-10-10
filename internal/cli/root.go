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

// withApp wraps a command so it runs with an opened app.
type withApp func(fn func(cmd *cobra.Command, a *app, args []string) error) func(*cobra.Command, []string) error

func newRoot(version string) *cobra.Command {
	var home string
	root := &cobra.Command{
		Use:           "qh",
		Short:         "Token-efficient database server for AI agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&home, "home", "", "data directory (default $QH_HOME, else $XDG_CONFIG_HOME/qh or ~/.config/qh)")
	with := func(fn func(cmd *cobra.Command, a *app, args []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			dir, err := homeDir(home)
			if err != nil {
				return err
			}
			a, err := openApp(dir)
			if err != nil {
				return err
			}
			defer a.Close()
			return fn(cmd, a, args)
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
		mcpCmd(with, version),
		masterkeyCmd(&home),
		connCmd(with),
		queryCmd(with),
		schemaCmd(with),
		serveCmd(&home, version),
		workspaceCmd(with),
		keyCmd(with),
	)
	return root
}
