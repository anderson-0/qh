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
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/secretref"
)

const localAddr = "127.0.0.1:8765"

// queryLogFlags are the query-log settings of qh serve and qh mcp.
type queryLogFlags struct {
	off  bool
	keep string
}

func (f *queryLogFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.off, "no-query-log", false, "do not log query calls (existing entries still expire)")
	cmd.Flags().StringVar(&f.keep, "query-log-retention", "30d", "how long to keep logged query calls, e.g. 30d or 72h")
}

func (f *queryLogFlags) retention() (time.Duration, error) {
	d, err := auth.ParseExpiry(f.keep)
	if err != nil || d <= 0 {
		return 0, qerr.New(qerr.Invalid, "--query-log-retention must be a positive duration such as 30d or 72h")
	}
	return d, nil
}

// pruneQueryLog deletes expired query-log entries now, then hourly until ctx ends.
func pruneQueryLog(ctx context.Context, svc *core.Service, retention time.Duration) {
	go func() {
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			_, _ = svc.PruneQueryLog(ctx, retention)
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

// oldMasterKeys parses QH_MASTER_KEY_OLD: comma-separated base64 keys that
// can still open data sealed before a rotation.
func oldMasterKeys(v string) ([][]byte, error) {
	var out [][]byte
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		k, err := secret.ParseKey(s)
		if err != nil {
			return nil, qerr.New(qerr.Invalid, "QH_MASTER_KEY_OLD must hold comma-separated base64 32-byte keys")
		}
		out = append(out, k)
	}
	return out, nil
}

func serveCmd(home *string, version string) *cobra.Command {
	var listen, dataDir, sqliteRoot string
	var allow, deny, allowRefs []string
	var denySSH bool
	var lf queryLogFlags
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve MCP over HTTP and the REST API (local mode on " + localAddr + ", or server mode with --http)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := refuseRemote(cmd); err != nil {
				return err
			}
			retention, err := lf.retention()
			if err != nil {
				return err
			}
			server := listen != ""
			logger := slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), nil))
			opts := core.Options{NoQueryLog: lf.off, Logger: logger}
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
				if opts, err = serverOptions(opts, allow, deny, allowRefs, denySSH, sqliteRoot); err != nil {
					return err
				}
			} else if len(allow)+len(deny)+len(allowRefs) > 0 || sqliteRoot != "" || denySSH {
				return qerr.New(qerr.Invalid, "--allow-cidr, --deny-cidr, --sqlite-root, --allow-secret-ref and --deny-ssh apply to server mode only").WithHint("Add --http ADDR")
			}
			dir := dataDir
			if dir == "" {
				var err error
				if dir, err = homeDir(*home); err != nil {
					return err
				}
			}
			a, err := openAppWith(cmd.Context(), dir, opts)
			if err != nil {
				return err
			}
			defer a.Close()
			// Cancelled before a.Close runs (defers are last in, first out), so
			// background work never outlives the store.
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			stderr := cmd.ErrOrStderr()
			cfg := httpapi.Config{Service: a.svc, Store: a.st, Version: version, Logger: logger}
			mode := "local"
			if server {
				mode = "server"
				cfg.Auth = auth.NewAuthenticator(a.st)
				tok, created, err := auth.Bootstrap(ctx, a.st, os.Getenv("QH_BOOTSTRAP_ADMIN_KEY"))
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
			a.pools.Start(ctx)
			a.svc.Start(ctx)
			pruneQueryLog(ctx, a.svc, retention) // retention applies even when logging is off
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
	lf.register(cmd)
	fl := cmd.Flags()
	fl.StringVar(&listen, "http", "", "server mode: listen on this address (e.g. :8080); requires QH_MASTER_KEY and API keys")
	fl.StringVar(&dataDir, "data-dir", "", "data directory (default: the same as --home)")
	fl.StringVar(&sqliteRoot, "sqlite-root", "", "server mode: allow SQLite files under DIR/<workspace>/ (SQLite is disabled otherwise)")
	fl.StringSliceVar(&allow, "allow-cidr", nil, "server mode: allow database addresses in this range, even if denied by default (repeatable)")
	fl.StringSliceVar(&deny, "deny-cidr", nil, "server mode: also refuse database addresses in this range (repeatable)")
	fl.StringArrayVar(&allowRefs, "allow-secret-ref", nil, "server mode: allow secret references matching this pattern (repeatable; * is a wildcard), e.g. env:QH_TENANT_*")
	fl.BoolVar(&denySSH, "deny-ssh", false, "server mode: refuse connections that use an SSH tunnel")
	return cmd
}

// serverOptions adds the server-mode restrictions to opts: the address policy
// (for dialling and for SSH bastions and tunnel targets), the allowed secret
// references, the SSH switch and the SQLite root.
func serverOptions(opts core.Options, allow, deny, allowRefs []string, denySSH bool, sqliteRoot string) (core.Options, error) {
	policy, err := netguard.New(allow, deny)
	if err != nil {
		return opts, err
	}
	opts.Dial = policy.DialContext
	opts.CheckTarget = policy.Allowed
	for _, p := range allowRefs {
		if err := secretref.CheckPattern(p); err != nil {
			return opts, err
		}
	}
	opts.SecretRefs, opts.DenySSH = allowRefs, denySSH
	if sqliteRoot == "" {
		opts.SQLiteDisabled = true
	} else if opts.SQLiteRoot, err = filepath.Abs(sqliteRoot); err != nil {
		return opts, err
	}
	return opts, nil
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
