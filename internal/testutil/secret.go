package testutil

import (
	"strings"
	"testing"
)

// RequireNoSecret fails t when got contains any of secrets. The failure names
// what was checked and the secret's index and length, never the text. A
// secret short enough to occur in fixed text, such as a category or the ***
// marker, needs an exact expected output instead.
func RequireNoSecret(t testing.TB, what, got string, secrets ...string) {
	t.Helper()
	for i, s := range secrets {
		if s != "" && strings.Contains(got, s) {
			t.Fatalf("%s: contains secret #%d (%d bytes)", what, i, len(s))
		}
	}
}
