package redact_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/redact"
	"github.com/anderson-0/qh/internal/testutil"
)

// expect fails t when got is not want. The failure shows want, which holds no
// secret, and where got first differs, never got itself.
func expect(t *testing.T, name, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	t.Errorf("%s: want %q; got %d bytes that differ at byte %d", name, want, len(got), i)
}

type textCase struct {
	name    string
	secrets []string
	in      string
	want    string
}

func runText(t *testing.T, cases []textCase) {
	t.Helper()
	for _, c := range cases {
		expect(t, c.name, redact.New(c.secrets...).Text(c.in), c.want)
	}
}

func TestTextShortValues(t *testing.T) {
	runText(t, []textCase{
		{"one byte", []string{"a"}, "a bad cat", "*** b***d c***t"},
		{"one byte run", []string{"a"}, "aaa", "***"},
		{"two bytes", []string{"ab"}, "ab cab abc", "*** c*** ***c"},
		{"two bytes partial", []string{"ab"}, "a b ba", "a b ba"},
		{"three bytes", []string{"abc"}, "xabcx abcabc ab", "x***x *** ab"},
	})
}

func TestTextCoverage(t *testing.T) {
	runText(t, []textCase{
		{"nested", []string{"abc", "abcdef"}, "xabcdefy", "x***y"},
		{"nested reversed", []string{"abcdef", "abc"}, "xabcdefy", "x***y"},
		{"overlapping", []string{"abc", "bcd"}, "abcd", "***"},
		{"overlapping reversed", []string{"bcd", "abc"}, "abcd", "***"},
		{"overlapping longer tail", []string{"ab", "bcdef"}, "abcdefg", "***g"},
		{"inline and resolved", []string{"x1", "1secret"}, "x1secret", "***"},
		{"adjacent", []string{"ab"}, "abab", "***"},
		{"separated", []string{"ab"}, "abXab", "***X***"},
		{"different secrets adjacent", []string{"ab", "cd"}, "-abcd-", "-***-"},
	})
}

// coverRef is a slow reference for Text: it marks every byte that any secret
// occurrence covers, then turns each run of marked bytes into one Marker.
func coverRef(secrets []string, in string) string {
	covered := make([]bool, len(in))
	for _, s := range secrets {
		if s == "" {
			continue
		}
		for i := 0; i+len(s) <= len(in); i++ {
			if in[i:i+len(s)] == s {
				for j := i; j < i+len(s); j++ {
					covered[j] = true
				}
			}
		}
	}
	var b strings.Builder
	for i := 0; i < len(in); i++ {
		switch {
		case !covered[i]:
			b.WriteByte(in[i])
		case i == 0 || !covered[i-1]:
			b.WriteString(redact.Marker)
		}
	}
	return b.String()
}

// TestTextMatchesReference compares Text with coverRef on random input. The
// alphabet has no byte that any encoding changes, so every form of a secret
// is the secret itself.
func TestTextMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	word := func(max int) string {
		b := make([]byte, 1+r.IntN(max))
		for i := range b {
			b[i] = "abc"[r.IntN(3)]
		}
		return string(b)
	}
	for n := range 5000 {
		secrets := make([]string, 1+r.IntN(4))
		for i := range secrets {
			secrets[i] = word(4)
		}
		in := word(24)
		if got, want := redact.New(secrets...).Text(in), coverRef(secrets, in); got != want {
			t.Fatalf("case %d: Text differs from the reference", n)
		}
	}
}

func TestTextNoRescan(t *testing.T) {
	runText(t, []textCase{
		{"star", []string{"*"}, "a*b", "a***b"},
		{"star run", []string{"*"}, "**", "***"},
		{"two stars", []string{"**"}, "a**b*", "a***b*"},
		{"marker itself", []string{"***"}, "x***y", "x***y"},
	})
}

func TestDuplicatesCollapse(t *testing.T) {
	const in = "a pw b pw"
	expect(t, "once", redact.New("pw").Text(in), "a *** b ***")
	expect(t, "twice", redact.New("pw", "pw").Text(in), "a *** b ***")
	expect(t, "with empties", redact.New("", "pw", "", "pw").Text(in), "a *** b ***")
}

