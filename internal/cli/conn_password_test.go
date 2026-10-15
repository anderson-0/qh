package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
)

// Capture the CLI's patch before the backend handles redaction and persistence.
// An accidental GET/rewrite or a missing/empty password pointer must fail here.
type passwordBackend struct {
	backend
	patch                  core.ConnPatch
	input                  core.ConnInput
	updates, creates, gets int
	test                   bool
}

func (b *passwordBackend) UpdateConnection(_ context.Context, name string, p core.ConnPatch, test bool) (*core.ConnInfo, *core.TestResult, error) {
	b.updates++
	b.patch, b.test = p, test
	return &core.ConnInfo{Name: name, Engine: "postgres", Mode: engine.Mode("ro"), Version: 2}, nil, nil
}

func (b *passwordBackend) CreateConnection(_ context.Context, in core.ConnInput, test bool) (*core.ConnInfo, *core.TestResult, error) {
	b.creates++
	b.input, b.test = in, test
	return &core.ConnInfo{Name: in.Name, Engine: in.Engine, Mode: in.Mode, Version: 1}, nil, nil
}

func (b *passwordBackend) GetConnection(context.Context, string) (*core.ConnInfo, error) {
	b.gets++
	return nil, errors.New("edit must not fetch redacted connection details")
}

func passwordCommand(b *passwordBackend, operation string) *cobra.Command {
	with := func(fn func(*cobra.Command, backend, []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error { return fn(cmd, b, args) }
	}
	if operation == "add" {
		return connAddCmd(with)
	}
	return connEditCmd(with)
}

type passwordReader struct {
	io.Reader
	reads int
	err   error
}

func (r *passwordReader) Read(p []byte) (int, error) {
	r.reads++
	if r.err != nil {
		return 0, r.err
	}
	return r.Reader.Read(p)
}

func TestConnEditPasswordStdin(t *testing.T) {
	long := strings.Repeat("x", 70*1024)
	for _, tc := range []struct {
		name, stdin, want string
		flags             []string
		present, skip     bool
	}{
		{name: "omitted", stdin: "unused"},
		{name: "false", stdin: "unused", flags: []string{"--password-stdin=false"}},
		{name: "true first line trimmed", stdin: " \treplacement \r\nignored\n", want: "replacement", flags: []string{"--password-stdin"}, present: true},
		{name: "explicit true", stdin: "replacement", want: "replacement", flags: []string{"--password-stdin=true"}, present: true},
		{name: "no newline", stdin: "replacement", want: "replacement", flags: []string{"--password-stdin"}, present: true, skip: true},
		{name: "immediate EOF", flags: []string{"--password-stdin"}, present: true},
		{name: "blank", stdin: "\nignored", flags: []string{"--password-stdin"}, present: true},
		{name: "whitespace", stdin: " \t\r\n", flags: []string{"--password-stdin"}, present: true},
		{name: "above scanner limit", stdin: long + "\nignored", want: long, flags: []string{"--password-stdin"}, present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &passwordBackend{}
			cmd := passwordCommand(b, "edit")
			reader := &passwordReader{Reader: strings.NewReader(tc.stdin)}
			var out, errOut bytes.Buffer
			cmd.SetIn(reader)
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			args := append([]string{"shop"}, tc.flags...)
			if tc.skip {
				args = append(args, "--skip-test")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("edit failed: %v", err)
			}
			if b.updates != 1 || b.gets != 0 || b.test == tc.skip {
				t.Fatal("wrong backend call or test policy")
			}
			if (b.patch.Password != nil) != tc.present {
				t.Fatal("password presence changed")
			}
			if tc.present && *b.patch.Password != tc.want {
				t.Fatal("stdin password was not forwarded")
			}
			if !tc.present && reader.reads != 0 {
				t.Fatal("inactive password flag consumed stdin")
			}
			if out.String() != "updated shop (version 2)\n" || errOut.Len() != 0 {
				t.Fatal("unexpected output or password disclosure")
			}
		})
	}
	t.Run("read error stops update", func(t *testing.T) {
		b := &passwordBackend{}
		cmd := passwordCommand(b, "edit")
		wantErr := errors.New("fixture read failed")
		cmd.SetIn(&passwordReader{err: wantErr})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"shop", "--password-stdin"})
		if err := cmd.Execute(); !errors.Is(err, wantErr) || b.updates != 0 || b.gets != 0 {
			t.Fatal("read error did not stop update")
		}
	})
}

