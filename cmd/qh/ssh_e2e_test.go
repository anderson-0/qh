package main

import (
	"bytes"
	"net"
	"os/exec"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

// TestSSHFingerprintFlow drives the real binary through the first-use host-key
// flow. The bastion refuses every forward: this checks the CLI, not a database.
func TestSSHFingerprintFlow(t *testing.T) {
	bin := buildBinary(t)
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw",
		Forward: func(target string) (net.Conn, error) { return nil, net.UnknownNetworkError(target) }})
	home := t.TempDir()
	run := func(stdin string, args ...string) (stdout, stderr string, err error) {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"--home", home}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		err = cmd.Run()
		return so.String(), se.String(), err
	}
	add := []string{"conn", "add", "pg", "--engine", "postgres", "--dsn", "postgres://u@db.internal:5432/app",
		"--ssh-host", "u@" + srv.Addr, "--ssh-password-stdin"}

	// First run: the host key is unknown, so qh shows the fingerprint and stores nothing.
	_, stderr, err := run("pw\n", add...)
	if err == nil {
		t.Fatal("conn add succeeded with an unknown host key")
	}
	if !strings.Contains(stderr, "unknown host key") || !strings.Contains(stderr, "--ssh-host-key "+srv.Fingerprint) {
		t.Fatalf("stderr lacks the fingerprint hint:\n%s", stderr)
	}
	stdout, stderr, err := run("", "conn", "ls")
	if err != nil {
		t.Fatalf("conn ls: %v\n%s", err, stderr)
	}
	if strings.Contains(stdout, "pg") {
		t.Fatalf("a failed add left a connection behind:\n%s", stdout)
	}

	// Second run with the fingerprint: the host key is accepted and the tunnel
	// reaches the forwarding stage, where the bastion refuses the target.
	_, stderr, err = run("pw\n", append(add, "--ssh-host-key", srv.Fingerprint)...)
	if err == nil {
		t.Fatal("conn add succeeded although the bastion cannot reach the database")
	}
	if !strings.Contains(stderr, "ssh: the bastion could not reach db.internal:5432") {
		t.Fatalf("stderr:\n%s", stderr)
	}
}