func TestTextEncodings(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		forms  map[string]string
	}{
		{"mixed", `p@ss w/rd'\`, map[string]string{
			"verbatim":       `p@ss w/rd'\`,
			"query escape":   `p%40ss+w%2Frd%27%5C`,
			"query %20":      `p%40ss%20w%2Frd%27%5C`,
			"path escape":    `p@ss%20w%2Frd%27%5C`,
			"keyword quoted": `p@ss w/rd\'\\`,
		}},
		// Reserved characters that path and userinfo escaping treat
		// differently, so each form is distinct.
		{"reserved", `a+b&c=d;e:f`, map[string]string{
			"verbatim":     `a+b&c=d;e:f`,
			"query escape": `a%2Bb%26c%3Dd%3Be%3Af`,
			"path escape":  `a+b&c=d%3Be:f`,
			"userinfo":     `a+b&c=d;e%3Af`,
		}},
		// The %20 form, which Postgres and MongoDB password edits write,
		// differs here from every other form.
		{"space and ampersand", `a b&c`, map[string]string{
			"verbatim":      `a b&c`,
			"query escape":  `a+b%26c`,
			"query %20":     `a%20b%26c`,
			"path userinfo": `a%20b&c`,
		}},
	}
	for _, c := range cases {
		s := redact.New(c.secret)
		for form, enc := range c.forms {
			expect(t, c.name+" "+form, s.Text("before "+enc+" after"), "before *** after")
		}
	}
	// The userinfo form of the first secret, as a URL carries it.
	expect(t, "url", redact.New(`p@ss w/rd'\`).Text("postgres://u:p%40ss%20w%2Frd%27%5C@h/db"), "postgres://u:***@h/db")
}

func TestTextByteExact(t *testing.T) {
	const pem = "-----BEGIN TEST KEY-----\nMIIBsynthetic0\r\nAAAAkeydata1\n-----END TEST KEY-----"
	runText(t, []textCase{
		{"spaces kept", []string{" pw "}, "[ pw ]", "[***]"},
		{"spaces required", []string{" pw "}, "pw, pw.", "pw, pw."},
		{"spaces query escaped", []string{" pw "}, "x=+pw+&y", "x=***&y"},
		{"unicode", []string{"pässwörd"}, "x pässwörd y", "x *** y"},
		{"unicode query escaped", []string{"pässwörd"}, "password=p%C3%A4ssw%C3%B6rd", "password=***"},
		{"unicode prefix", []string{"pässwörd"}, "pässw", "pässw"},
		{"pem", []string{pem}, "tls: bad key " + pem + " end", "tls: bad key *** end"},
		{"pem one line", []string{pem}, "tls: bad key -----BEGIN TEST KEY----- MIIBsynthetic0 AAAAkeydata1 -----END TEST KEY----- end", "tls: bad key *** end"},
		// A single-space secret removes every space, and the '+' and %20
		// that encode one. Redaction favours safety over readability.
		{"single space", []string{" "}, "a b c", "a***b***c"},
		{"single space encoded", []string{" "}, "a+b%20c", "a***b***c"},
	})

	// Error redacts the raw multi-line message before qerr prints it on one line.
	out := redact.New(pem).Error(qerr.New(qerr.Invalid, "bad key:\n"+pem)).Error()
	testutil.RequireNoSecret(t, "pem error", out, "MIIBsynthetic0", "AAAAkeydata1")
	expect(t, "pem error", out, "error[invalid]: bad key: ***")
}

func TestTextTrimmedMultiLine(t *testing.T) {
	// A key read from a file ends in a newline. Printed at the end of a qerr
	// message, it loses that newline, and a later message may quote the
	// printed text.
	const pem = "-----BEGIN TEST KEY-----\nMIIBsynthetic0\nAAAAkeydata1\n-----END TEST KEY-----\n"
	inner := qerr.New(qerr.Invalid, "bad key: "+pem).Error()
	out := redact.New(pem).Error(qerr.New(qerr.ConnectionFailed, "ssh: "+inner)).Error()
	testutil.RequireNoSecret(t, "nested pem", out, "MIIBsynthetic0", "AAAAkeydata1")
	expect(t, "nested pem", out, "error[connection_failed]: ssh: error[invalid]: bad key: ***")

	runText(t, []textCase{
		{"trimmed raw", []string{"\n\tline one\nline two\n"}, "[line one\nline two]", "[***]"},
		{"trimmed one line", []string{"\n\tline one\nline two\n"}, "[line one line two]", "[***]"},
		// Only values with line breaks or tabs get trimmed forms.
		{"single line stays exact", []string{" pw-one "}, "[pw-one]", "[pw-one]"},
	})
}

func TestError(t *testing.T) {
	s := redact.New("pw")
	if s.Error(nil) != nil {
		t.Fatal("Error(nil) is not nil")
	}
	if (*redact.Set)(nil).Error(nil) != nil {
		t.Fatal("nil Set: Error(nil) is not nil")
	}

	cause := errors.New("driver: password pw rejected")
	in := &qerr.Error{Category: qerr.Auth, Message: "password pw rejected", Hint: "check pw",
		UnknownColumn: "pw_col", UnknownTable: "pw_tab", Err: cause}
	before := *in
	out := s.Error(in)
	if *in != before {
		t.Fatal("Error modified its input")
	}
	e, ok := out.(*qerr.Error)
	if !ok {
		t.Fatalf("Error returned %T, want *qerr.Error", out)
	}
	if e == in {
		t.Fatal("Error returned its input")
	}
	if errors.Unwrap(out) != nil || errors.Is(out, cause) {
		t.Fatal("the cause survives")
	}
	if e.Category != qerr.Auth {
		t.Errorf("category %q, want %q", e.Category, qerr.Auth)
	}
	expect(t, "message", e.Message, "password *** rejected")
	expect(t, "hint", e.Hint, "check ***")
	expect(t, "unknown column", e.UnknownColumn, "***_col")
	expect(t, "unknown table", e.UnknownTable, "***_tab")
	expect(t, "formatted", out.Error(), "error[auth]: password *** rejected. check ***")

	// The category is printed after redaction, so its bytes stay even when
	// a one-byte secret matches them; only the message's own "r" goes.
	expect(t, "category bytes", redact.New("r").Error(qerr.New(qerr.QueryError, "bad r")).Error(), "error[query_error]: bad ***")

	plain := s.Error(errors.New("dial: pw refused"))
	pe, ok := plain.(*qerr.Error)
	if !ok {
		t.Fatalf("plain: Error returned %T, want *qerr.Error", plain)
	}
	if pe.Category != qerr.QueryError || errors.Unwrap(plain) != nil {
		t.Errorf("plain: category %q, cause kept %v", pe.Category, errors.Unwrap(plain) != nil)
	}
	expect(t, "plain message", pe.Message, "dial: *** refused")

	wrapped := s.Error(fmt.Errorf("context: %w", qerr.Wrap(qerr.ConnectionFailed, "connect pw", cause)))
	we := wrapped.(*qerr.Error)
	if we.Category != qerr.ConnectionFailed || errors.Unwrap(wrapped) != nil {
		t.Errorf("wrapped: category %q, cause kept %v", we.Category, errors.Unwrap(wrapped) != nil)
	}
	expect(t, "wrapped message", we.Message, "connect ***")

	// A nil Set redacts nothing but still drops the cause.
	ne := (*redact.Set)(nil).Error(qerr.Wrap(qerr.Timeout, "slow", cause)).(*qerr.Error)
	if ne.Category != qerr.Timeout || ne.Err != nil {
		t.Errorf("nil Set: category %q, cause kept %v", ne.Category, ne.Err != nil)
	}
	expect(t, "nil Set message", ne.Message, "slow")
}

func TestNilAndEmptySets(t *testing.T) {
	sets := map[string]*redact.Set{
		"nil":          nil,
		"no values":    redact.New(),
		"empty values": redact.New("", ""),
		"zero value":   new(redact.Set),
	}
	for name, s := range sets {
		for _, in := range []string{"", "a b", "***", "pw"} {
			expect(t, name, s.Text(in), in)
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	s := redact.New("pw", "token-1", "-----BEGIN KEY-----\nabc\n-----END KEY-----")
	in := qerr.New(qerr.Auth, "pw and token-1 rejected").WithHint("rotate pw")
	before := *in
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 200 {
				expect(t, "text", s.Text("x pw token-1 y"), "x *** *** y")
				expect(t, "error", s.Error(in).Error(), "error[auth]: *** and *** rejected. rotate ***")
			}
		})
	}
	wg.Wait()
	if *in != before {
		t.Fatal("Error modified its shared input")
	}
}
