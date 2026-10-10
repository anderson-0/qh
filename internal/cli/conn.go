package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

const ws = store.DefaultWorkspace

func connCmd(with withApp) *cobra.Command {
	c := &cobra.Command{Use: "conn", Short: "Manage database connections"}
	c.AddCommand(connAddCmd(with), connListCmd(with), connShowCmd(with), connEditCmd(with), connRemoveCmd(with), connTestCmd(with))
	return c
}

type connFlags struct {
	engine, dsn, mode, description string
	maxRows, poolSize              int
	timeout                        time.Duration
	namespaces                     []string
	skipTest                       bool
}

func (f *connFlags) register(cmd *cobra.Command, withEngine bool) {
	fl := cmd.Flags()
	if withEngine {
		fl.StringVar(&f.engine, "engine", "", "postgres, mysql (alias mariadb) or sqlite (required)")
	}
	fl.StringVar(&f.dsn, "dsn", "", "connection string, or - to read it from stdin")
	fl.StringVar(&f.mode, "mode", "ro", "ro (read-only) or rw (read-write)")
	fl.StringVar(&f.description, "description", "", "what this database holds; shown to agents")
	fl.IntVar(&f.maxRows, "max-rows", 0, "row cap per query (default 100, max 10000)")
	fl.DurationVar(&f.timeout, "timeout", 0, "query timeout (default 30s, max 5m)")
	fl.IntVar(&f.poolSize, "pool-size", 0, "open database connections (default 5, max 50)")
	fl.StringSliceVar(&f.namespaces, "namespaces", nil, "only show these schemas to agents (comma-separated)")
	fl.BoolVar(&f.skipTest, "skip-test", false, "save without connecting first")
}

// readDSN returns v, or the first line of stdin when v is "-" (keeps passwords out of shell history).
func readDSN(cmd *cobra.Command, v string) (string, error) {
	if v != "-" {
		return v, nil
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func connAddCmd(with withApp) *cobra.Command {
	var f connFlags
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a connection (tests it first unless --skip-test)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			if f.engine == "" {
				return qerr.New(qerr.Invalid, "--engine is required")
			}
			if f.dsn == "" {
				return qerr.New(qerr.Invalid, "--dsn is required")
			}
			dsn, err := readDSN(cmd, f.dsn)
			if err != nil {
				return err
			}
			info, tr, err := a.svc.CreateConnection(cmd.Context(), ws, core.ConnInput{
				Name: args[0], Engine: f.engine, DSN: dsn, Description: f.description, Mode: engine.Mode(f.mode),
				MaxRows: f.maxRows, TimeoutMS: int(f.timeout.Milliseconds()), PoolSize: f.poolSize,
				AllowedNamespaces: f.namespaces,
			}, !f.skipTest)
			if err != nil {
				return err
			}
			out := fmt.Sprintf("added %s (%s, %s)", info.Name, info.Engine, info.Mode)
			if tr != nil {
				out += fmt.Sprintf(" · %s · connected in %dms", tr.ServerVersion, tr.Connect.Milliseconds())
			}
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		}),
	}
	f.register(cmd, true)
	return cmd
}

func target(c *core.ConnInfo) string {
	if c.Host == "" {
		return c.Database
	}
	t := c.Host
	if c.Port != 0 {
		t += ":" + strconv.Itoa(c.Port)
	}
	if c.Database != "" {
		t += "/" + c.Database
	}
	return t
}

func connListCmd(with withApp) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List connections",
		Args:    cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, a *app, _ []string) error {
			list, err := a.svc.ListConnections(cmd.Context(), ws)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no connections; add one with: qh conn add <name> --engine postgres --dsn -")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tENGINE\tMODE\tTARGET\tDESCRIPTION")
			for _, c := range list {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.Engine, c.Mode, target(c), c.Description)
			}
			return w.Flush()
		}),
	}
}

func connShowCmd(with withApp) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a connection (password hidden)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			c, err := a.svc.GetConnection(cmd.Context(), ws, args[0])
			if err != nil {
				return err
			}
			ns := "(all)"
			if len(c.AllowedNamespaces) > 0 {
				ns = strings.Join(c.AllowedNamespaces, ", ")
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "name: %s\nengine: %s\nmode: %s\ndsn: %s\n", c.Name, c.Engine, c.Mode, c.DSN)
			fmt.Fprintf(out, "description: %s\nmax_rows: %d\ntimeout: %s\npool_size: %d\nnamespaces: %s\n",
				c.Description, c.MaxRows, time.Duration(c.TimeoutMS)*time.Millisecond, c.PoolSize, ns)
			fmt.Fprintf(out, "version: %d\ncreated: %s\nupdated: %s\n", c.Version,
				c.CreatedAt.Format(time.RFC3339), c.UpdatedAt.Format(time.RFC3339))
			return nil
		}),
	}
}

func connEditCmd(with withApp) *cobra.Command {
	var f connFlags
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a connection; only the flags you pass are changed",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			var p core.ConnPatch
			fl := cmd.Flags()
			if fl.Changed("dsn") {
				dsn, err := readDSN(cmd, f.dsn)
				if err != nil {
					return err
				}
				p.DSN = &dsn
			}
			if fl.Changed("mode") {
				m := engine.Mode(f.mode)
				p.Mode = &m
			}
			if fl.Changed("description") {
				p.Description = &f.description
			}
			if fl.Changed("max-rows") {
				p.MaxRows = &f.maxRows
			}
			if fl.Changed("timeout") {
				ms := int(f.timeout.Milliseconds())
				p.TimeoutMS = &ms
			}
			if fl.Changed("pool-size") {
				p.PoolSize = &f.poolSize
			}
			if fl.Changed("namespaces") {
				p.AllowedNamespaces = &f.namespaces
			}
			info, _, err := a.svc.UpdateConnection(cmd.Context(), ws, args[0], p, !f.skipTest)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated %s (version %d)\n", info.Name, info.Version)
			return nil
		}),
	}
	f.register(cmd, false)
	return cmd
}

func connRemoveCmd(with withApp) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove a connection",
		Args:    cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			if err := a.svc.DeleteConnection(cmd.Context(), ws, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return nil
		}),
	}
}

func connTestCmd(with withApp) *cobra.Command {
	return &cobra.Command{
		Use:   "test <name>",
		Short: "Connect with a stored connection's settings",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			tr, err := a.svc.TestConnection(cmd.Context(), ws, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok · %s · %dms\n", tr.ServerVersion, tr.Connect.Milliseconds())
			return nil
		}),
	}
}
