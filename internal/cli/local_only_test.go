package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Catches local commands ignoring an environment-selected server, including
// when an explicitly empty --server falls back to the environment.
func TestLocalOnlyRejectsServerSelection(t *testing.T) {
	t.Setenv("QH_DSN", "")
	t.Setenv("QH_WORKSPACE", "")
	const serverURL = "http://127.0.0.1:1"
	const storeURL = "postgres://qh@127.0.0.1:1/qh"
	commands := []struct {
		name string
		args []string
		path string
	}{
		// Invalid retention prevents an unfixed serve from starting a listener.
		{"mcp", []string{"mcp", "--query-log-retention", "0"}, "qh mcp"},
		{"serve", []string{"serve", "--query-log-retention", "0"}, "qh serve"},
		{"copy", []string{"store", "copy"}, "qh store copy"},
		{"rotate", []string{"masterkey", "rotate"}, "qh masterkey rotate"},
	}
	sources := []struct {
		name  string
		env   string
		flags []string
	}{
		{"flag", "", []string{"--server", serverURL}},
		{"environment", serverURL, nil},
		{"both", serverURL, []string{"--server", serverURL}},
		{"empty_flag_falls_back", serverURL, []string{"--server="}},
	}
	stores := []struct {
		name  string
		env   string
		flags []string
	}{
		{"local_store", "", nil},
		{"store_flag", "", []string{"--store", storeURL}},
		{"store_environment", storeURL, nil},
	}
	for _, command := range commands {
		for _, source := range sources {
			for _, st := range stores {
				t.Run(command.name+"/"+source.name+"/"+st.name, func(t *testing.T) {
					t.Setenv("QH_SERVER", source.env)
					t.Setenv("QH_STORE_DSN", st.env)
					home := t.TempDir()
					args := append([]string{}, source.flags...)
					args = append(args, st.flags...)
					args = append(args, command.args...)
					out, errOut, code := run(t, home, "", args...)
					if code != 1 {
						t.Errorf("expected rejection, got exit code %d", code)
					}
					if out != "" {
						t.Error("rejected command wrote to stdout")
					}
					if st.env != "" || len(st.flags) != 0 {
						if !strings.HasPrefix(errOut, "error[invalid]: --store and --server cannot be used together") ||
							!strings.Contains(errOut, "QH_SERVER") || !strings.Contains(errOut, "QH_STORE_DSN") {
							t.Error("expected store/server conflict before command validation")
						}
					} else if !strings.HasPrefix(errOut, "error[invalid]: "+command.path+" runs locally only") ||
						!strings.Contains(errOut, "Remove --server and QH_SERVER") {
						t.Error("expected local-only rejection with flag and environment guidance")
					}
					if strings.Contains(errOut, serverURL) || strings.Contains(errOut, storeURL) {
						t.Error("error disclosed a supplied server or store value")
					}
					for _, name := range []string{"qh.db", "master.key"} {
						if _, err := os.Stat(filepath.Join(home, name)); !errors.Is(err, os.ErrNotExist) {
							t.Errorf("rejected command created %s or file absence could not be verified", name)
						}
					}
				})
			}
		}
	}
}

// Catches a refactor changing flag precedence or letting an empty flag disable
// the environment-selected remote backend.
func TestServerSelectionRemotePrecedence(t *testing.T) {
	t.Setenv("QH_DSN", "")
	t.Setenv("QH_STORE_DSN", "")
	serverURL, admin := remoteServer(t)
	for _, tc := range []struct {
		name  string
		env   string
		flags []string
	}{
		{"flag_over_environment", "http://127.0.0.1:1", []string{"--server", serverURL}},
		{"environment", serverURL, nil},
		{"empty_flag_falls_back", serverURL, []string{"--server="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("QH_SERVER", tc.env)
			home := t.TempDir()
			args := append([]string{}, tc.flags...)
			args = append(args, "--key", admin, "--workspace", "default", "conn", "ls")
			out, errOut, code := run(t, home, "", args...)
			if code != 0 || !strings.HasPrefix(out, "no connections") || errOut != "" {
				t.Fatal("connection listing did not use the selected remote backend")
			}
			for _, name := range []string{"qh.db", "master.key"} {
				if _, err := os.Stat(filepath.Join(home, name)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("remote command created %s or file absence could not be verified", name)
				}
			}
		})
	}
}

// Catches server-environment enforcement leaking into informational commands.
func TestServerSelectionAllowsVersionAndHelp(t *testing.T) {
	t.Setenv("QH_SERVER", "http://127.0.0.1:1")
	t.Setenv("QH_STORE_DSN", "postgres://qh@127.0.0.1:1/qh")
	for _, args := range [][]string{{"version"}, {"--version"}, {"serve", "--help"}} {
		out, errOut, code := run(t, t.TempDir(), "", args...)
		if code != 0 || out == "" || errOut != "" {
			t.Error("informational command was blocked by server/store selection")
		}
	}
}
