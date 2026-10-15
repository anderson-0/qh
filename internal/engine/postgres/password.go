package postgres

import (
	"net/url"
	"strings"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

var _ engine.PasswordDSNSetter = Engine{}

// SetPassword replaces only explicit database-password settings. Parsing here
// follows the syntax in pgx v5.11.0's pgconn/config.go and parse_url.go, without
// calling ParseConfig: that resolver reads environment, service and TLS files.
func (Engine) SetPassword(dsn, password string) (string, error) {
	if strings.IndexByte(dsn, 0) >= 0 || strings.IndexByte(password, 0) >= 0 {
		return "", invalidPasswordDSN()
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return setURLPassword(dsn, password)
	}
	return setKeywordPassword(dsn, password)
}

func invalidPasswordDSN() error {
	return qerr.New(qerr.Invalid, "invalid postgres connection string")
}

const passwordWhitespace = " \t\n\r\v\f"

// Quoting every value preserves even a final escaped end-of-string, an empty
// value, or quoted values followed immediately by another key. Retain duplicate
// non-password keys in order, including pgx's special empty-user semantics.
func setKeywordPassword(dsn, password string) (string, error) {
	var out strings.Builder
	for rest := strings.TrimLeft(dsn, passwordWhitespace); rest != ""; {
		key, tail, found := strings.Cut(rest, "=")
		key = strings.Trim(key, passwordWhitespace)
		if !found || key == "" || strings.ContainsAny(key, passwordWhitespace) {
			return "", invalidPasswordDSN()
		}
		rest = strings.TrimLeft(tail, passwordWhitespace)
		var value strings.Builder
		quoted := strings.HasPrefix(rest, "'")
		if quoted {
			rest = rest[1:]
		}
		end := 0
		closed := !quoted
		for end < len(rest) {
			c := rest[end]
			if quoted && c == '\'' {
				end++
				closed = true
				break
			}
			if !quoted && strings.ContainsRune(passwordWhitespace, rune(c)) {
				break
			}
			end++
			if c == '\\' {
				if end == len(rest) {
					break
				}
				c = rest[end]
				end++
			}
			value.WriteByte(c)
		}
		if !closed {
			return "", invalidPasswordDSN()
		}
		rest = strings.TrimLeft(rest[end:], passwordWhitespace)
		if key != "password" {
			writePasswordKeyword(&out, key, value.String())
		}
	}
	writePasswordKeyword(&out, "password", password)
	return out.String(), nil
}

func writePasswordKeyword(out *strings.Builder, key, value string) {
	if out.Len() > 0 {
		out.WriteByte(' ')
	}
	out.WriteString(key)
	out.WriteString("='")
	for i := 0; i < len(value); i++ {
		if value[i] == '\'' || value[i] == '\\' {
			out.WriteByte('\\')
		}
		out.WriteByte(value[i])
	}
	out.WriteByte('\'')
}

func setURLPassword(dsn, password string) (string, error) {
	schemeEnd := strings.Index(dsn, "://") + 3
	prefix, rest := dsn[:schemeEnd], dsn[schemeEnd:]
	// As in pgx/libpq, only '/' stops the lookahead for userinfo, even if
	// an '@' appears in what otherwise looks like a query string.
	if i := strings.IndexAny(rest, "@/"); i >= 0 && rest[i] == '@' {
		user, oldPassword, hasPassword := strings.Cut(rest[:i], ":")
		if _, ok := decodePasswordURI(user); !ok {
			return "", invalidPasswordDSN()
		}
		if hasPassword {
			if _, ok := decodePasswordURI(oldPassword); !ok {
				return "", invalidPasswordDSN()
			}
		}
		prefix += user + "@"
		rest = rest[i+1:]
	}
	// Walk pgx's host-list grammar; net/url cannot parse multiple ports and
	// treats '?' inside bracketed hosts differently. Leave these bytes intact.
	hostRest := rest
	var hosts, ports strings.Builder
	for {
		if strings.HasPrefix(rest, "[") {
			end := strings.IndexByte(rest, ']')
			if end <= 1 {
				return "", invalidPasswordDSN()
			}
			hosts.WriteString(rest[1:end])
			rest = rest[end+1:]
			if rest != "" && !strings.ContainsRune(":/?,", rune(rest[0])) {
				return "", invalidPasswordDSN()
			}
		} else {
			end := strings.IndexAny(rest, ":/?,")
			if end < 0 {
				end = len(rest)
			}
			hosts.WriteString(rest[:end])
			rest = rest[end:]
		}
		if strings.HasPrefix(rest, ":") {
			rest = rest[1:]
			end := strings.IndexAny(rest, "/?,")
			if end < 0 {
				end = len(rest)
			}
			ports.WriteString(rest[:end])
			rest = rest[end:]
		}
		if !strings.HasPrefix(rest, ",") {
			break
		}
		rest = rest[1:]
		hosts.WriteByte(',')
		ports.WriteByte(',')
	}
	if _, ok := decodePasswordURI(hosts.String()); !ok {
		return "", invalidPasswordDSN()
	}
	if _, ok := decodePasswordURI(ports.String()); !ok {
		return "", invalidPasswordDSN()
	}
	prefix += hostRest[:len(hostRest)-len(rest)]
	if strings.HasPrefix(rest, "/") {
		end := strings.IndexByte(rest, '?')
		if end < 0 {
			end = len(rest)
		}
		if _, ok := decodePasswordURI(rest[1:end]); !ok {
			return "", invalidPasswordDSN()
		}
		prefix += rest[:end]
		rest = rest[end:]
	}
	var pairs []string
	if strings.HasPrefix(rest, "?") {
		for query := rest[1:]; query != ""; {
			pair, next, _ := strings.Cut(query, "&")
			query = next
			rawKey, rawValue, found := strings.Cut(pair, "=")
			if !found || strings.Contains(rawValue, "=") {
				return "", invalidPasswordDSN()
			}
			key, ok := decodePasswordURI(rawKey)
			if !ok {
				return "", invalidPasswordDSN()
			}
			if _, ok := decodePasswordURI(rawValue); !ok {
				return "", invalidPasswordDSN()
			}
			if key != "password" {
				pairs = append(pairs, pair)
			}
		}
	}
	// pgx does not decode '+' as space. Escape all reserved bytes, including
	// '@' (whose raw form could otherwise create userinfo in a pathless URI).
	encoded := strings.ReplaceAll(url.QueryEscape(password), "+", "%20")
	pairs = append(pairs, "password="+encoded)
	return prefix + "?" + strings.Join(pairs, "&"), nil
}

// pgx/libpq trim literal ASCII spaces around URI components, reject interior
// literal spaces, and percent-decode without treating '+' specially.
func decodePasswordURI(raw string) (string, bool) {
	raw = strings.Trim(raw, " ")
	if strings.ContainsRune(raw, ' ') {
		return "", false
	}
	value, err := url.PathUnescape(raw)
	return value, err == nil && strings.IndexByte(value, 0) < 0
}
