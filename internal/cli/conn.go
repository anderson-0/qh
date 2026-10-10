package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func connCmd(with withBackend) *cobra.Command {
	c := &cobra.Command{Use: "conn", Short: "Manage database connections"}
	c.AddCommand(connAddCmd(with), connListCmd(with), connShowCmd(with), connEditCmd(with), connRemoveCmd(with), connTestCmd(with))
	return c
}

type connFlags struct {
	engine, dsn, mode, description string
	host, database, user           string
	port                           int
	passwordStdin                  bool
	params                         map[string]string
	maxRows, poolSize              int
	timeout                        time.Duration
	namespaces                     []string
	skipTest                       bool
	tlsMode, tlsCA, tlsCert        string
	tlsKey, tlsServerName          string
	noTLS                          bool
}

func (f *connFlags) register(cmd *cobra.Command, create bool) {
	fl := cmd.Flags()
	if create {
		fl.StringVar(&f.engine, "engine", "", "postgres, mysql (alias mariadb) or sqlite (required)")
		fl.StringVar(&f.host, "host", "", "database host, instead of --dsn")
		fl.IntVar(&f.port, "port", 0, "database port (default: the engine's)")
		fl.StringVar(&f.database, "database", "", "database name, or the file path for sqlite")
		fl.StringVar(&f.user, "user", "", "database user")
		fl.BoolVar(&f.passwordStdin, "password-stdin", false, "read the password from the first line of stdin")
		fl.StringToStringVar(&f.params, "param", nil, "extra connection setting key=value (repeatable)")
	} else {
		fl.BoolVar(&f.noTLS, "no-tls", false, "remove the TLS settings")
	}
	fl.StringVar(&f.dsn, "dsn", "", "connection string, or - to read it from stdin")
	fl.StringVar(&f.mode, "mode", "ro", "ro (read-only) or rw (read-write)")
	fl.StringVar(&f.description, "description", "", "what this database holds; shown to agents")
	fl.IntVar(&f.maxRows, "max-rows", 0, "row cap per query (default 100, max 10000)")
	fl.DurationVar(&f.timeout, "timeout", 0, "query timeout (default 30s, max 5m)")
	fl.IntVar(&f.poolSize, "pool-size", 0, "open database connections (default 5, max 50)")
	fl.StringSliceVar(&f.namespaces, "namespaces", nil, "only show these schemas to agents (comma-separated)")
	fl.BoolVar(&f.skipTest, "skip-test", false, "save without connecting first")
	fl.StringVar(&f.tlsMode, "tls-mode", "", "disable, require, verify-ca or verify-full; replaces TLS settings in the connection string")
	fl.StringVar(&f.tlsCA, "tls-ca", "", "file with PEM CA certificates to trust")
	fl.StringVar(&f.tlsCert, "tls-cert", "", "file with a PEM client certificate")
	fl.StringVar(&f.tlsKey, "tls-key", "", "file with the PEM private key for --tls-cert")
	fl.StringVar(&f.tlsServerName, "tls-server-name", "", "name the server certificate must carry (verify-full)")
}

// tls returns the TLS settings the flags describe, or nil when --tls-mode is absent.
func (f *connFlags) tls(cmd *cobra.Command) (*engine.TLS, error) {
	fl := cmd.Flags()
	if !fl.Changed("tls-mode") {
		for _, n := range []string{"tls-ca", "tls-cert", "tls-key", "tls-server-name"} {
			if fl.Changed(n) {
				return nil, qerr.New(qerr.Invalid, "--"+n+" needs --tls-mode")
			}
		}
		return nil, nil
	}
	t := &engine.TLS{Mode: f.tlsMode, ServerName: f.tlsServerName}
	for _, file := range []struct {
		path string
		dst  *string
	}{{f.tlsCA, &t.CA}, {f.tlsCert, &t.Cert}, {f.tlsKey, &t.Key}} {
		if file.path == "" {
			continue
		}
		b, err := os.ReadFile(file.path)
		if err != nil {
			return nil, qerr.New(qerr.Invalid, "cannot read "+file.path)
		}
		*file.dst = string(b)
	}
	return t, nil
}