func TestConnPasswordStdinConflicts(t *testing.T) {
	readers := [][]string{{"--dsn", "-"}, {"--password-stdin"}, {"--ssh-password-stdin"}, {"--ssh-passphrase-stdin"}}
	for _, operation := range []string{"add", "edit"} {
		for i := range readers {
			for j := i + 1; j < len(readers); j++ {
				t.Run(operation+"/"+readers[i][0]+"/"+readers[j][0], func(t *testing.T) {
					args := []string{"shop"}
					if operation == "add" {
						args = append(args, "--engine", "postgres", "--host", "localhost")
					}
					args = append(args, readers[i]...)
					args = append(args, readers[j]...)
					b := &passwordBackend{}
					cmd := passwordCommand(b, operation)
					reader := &passwordReader{Reader: strings.NewReader("unused\n")}
					cmd.SetIn(reader)
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					cmd.SetArgs(args)
					if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "only one flag can read stdin") {
						t.Fatal("missing stdin conflict")
					}
					if reader.reads != 0 || b.updates != 0 || b.creates != 0 || b.gets != 0 {
						t.Fatal("conflict read stdin or called backend")
					}
					// Exercise root routing with a counting reader too.
					reader = &passwordReader{Reader: strings.NewReader("unused\n")}
					var out, errOut bytes.Buffer
					rootArgs := append([]string{"--home", t.TempDir(), "conn", operation}, args...)
					if code := Execute("test", rootArgs, reader, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "only one flag can read stdin") || reader.reads != 0 {
						t.Fatal("root command did not reject before reading")
					}
				})
			}
		}
	}
}

func TestConnPasswordStdinReadErrorExecute(t *testing.T) {
	home := t.TempDir()
	if _, _, code := run(t, home, "", "conn", "add", "shop", "--engine", "postgres", "--host", "localhost", "--skip-test"); code != 0 {
		t.Fatal("create fixture failed")
	}
	for _, operation := range []string{"add", "edit"} {
		t.Run(operation, func(t *testing.T) {
			name := "shop"
			args := []string{"--home", home, "conn", operation}
			if operation == "add" {
				name = "new"
			}
			args = append(args, name, "--password-stdin", "--skip-test")
			if operation == "add" {
				args = append(args, "--engine", "postgres", "--host", "localhost")
			}
			reader := &passwordReader{err: errors.New("fixture read failed")}
			var out, errOut bytes.Buffer
			if code := Execute("test", args, reader, &out, &errOut); code != 1 || reader.reads == 0 || !strings.Contains(errOut.String(), "fixture read failed") || out.Len() != 0 {
				t.Fatal("read error was not returned")
			}
			if out, _, code := run(t, home, "", "conn", "show", "shop"); code != 0 || !strings.Contains(out, "version: 1\n") {
				t.Fatal("read error changed existing connection")
			}
			if _, _, code := run(t, home, "", "conn", "show", "new"); code != 1 {
				t.Fatal("read error created a connection")
			}
		})
	}
}

func TestConnPasswordStdinInactiveFlags(t *testing.T) {
	for _, operation := range []string{"add", "edit"} {
		t.Run(operation, func(t *testing.T) {
			b := &passwordBackend{}
			cmd := passwordCommand(b, operation)
			args := []string{"shop", "--dsn", "-", "--password-stdin=false", "--ssh-host", "jump@localhost", "--ssh-password-stdin=false", "--ssh-passphrase-stdin=false"}
			if operation == "add" {
				args = append(args, "--engine", "postgres")
			}
			cmd.SetArgs(args)
			cmd.SetIn(strings.NewReader("postgres://app@localhost/db\n"))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("false flags counted as stdin readers: %v", err)
			}
			if !b.test {
				t.Fatal("default connection test was disabled")
			}
			if operation == "edit" && (b.patch.Password != nil || b.patch.DSN == nil || *b.patch.DSN != "postgres://app@localhost/db") {
				t.Fatal("false flag changed password or prevented DSN input")
			}
			if operation == "add" && (b.input.Password != "" || b.input.DSN != "postgres://app@localhost/db") {
				t.Fatal("false flag changed create input")
			}
		})
	}
}

