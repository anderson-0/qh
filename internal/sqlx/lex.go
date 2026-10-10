// Package sqlx is a small SQL lexer used to classify statements, count them
// and find the tables they reference. It never rewrites SQL.
package sqlx

import (
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
)

// Dialect selects quoting and comment rules.
type Dialect int

const (
	Postgres Dialect = iota + 1
	MySQL
	SQLite
)

type kind int

const (
	kWord   kind = iota + 1 // unquoted identifier or keyword, lowercased
	kQuoted                 // quoted identifier, case preserved
	kSemi
	kLParen
	kRParen
	kComma
	kDot
	kEquals
	kOther // literals, operators, parameters
)

type token struct {
	kind kind
	text string
}

func unterminated(what string) error {
	return qerr.New(qerr.Invalid, "unterminated "+what+" in query")
}

func tokenize(s string, d Dialect) ([]token, error) {
	var toks []token
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < n && s[i+1] == '-', c == '#' && d == MySQL:
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			end, ok := skipBlockComment(s, i, d == Postgres)
			if !ok {
				return nil, unterminated("comment")
			}
			i = end
		case c == '\'':
			end, ok := skipQuoted(s, i, '\'', d == MySQL)
			if !ok {
				return nil, unterminated("string")
			}
			toks = append(toks, token{kOther, "'"})
			i = end
		case (c == 'e' || c == 'E') && d == Postgres && i+1 < n && s[i+1] == '\'':
			end, ok := skipQuoted(s, i+1, '\'', true)
			if !ok {
				return nil, unterminated("string")
			}
			toks = append(toks, token{kOther, "'"})
			i = end
		case c == '"' && d == MySQL:
			end, ok := skipQuoted(s, i, '"', true)
			if !ok {
				return nil, unterminated("string")
			}
			toks = append(toks, token{kOther, "'"})
			i = end
		case c == '"':
			end, text, ok := scanIdent(s, i, '"')
			if !ok {
				return nil, unterminated("quoted identifier")
			}
			toks = append(toks, token{kQuoted, text})
			i = end
		case c == '`' && d != Postgres:
			end, text, ok := scanIdent(s, i, '`')
			if !ok {
				return nil, unterminated("quoted identifier")
			}
			toks = append(toks, token{kQuoted, text})
			i = end
		case c == '[' && d == SQLite:
			end := strings.IndexByte(s[i+1:], ']')
			if end < 0 {
				return nil, unterminated("quoted identifier")
			}
			toks = append(toks, token{kQuoted, s[i+1 : i+1+end]})
			i += end + 2
		case c == '$' && d == Postgres:
			if tag, ok := dollarTag(s, i); ok {
				end := strings.Index(s[i+len(tag):], tag)
				if end < 0 {
					return nil, unterminated("dollar-quoted string")
				}
				toks = append(toks, token{kOther, "$$"})
				i += len(tag) + end + len(tag)
			} else {
				toks = append(toks, token{kOther, "$"})
				i++
			}
		case c == ';':
			toks = append(toks, token{kSemi, ";"})
			i++
		case c == '(':
			toks = append(toks, token{kLParen, "("})
			i++
		case c == ')':
			toks = append(toks, token{kRParen, ")"})
			i++
		case c == ',':
			toks = append(toks, token{kComma, ","})
			i++
		case c == '.':
			toks = append(toks, token{kDot, "."})
			i++
		case c == '=':
			toks = append(toks, token{kEquals, "="})
			i++
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdentPart(s[j]) {
				j++
			}
			toks = append(toks, token{kWord, strings.ToLower(s[i:j])})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < n && (isIdentPart(s[j]) || s[j] == '.') {
				j++
			}
			toks = append(toks, token{kOther, s[i:j]})
			i = j
		default:
			toks = append(toks, token{kOther, string(c)})
			i++
		}
	}
	return toks, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || c >= '0' && c <= '9' || c == '$'
}

// skipBlockComment returns the index just past the comment starting at i.
// Postgres block comments nest; the others end at the first */.
func skipBlockComment(s string, i int, nested bool) (int, bool) {
	depth := 0
	for j := i; j < len(s)-1; {
		switch {
		case s[j] == '/' && s[j+1] == '*' && (nested || depth == 0):
			depth++
			j += 2
		case s[j] == '*' && s[j+1] == '/':
			depth--
			j += 2
			if depth == 0 {
				return j, true
			}
		default:
			j++
		}
	}
	return len(s), false
}

// skipQuoted returns the index just past the quoted string starting at i.
// A doubled quote is an escaped quote; backslash escapes when enabled.
func skipQuoted(s string, i int, q byte, backslash bool) (int, bool) {
	for j := i + 1; j < len(s); j++ {
		switch {
		case backslash && s[j] == '\\':
			j++
		case s[j] == q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1, true
		}
	}
	return len(s), false
}

// scanIdent reads a quoted identifier starting at i and returns its unquoted text.
func scanIdent(s string, i int, q byte) (int, string, bool) {
	var b strings.Builder
	for j := i + 1; j < len(s); j++ {
		if s[j] == q {
			if j+1 < len(s) && s[j+1] == q {
				b.WriteByte(q)
				j++
				continue
			}
			return j + 1, b.String(), true
		}
		b.WriteByte(s[j])
	}
	return len(s), "", false
}

// dollarTag reports whether a Postgres dollar-quote tag ($$ or $name$) starts at i.
func dollarTag(s string, i int) (string, bool) {
	j := i + 1
	if j < len(s) && s[j] >= '0' && s[j] <= '9' {
		return "", false // $1 is a parameter
	}
	for j < len(s) && (isIdentStart(s[j]) || s[j] >= '0' && s[j] <= '9') {
		j++
	}
	if j < len(s) && s[j] == '$' {
		return s[i : j+1], true
	}
	return "", false
}
