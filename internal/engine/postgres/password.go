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
	if isURLDSN(dsn) {
		return setURLPassword(dsn, password)
	}
	return setKeywordPassword(dsn, password)
}

func invalidPasswordDSN() error {
	return qerr.New(qerr.Invalid, "invalid postgres connection string")
}

// isURLDSN reports whether pgx parses dsn as a URL rather than keyword/value.
func isURLDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

const passwordWhitespace = " \t\n\r\v\f"

// Quoting every value preserves even a final escaped end-of-string, an empty
// value, or quoted values followed immediately by another key. Retain duplicate
// non-password keys in order, including pgx's special empty-user semantics.
func setKeywordPassword(dsn, password string) (string, error) {
	var out strings.Builder
	spaced := false
	err := scanKeywords(dsn, func(key, _, _, value string) {
		// pgx rejects whitespace inside a key; so does the rewrite.
		if strings.ContainsAny(key, passwordWhitespace) {
			spaced = true
		}
		if key != "password" {
			writePasswordKeyword(&out, key, value)
		}
	})
	if err != nil || spaced {
		return "", invalidPasswordDSN()
	}
	writePasswordKeyword(&out, "password", password)
	return out.String(), nil
}

// scanKeywords walks a keyword/value connection string and calls visit for
// each setting in order. raw is the value token as written, quotes included;
// inner is the same token without its quotes, still escaped; value is
// decoded. Keys are reported as found, interior whitespace included.
func scanKeywords(dsn string, visit func(key, raw, inner, value string)) error {
	for rest := strings.TrimLeft(dsn, passwordWhitespace); rest != ""; {
		key, tail, found := strings.Cut(rest, "=")
		key = strings.Trim(key, passwordWhitespace)
		if !found || key == "" {
			return invalidPasswordDSN()
		}
		rest = strings.TrimLeft(tail, passwordWhitespace)
		token := rest
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
			return invalidPasswordDSN()
		}
		raw, inner := rest[:end], rest[:end]
		if quoted {
			raw, inner = token[:end+1], rest[:end-1]
		}
		rest = strings.TrimLeft(rest[end:], passwordWhitespace)
		visit(key, raw, inner, value.String())
	}
	return nil
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
	p, err := parseURLParts(dsn)
	if err != nil {
		return "", err
	}
	// Keep the user name and drop the userinfo password; keep the hosts and
	// path byte for byte.
	prefix := dsn[:p.query]
	if p.userEnd >= 0 {
		prefix = dsn[:p.userEnd] + "@" + dsn[p.hostStart:p.query]
	}
	var pairs []string
	for _, q := range p.pairs {
		if q.key != "password" {
			pairs = append(pairs, q.raw)
		}
	}
	// pgx does not decode '+' as space. Escape all reserved bytes, including
	// '@' (whose raw form could otherwise create userinfo in a pathless URI).
	encoded := strings.ReplaceAll(url.QueryEscape(password), "+", "%20")
	pairs = append(pairs, "password="+encoded)
	return prefix + "?" + strings.Join(pairs, "&"), nil
}

// urlParts locates the pieces of a postgres:// or postgresql:// connection
// string, as byte offsets into it.
type urlParts struct {
	userEnd   int // end of the user name in the userinfo; -1 without userinfo
	pwStart   int // start of the userinfo password; -1 without one
	pwEnd     int // end of the userinfo password, at its '@'
	password  string
	hostStart int // start of the host list, after any userinfo
	query     int // start of the query, at its '?'; len(dsn) without one
	pairs     []urlPair
}

// urlPair is one key=value pair of the query.
type urlPair struct {
	raw      string // the pair as written
	key      string // decoded
	rawValue string
	value    string // decoded
}

// parseURLParts walks dsn with pgx's URI grammar and checks that every
// component decodes. dsn must start with postgres:// or postgresql://.
func parseURLParts(dsn string) (urlParts, error) {
	schemeEnd := strings.Index(dsn, "://") + 3
	p := urlParts{userEnd: -1, pwStart: -1, hostStart: schemeEnd}
	rest := dsn[schemeEnd:]
	// As in pgx/libpq, only '/' stops the lookahead for userinfo, even if
	// an '@' appears in what otherwise looks like a query string.
	if i := strings.IndexAny(rest, "@/"); i >= 0 && rest[i] == '@' {
		user, oldPassword, hasPassword := strings.Cut(rest[:i], ":")
		if _, ok := decodePasswordURI(user); !ok {
			return urlParts{}, invalidPasswordDSN()
		}
		p.userEnd = schemeEnd + len(user)
		if hasPassword {
			decoded, ok := decodePasswordURI(oldPassword)
			if !ok {
				return urlParts{}, invalidPasswordDSN()
			}
			p.pwStart, p.pwEnd, p.password = p.userEnd+1, schemeEnd+i, decoded
		}
		p.hostStart = schemeEnd + i + 1
		rest = rest[i+1:]
	}
	// Walk pgx's host-list grammar; net/url cannot parse multiple ports and
	// treats '?' inside bracketed hosts differently. Leave these bytes intact.
	var hosts, ports strings.Builder
	for {
		if strings.HasPrefix(rest, "[") {
			end := strings.IndexByte(rest, ']')
			if end <= 1 {
				return urlParts{}, invalidPasswordDSN()
			}
			hosts.WriteString(rest[1:end])
			rest = rest[end+1:]
			if rest != "" && !strings.ContainsRune(":/?,", rune(rest[0])) {
				return urlParts{}, invalidPasswordDSN()
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
		return urlParts{}, invalidPasswordDSN()
	}
	if _, ok := decodePasswordURI(ports.String()); !ok {
		return urlParts{}, invalidPasswordDSN()
	}
	if strings.HasPrefix(rest, "/") {
		end := strings.IndexByte(rest, '?')
		if end < 0 {
			end = len(rest)
		}
		if _, ok := decodePasswordURI(rest[1:end]); !ok {
			return urlParts{}, invalidPasswordDSN()
		}
		rest = rest[end:]
	}
	p.query = len(dsn) - len(rest)
	if strings.HasPrefix(rest, "?") {
		for query := rest[1:]; query != ""; {
			pair, next, _ := strings.Cut(query, "&")
			query = next
			rawKey, rawValue, found := strings.Cut(pair, "=")
			if !found || strings.Contains(rawValue, "=") {
				return urlParts{}, invalidPasswordDSN()
			}
			key, ok := decodePasswordURI(rawKey)
			if !ok {
				return urlParts{}, invalidPasswordDSN()
			}
			value, ok := decodePasswordURI(rawValue)
			if !ok {
				return urlParts{}, invalidPasswordDSN()
			}
			p.pairs = append(p.pairs, urlPair{raw: pair, key: key, rawValue: rawValue, value: value})
		}
	}
	return p, nil
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