func TestConnAddPasswordReadError(t *testing.T) {
	b := &passwordBackend{}
	cmd := passwordCommand(b, "add")
	wantErr := errors.New("fixture read failed")
	cmd.SetIn(&passwordReader{err: wantErr})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"shop", "--engine", "postgres", "--host", "localhost", "--password-stdin"})
	if err := cmd.Execute(); !errors.Is(err, wantErr) || b.creates != 0 {
		t.Fatal("read error did not stop creation")
	}
}

func TestConnAddPasswordStdinRegression(t *testing.T) {
	for _, tc := range []struct{ name, flag, stdin, want string }{
		{"true", "--password-stdin", " replacement \nignored", "replacement"},
		{"false", "--password-stdin=false", "unused", ""},
		{"empty", "--password-stdin", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &passwordBackend{}
			cmd := passwordCommand(b, "add")
			reader := &passwordReader{Reader: strings.NewReader(tc.stdin)}
			cmd.SetIn(reader)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"shop", "--engine", "postgres", "--host", "localhost", "--skip-test", tc.flag})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("add failed: %v", err)
			}
			if b.creates != 1 || b.updates != 0 || b.input.Password != tc.want || b.test {
				t.Fatal("creation password or test policy changed")
			}
			if tc.name == "false" && reader.reads != 0 {
				t.Fatal("false flag read stdin")
			}
		})
	}
}

// These real commands catch regressions in local persistence and remote routing;
// the recorder above checks password presence without using redacted GET output.
func testPasswordEditCommands(t *testing.T, command func(string, ...string) (string, string, int)) {
	t.Helper()
	check := func(stdin string, wantCode int, wantText string, args ...string) {
		t.Helper()
		out, errOut, code := command(stdin, args...)
		if code != wantCode || !strings.Contains(out+errOut, wantText) {
			t.Fatalf("%s %s: code %d (want %d), expected %q", args[0], args[1], code, wantCode, wantText)
		}
		if strings.Contains(out+errOut, "replacement") {
			t.Fatal("password disclosed")
		}
	}
	check("", 0, "added shop", "conn", "add", "shop", "--engine", "postgres", "--dsn", "postgres://app@localhost/old?sslmode=disable", "--skip-test")
	check("replacement\n", 0, "version 2", "conn", "edit", "shop", "--password-stdin", "--skip-test")
	check("", 0, "version 3", "conn", "edit", "shop", "--password-stdin", "--skip-test")
	check("", 0, "version 4", "conn", "edit", "shop", "--password-ref", "env:QH_TEST_UNRESOLVED_PASSWORD", "--skip-test")
	check("replacement\n", 1, "error[invalid]", "conn", "edit", "shop", "--password-stdin", "--skip-test")
	check("replacement\n", 0, "version 5", "conn", "edit", "shop", "--password-ref", "", "--password-stdin", "--skip-test")
	check("", 0, "version 6", "conn", "edit", "shop", "--dsn-ref", "env:QH_TEST_UNRESOLVED_DSN", "--skip-test")
	check("replacement\n", 1, "error[invalid]", "conn", "edit", "shop", "--password-stdin", "--skip-test")
	check("replacement\n", 0, "version 7", "conn", "edit", "shop", "--dsn", "postgres://app@localhost/new?sslmode=disable", "--password-stdin", "--skip-test")
	check("", 0, "new", "conn", "show", "shop")
}

func TestConnEditPasswordStdinLocal(t *testing.T) {
	home := t.TempDir()
	testPasswordEditCommands(t, func(stdin string, args ...string) (string, string, int) { return run(t, home, stdin, args...) })
}
