package mysql

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/anderson-0/qh/internal/engine"
)

var _ engine.PasswordDSNSetter = Engine{}

// SetPassword edits credentials without resolving driver options. In particular,
// ParseDSN reads zone files for loc, and FormatDSN drops empty-user passwords.
func (Engine) SetPassword(dsn, password string) (string, error) {
	trimmed := strings.TrimSpace(dsn)
	if strings.IndexByte(dsn, 0) >= 0 || strings.IndexByte(password, 0) >= 0 {
		return "", invalidDSN()
	}
	if u, err := url.Parse(trimmed); err == nil && isURL(u) {
		// An opaque URL cannot carry userinfo in its serialized form.
		if u.Opaque != "" {
			return "", invalidDSN()
		}
		username := ""
		if u.User != nil {
			username = u.User.Username()
		}
		u.User = url.UserPassword(username, password)
		return u.String(), nil
	} else if strings.HasPrefix(trimmed, "mysql://") || strings.HasPrefix(trimmed, "mariadb://") {
		return "", invalidDSN()
	}

	// Match go-sql-driver/mysql v1.10.1's delimiter grammar: the last slash
	// separates the database, and the last @ before it separates credentials.
	// Passwords can contain '/', '@', and ':' without escaping in this format.
	if trimmed == "" {
		dsn = "/"
	} else {
		// Match the engine's leading whitespace handling, but retain all suffix
		// bytes, including trailing whitespace that normal validation may trim.
		dsn = strings.TrimLeftFunc(dsn, unicode.IsSpace)
	}
	slash := strings.LastIndexByte(dsn, '/')
	if slash < 0 {
		return "", invalidDSN()
	}
	at := strings.LastIndexByte(dsn[:slash], '@')
	username := ""
	if at >= 0 {
		username, _, _ = strings.Cut(dsn[:at], ":")
	}
	suffix := dsn[at+1:]
	transport := dsn[at+1 : slash]
	if open := strings.IndexByte(transport, '('); open >= 0 && !strings.HasSuffix(transport, ")") {
		return "", invalidDSN()
	}
	database, _, _ := strings.Cut(dsn[slash+1:], "?")
	if _, err := url.PathUnescape(database); err != nil {
		return "", invalidDSN()
	}
	return username + ":" + password + "@" + suffix, nil
}
