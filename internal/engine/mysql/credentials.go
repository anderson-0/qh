package mysql

import (
	"net/url"
	"strings"

	"github.com/anderson-0/qh/internal/engine"
)

var _ engine.CredentialExtractor = Engine{}

// Credentials returns the password written in dsn, as written and decoded,
// once each. It reads dsn as parseRaw does: a mysql:// or mariadb:// URL
// when url.Parse accepts it, otherwise the driver's native form, split as
// SetPassword splits it. It never calls ParseDSN, which reads zone files
// for loc. An opaque mysql: URL carries no password the driver uses, so it
// yields none. Query parameters are not credentials. Errors are fixed text.
func (Engine) Credentials(dsn string) ([]string, error) {
	trimmed := strings.TrimSpace(dsn)
	if u, err := url.Parse(trimmed); err == nil && isURL(u) {
		if u.User == nil {
			return nil, nil
		}
		password, ok := u.User.Password()
		if !ok || password == "" {
			return nil, nil
		}
		// url.Parse ends the authority at the first '/', '?' or '#', ends
		// the userinfo at its last '@' and the user name at its first ':'.
		authority := trimmed[len(u.Scheme)+len("://"):]
		if end := strings.IndexAny(authority, "/?#"); end >= 0 {
			authority = authority[:end]
		}
		_, raw, _ := strings.Cut(authority[:strings.LastIndexByte(authority, '@')], ":")
		if raw == password {
			return []string{raw}, nil
		}
		return []string{raw, password}, nil
	}
	dsn, at, _, err := splitNative(dsn)
	if err != nil {
		return nil, err
	}
	if at < 0 {
		return nil, nil
	}
	if _, password, _ := strings.Cut(dsn[:at], ":"); password != "" {
		return []string{password}, nil
	}
	return nil, nil
}
