package mongodb

import (
	"net/url"
	"strings"

	"github.com/anderson-0/qh/internal/engine"
)

var _ engine.PasswordDSNSetter = Engine{}

// SetPassword edits only the URI's inline database password. The syntax-only
// parser avoids the driver's SRV resolution and TLS file loading, and its byte
// offsets let us retain the encoded username, topology and options verbatim.
func (Engine) SetPassword(dsn, password string) (string, error) {
	u, err := parseURI(dsn)
	if err != nil {
		return "", err
	}
	if password != "" && u.user == "" {
		return "", badURI()
	}
	// Userinfo is PathUnescaped by the MongoDB parser, so spaces must use
	// %20 rather than QueryEscape's '+'. Escape all reserved bytes as well.
	encoded := strings.ReplaceAll(url.QueryEscape(password), "+", "%20")
	if u.hasPassword {
		return dsn[:u.pwStart] + encoded + dsn[u.pwEnd:], nil
	}
	if password == "" {
		return dsn, nil
	}
	// A nonempty parsed username guarantees userinfo. Match parseURI's first
	// '@' boundary, including when the raw username contains a '?'.
	at := strings.IndexByte(dsn, '@')
	return dsn[:at] + ":" + encoded + dsn[at:], nil
}
