package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute("1.2.3", []string{"version"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errOut.String())
	}
	if out.String() != "1.2.3\n" {
		t.Fatalf("stdout %q", out.String())
	}
	out.Reset()
	if code := Execute("1.2.3", []string{"--version"}, strings.NewReader(""), &out, &errOut); code != 0 || out.String() != "1.2.3\n" {
		t.Fatalf("--version: exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Execute("dev", []string{"nope"}, strings.NewReader(""), &out, &errOut); code != 1 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("stderr %q", errOut.String())
	}
}