// readLine returns the first line of stdin.
func readLine(cmd *cobra.Command) (string, error) {
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readDSN returns v, or the first line of stdin when v is "-" (keeps passwords out of shell history).
func readDSN(cmd *cobra.Command, v string) (string, error) {
	if v != "-" {
		return v, nil
	}
	return readLine(cmd)
}

func connAddCmd(with withBackend) *cobra.Command {
	var f connFlags
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a connection (tests it first unless --skip-test)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			if f.engine == "" {
				return qerr.New(qerr.Invalid, "--engine is required")
			}
			if f.dsn == "" && f.host == "" && f.database == "" {
				return qerr.New(qerr.Invalid, "--dsn is required").
					WithHint("Or give --host (--database for sqlite) with --port, --database, --user, --password-stdin and --param")
			}
			if f.dsn == "-" && f.passwordStdin {
				return qerr.New(qerr.Invalid, "--dsn - and --password-stdin cannot both read stdin")
			}
			in := core.ConnInput{
				Name: args[0], Engine: f.engine, Host: f.host, Port: f.port, Database: f.database, Username: f.user,
				Params: f.params, Description: f.description, Mode: engine.Mode(f.mode), MaxRows: f.maxRows,
				TimeoutMS: int(f.timeout.Milliseconds()), PoolSize: f.poolSize, AllowedNamespaces: f.namespaces,
			}
			var err error
			if f.dsn != "" {
				if in.DSN, err = readDSN(cmd, f.dsn); err != nil {
					return err
				}
			}
			if f.passwordStdin {
				if in.Password, err = readLine(cmd); err != nil {
					return err
				}
			}
			if in.TLS, err = f.tls(cmd); err != nil {
				return err
			}
			info, tr, err := b.CreateConnection(cmd.Context(), in, !f.skipTest)
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

func tlsSummary(t *engine.TLSInfo) string {
	if t == nil {
		return "none (the connection string's settings apply)"
	}
	var parts []string
	if t.CA {
		parts = append(parts, "ca")
	}
	if t.ClientCert {
		parts = append(parts, "client cert")
	}
	if t.ServerName != "" {
		parts = append(parts, "server name "+t.ServerName)
	}
	if len(parts) == 0 {
		return t.Mode
	}
	return t.Mode + " (" + strings.Join(parts, ", ") + ")"
}

func connListCmd(with withBackend) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List connections",
		Args:    cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, b backend, _ []string) error {
			list, err := b.ListConnections(cmd.Context())
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

func connShowCmd(with withBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a connection (password hidden)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			c, err := b.GetConnection(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			ns := "(all)"
			if len(c.AllowedNamespaces) > 0 {
				ns = strings.Join(c.AllowedNamespaces, ", ")
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "name: %s\nengine: %s\nmode: %s\ndsn: %s\ntls: %s\n", c.Name, c.Engine, c.Mode, c.DSN, tlsSummary(c.TLS))
			fmt.Fprintf(out, "description: %s\nmax_rows: %d\ntimeout: %s\npool_size: %d\nnamespaces: %s\n",
				c.Description, c.MaxRows, time.Duration(c.TimeoutMS)*time.Millisecond, c.PoolSize, ns)
			fmt.Fprintf(out, "version: %d\ncreated: %s\nupdated: %s\n", c.Version,
				c.CreatedAt.Format(time.RFC3339), c.UpdatedAt.Format(time.RFC3339))
			return nil
		}),
	}
}

func connEditCmd(with withBackend) *cobra.Command {
	var f connFlags
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a connection; only the flags you pass are changed",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
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
			p.ClearTLS = f.noTLS
			var err error
			if p.TLS, err = f.tls(cmd); err != nil {
				return err
			}
			info, _, err := b.UpdateConnection(cmd.Context(), args[0], p, !f.skipTest)
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

func connRemoveCmd(with withBackend) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove a connection",
		Args:    cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			if err := b.DeleteConnection(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return nil
		}),
	}
}

func connTestCmd(with withBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "test <name>",
		Short: "Connect with a stored connection's settings",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			tr, err := b.TestConnection(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok · %s · %dms\n", tr.ServerVersion, tr.Connect.Milliseconds())
			return nil
		}),
	}
}
