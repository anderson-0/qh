package secretref

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/qerr"
)

func cat(err error) qerr.Category { return qerr.From(err).Category }

func TestParse(t *testing.T) {
	for in, want := range map[string]Ref{
		"env:QH_DB_PW":              {Raw: "env:QH_DB_PW", Scheme: "env", Name: "QH_DB_PW"},
		"file:/run/secrets/db":      {Raw: "file:/run/secrets/db", Scheme: "file", Name: "/run/secrets/db"},
		"aws-sm://prod/db":          {Raw: "aws-sm://prod/db", Scheme: "aws-sm", Name: "prod/db"},
		"aws-sm://prod/db#password": {Raw: "aws-sm://prod/db#password", Scheme: "aws-sm", Name: "prod/db", Key: "password"},
		"aws-sm://arn:aws:secretsmanager:eu-west-1:1:secret:x#k": {Raw: "aws-sm://arn:aws:secretsmanager:eu-west-1:1:secret:x#k", Scheme: "aws-sm", Name: "arn:aws:secretsmanager:eu-west-1:1:secret:x", Key: "k"},
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "env:", "env:1X", "env:A-B", "file:relative/path", "aws-sm://", "aws-sm://x#", "vault://x", "ENV:X"} {
		if _, err := Parse(bad); cat(err) != qerr.Invalid {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestResolveEnvAndFile(t *testing.T) {
	ctx := context.Background()
	s := NewSet(false, nil)
	t.Setenv("QH_TEST_SECRET", "s3cret-value")
	if v, err := s.Resolve(ctx, "env:QH_TEST_SECRET"); err != nil || v != "s3cret-value" {
		t.Fatalf("env: %q %v", v, err)
	}
	t.Setenv("QH_TEST_EMPTY", "")
	if _, err := s.Resolve(ctx, "env:QH_TEST_EMPTY"); cat(err) != qerr.ConnectionFailed || !strings.Contains(err.Error(), "env:QH_TEST_EMPTY: not set") {
		t.Fatalf("empty env: %v", err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "pw")
	os.WriteFile(p, []byte("file-secret\n"), 0o600)
	if v, err := s.Resolve(ctx, "file:"+p); err != nil || v != "file-secret" {
		t.Fatalf("file: %q %v", v, err)
	}
	crlf := filepath.Join(dir, "crlf")
	os.WriteFile(crlf, []byte("a\r\n"), 0o600)
	if v, _ := s.Resolve(ctx, "file:"+crlf); v != "a" {
		t.Fatalf("crlf: %q", v)
	}
	big := filepath.Join(dir, "big")
	os.WriteFile(big, make([]byte, 64<<10+1), 0o600)
	if _, err := s.Resolve(ctx, "file:"+big); err == nil || !strings.Contains(err.Error(), "larger than 64 KiB") {
		t.Fatalf("big: %v", err)
	}
	if _, err := s.Resolve(ctx, "file:"+filepath.Join(dir, "missing")); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("missing: %v", err)
	}
}

func TestErrorsNeverContainValues(t *testing.T) {
	t.Setenv("QH_TEST_JSONISH", "{not json")
	s := NewSet(false, nil)
	// env and file never fail after reading, so check the aws-sm JSON path
	// through a fake in aws_test.go; here check a read error message.
	_, err := s.Resolve(context.Background(), "file:/definitely/not/here")
	if err == nil || strings.Contains(err.Error(), "{not json") {
		t.Fatalf("%v", err)
	}
}

func TestAllowList(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "allowed")
	os.Mkdir(allowed, 0o700)
	os.WriteFile(filepath.Join(allowed, "pw"), []byte("x"), 0o600)
	outside := filepath.Join(dir, "outside")
	os.WriteFile(outside, []byte("y"), 0o600)
	os.Symlink(outside, filepath.Join(allowed, "link"))

	s := NewSet(true, []string{"env:QH_TENANT_*", "file:" + allowed + "/*"})
	for _, ok := range []string{"env:QH_TENANT_DB", "file:" + filepath.Join(allowed, "pw")} {
		if _, err := s.Check(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"env:HOME", "env:QH_TENANTX", "aws-sm://anything",
		"file:" + allowed + "/../outside",        // .. escape
		"file:" + filepath.Join(allowed, "link"), // symlink escape
	} {
		_, err := s.Check(bad)
		if cat(err) != qerr.Forbidden || !strings.Contains(err.Error(), "is not allowed on this server") ||
			!strings.Contains(err.Error(), "qh serve --allow-secret-ref") {
			t.Errorf("%s: %v", bad, err)
		}
	}
	// Local mode accepts everything.
	if _, err := NewSet(false, nil).Check("env:HOME"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPattern(t *testing.T) {
	for _, ok := range []string{"env:QH_*", "file:/run/secrets/*", "aws-sm://arn:aws:secretsmanager:*"} {
		if err := CheckPattern(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "*", "QH_*", "vault://x"} {
		if err := CheckPattern(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRelativeFilePatternRefused(t *testing.T) {
	if err := CheckPattern("file:data/*"); err == nil || !strings.Contains(err.Error(), `invalid --allow-secret-ref pattern "file:data/*"`) {
		t.Fatalf("relative pattern accepted: %v", err)
	}
	if err := CheckPattern("file:/run/secrets/*"); err != nil {
		t.Fatal(err)
	}
}

// Works on any OS: the data symlink stands in for macOS /var.
func TestSymlinkedRootAllowList(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	os.Mkdir(real, 0o700)
	os.WriteFile(filepath.Join(real, "pw"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(tmp, "top"), []byte("y"), 0o600)
	if err := os.Symlink(real, filepath.Join(tmp, "data")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}

	s := NewSet(true, []string{"file:" + filepath.Join(tmp, "data") + "/*"})
	if _, err := s.Check("file:" + filepath.Join(tmp, "data", "pw")); err != nil {
		t.Errorf("pw refused: %v", err)
	}
	if _, err := s.Check("file:" + filepath.Join(tmp, "data") + "/../top"); cat(err) != qerr.Forbidden {
		t.Errorf("escape accepted: %v", err)
	}
}

func TestCanonicalFileKeepsRelativePattern(t *testing.T) {
	tmp := t.TempDir()
	os.Mkdir(filepath.Join(tmp, "real"), 0o700)
	if err := os.Symlink(filepath.Join(tmp, "real"), filepath.Join(tmp, "data")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	t.Chdir(tmp)
	if got := canonicalFile("file:data/*"); got != "file:data/*" {
		t.Fatalf("relative pattern resolved against the working directory: %q", got)
	}
}
