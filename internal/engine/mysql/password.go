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

	dsn, at, slash, err := splitNative(dsn)
	if err != nil {
		return "", err
	}
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

// splitNative locates the credentials of a go-sql-driver DSN. It returns dsn
// without leading whitespace (or "/" for a blank dsn), the offset of the @
// that ends the credentials (-1 without credentials) and the offset of the
// slash before the database.
func splitNative(dsn string) (trimmed string, at, slash int, err error) {
	// Match go-sql-driver/mysql v1.10.1's delimiter grammar: the last slash
	// separates the database, and the last @ before it separates credentials.
	// Passwords can contain '/', '@', and ':' without escaping in this format.
	if strings.TrimSpace(dsn) == "" {
		trimmed = "/"
	} else {
		// Match the engine's leading whitespace handling, but retain all suffix
		// bytes, including trailing whitespace that normal validation may trim.
		trimmed = strings.TrimLeftFunc(dsn, unicode.IsSpace)
	}
	slash = strings.LastIndexByte(trimmed, '/')
	if slash < 0 {
		return "", 0, 0, invalidDSN()
	}
	return trimmed, strings.LastIndexByte(trimmed[:slash], '@'), slash, nil
}
