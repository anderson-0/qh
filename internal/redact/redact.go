// Package redact removes known secret values from text that qh shows to
// agents, API clients and CLI users.
package redact

import (
	"net/url"
	"sort"
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
)

// Marker replaces every secret occurrence.
const Marker = "***"

// Set is an immutable set of secrets. A nil *Set redacts nothing. It is safe
// for concurrent use.
type Set struct {
	byFirst map[byte][]string // distinct secrets by first byte, longest first
}

// New returns a Set holding every nonempty value, byte for byte, and the
// encodings of it that supported connection strings and qh's own error
// formatting produce.
func New(values ...string) *Set {
	s := &Set{byFirst: map[byte][]string{}}
	seen := map[string]bool{}
	for _, v := range values {
		if v == "" {
			continue
		}
		for _, f := range forms(v) {
			if f != "" && !seen[f] {
				seen[f] = true
				s.byFirst[f[0]] = append(s.byFirst[f[0]], f)
			}
		}
	}
	for _, b := range s.byFirst {
		sort.SliceStable(b, func(x, y int) bool { return len(b[x]) > len(b[y]) })
	}
	return s
}

// forms are v and its bounded encodings: percent-encoded query and userinfo
// values (with '+' or %20 for space), libpq keyword quoting, and the
// one-line form qerr gives multi-line text. A value with line breaks or
// tabs, such as a PEM key, also gets its trimmed forms: qerr trims the
// message it prints, so such a value at either end loses its outer
// whitespace. Other values stay byte-exact.
func forms(v string) []string {
	q := url.QueryEscape(v)
	user := url.UserPassword("", v).String() // ":" + escaped password
	one := qerr.OneLine(v)
	out := []string{v, one, q, strings.ReplaceAll(q, "+", "%20"), url.PathEscape(v), user[1:], keywordQuoted(v)}
	if one != v {
		out = append(out, strings.TrimSpace(v), strings.TrimSpace(one))
	}
	return out
}

// keywordQuoted escapes ' and \ as in a libpq keyword/value string.
func keywordQuoted(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\'' || v[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// Text returns in with every byte that any secret occurrence covers replaced:
// each run of covered bytes, including overlapping or adjacent occurrences,
// becomes one Marker. Matching runs on the original text only, so a Marker
// that Text inserts is never matched again.
func (s *Set) Text(in string) string {
	if s == nil || len(s.byFirst) == 0 || in == "" {
		return in
	}
	var b strings.Builder
	last, end := 0, -1 // last: start of uncopied text; end: end of the covered run, -1 before the first
	for i := 0; i < len(in); i++ {
		n := s.match(in[i:])
		if n == 0 || i+n <= end {
			continue // no secret here, or one inside the current run
		}
		if i > end { // a new run: copy the text since the previous one
			b.WriteString(in[last:i])
			b.WriteString(Marker)
		}
		end = i + n // a new, overlapping or adjacent occurrence extends the run
		last = end
	}
	if end < 0 {
		return in
	}
	b.WriteString(in[last:])
	return b.String()
}

// match returns the length of the longest secret at the start of t.
func (s *Set) match(t string) int {
	for _, sec := range s.byFirst[t[0]] {
		if strings.HasPrefix(t, sec) {
			return len(sec)
		}
	}
	return 0
}

// Error returns err as a new *qerr.Error with Message, Hint, UnknownColumn
// and UnknownTable redacted and the cause dropped, so no secret leaks through
// Unwrap. The category is kept, and err itself is never modified. A nil err
// stays nil.
func (s *Set) Error(err error) error {
	if err == nil {
		return nil
	}
	e := *qerr.From(err)
	e.Err = nil
	e.Message, e.Hint = s.Text(e.Message), s.Text(e.Hint)
	e.UnknownColumn, e.UnknownTable = s.Text(e.UnknownColumn), s.Text(e.UnknownTable)
	return &e
}
