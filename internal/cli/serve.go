package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/httpapi"
	"github.com/anderson-0/qh/internal/netguard"
	"github.com/anderson-0/qh/internal/qerr"
)

const localAddr = "127.0.0.1:8765"

func serveCmd(home *string, version string) *cobra.Command {
	var listen, dataDir, sqliteRoot string
	var allow, deny []string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve MCP over HTTP and the REST API (local mode on " + localAddr + ", or server mode with --http)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			server := listen != ""
			var opts core.Options
			addr := localAddr
			if server {
				addr = listen
				if os.Getenv("QH_MASTER_KEY") == "" {
					return qerr.New(qerr.Invalid, "server mode needs QH_MASTER_KEY").
						WithHint("Set it to a base64-encoded 32-byte key, e.g. openssl rand -base64 32")
				}
				if names := clearPGEnv(); len(names) > 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "ignoring %s in server mode\n", strings.Join(names, ", "))
				}
				policy, err := netguard.New(allow, deny)
				if err != nil {
					return err
				}
				opts.Dial = policy.DialContext
				if sqliteRoot == "" {
					opts.SQLiteDisabled = true
				} else if opts.SQLiteRoot, err = filepath.Abs(sqliteRoot); err != nil {
					return err
				}
			} else if len(allow)+len(deny) > 0 || sqliteRoot != "" {
				return qerr.New(qerr.Invalid, "--allow-cidr, --deny-cidr and --sqlite-root apply to server mode only").WithHint("Add --http ADDR")
			}
			dir := dataDir
			if dir == "" {
				var err error
				if dir, err = homeDir(*home); err != nil {
					return err
				}
			}
			a, err := openAppWith(dir, opts)
			if err != nil {
				return err
			}
			defer a.Close()
			stderr := cmd.ErrOrStderr()
			cfg := httpapi.Config{Service: a.svc, Store: a.st, Version: version, Logger: slog.New(slog.NewJSONHandler(stderr, nil))}
			mode := "local"
			if server {
				mode = "server"
				cfg.Auth = auth.NewAuthenticator(a.st)
				tok, created, err := auth.Bootstrap(cmd.Context(), a.st, os.Getenv("QH_BOOTSTRAP_ADMIN_KEY"))
				if err != nil {
					return err
				}
				if tok != "" {
					fmt.Fprintf(stderr, "admin API key (shown once): %s\n", tok)
				} else if created {
					fmt.Fprintln(stderr, "admin API key taken from QH_BOOTSTRAP_ADMIN_KEY")
				}
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("cannot listen on %s: %w", addr, err)
			}
			fmt.Fprintf(stderr, "qh listening on http://%s (%s mode)\n", ln.Addr(), mode)
			srv := &http.Server{Handler: httpapi.New(cfg), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
			ctx := cmd.Context()
			a.pools.Start(ctx)
			done := make(chan error, 1)
			go func() { done <- srv.Serve(ln) }()
			select {
			case err := <-done:
				return err
			case <-ctx.Done():
				sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := srv.Shutdown(sctx); err != nil {
					return err
				}
				<-done
				return nil
			}
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&listen, "http", "", "server mode: listen on this address (e.g. :8080); requires QH_MASTER_KEY and API keys")
	fl.StringVar(&dataDir, "data-dir", "", "data directory (default: the same as --home)")
	fl.StringVar(&sqliteRoot, "sqlite-root", "", "server mode: allow SQLite files under DIR/<workspace>/ (SQLite is disabled otherwise)")
	fl.StringSliceVar(&allow, "allow-cidr", nil, "server mode: allow database addresses in this range, even if denied by default (repeatable)")
	fl.StringSliceVar(&deny, "deny-cidr", nil, "server mode: also refuse database addresses in this range (repeatable)")
	return cmd
}

// clearPGEnv unsets every PG* environment variable and returns their names.
// In server mode the Postgres driver must not take a password, service,
// host or file from the operator's environment for a tenant's connection.
func clearPGEnv() []string {
	var names []string
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "PG") {
			os.Unsetenv(name)
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
