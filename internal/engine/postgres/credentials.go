package postgres

import (
	"slices"
	"strings"

	"github.com/anderson-0/qh/internal/engine"
)

var _ engine.CredentialExtractor = Engine{}

// Credentials returns the secrets written in dsn: the userinfo password and
// every setting whose key contains "password" (password, sslpassword), each
// as written and decoded, once, in order. A value that decodes to empty is
// no password, so it yields nothing. It reuses SetPassword's grammar and
// never calls ParseConfig, which reads PGPASSWORD, ~/.pgpass and other
// files. Errors are fixed text.
func (Engine) Credentials(dsn string) ([]string, error) {
	if strings.IndexByte(dsn, 0) >= 0 {
		return nil, invalidPasswordDSN()
	}
	var out []string
	add := func(values ...string) {
		for _, v := range values {
			if v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	if isURLDSN(dsn) {
		p, err := parseURLParts(dsn)
		if err != nil {
			return nil, err
		}
		if p.pwStart >= 0 && p.password != "" {
			add(dsn[p.pwStart:p.pwEnd], p.password)
		}
		for _, q := range p.pairs {
			if isPasswordKey(q.key) && q.value != "" {
				add(q.rawValue, q.value)
			}
		}
		return out, nil
	}
	// Unlike SetPassword, accept keys with interior whitespace: a secret
	// they carry is still found. A key holding "://" is a URL that pgx read
	// as keyword/value (a leading space, or a scheme in another case): its
	// password sits in a parameter name, which the server quotes. Such a
	// string never connects, so fail closed.
	urlKey := false
	err := scanKeywords(dsn, func(key, raw, inner, value string) {
		if strings.Contains(key, "://") {
			urlKey = true
		}
		if isPasswordKey(key) && value != "" {
			add(raw, inner, value)
		}
	})
	if err != nil || urlKey {
		return nil, invalidPasswordDSN()
	}
	return out, nil
}

func isPasswordKey(key string) bool {
	return strings.Contains(strings.ToLower(key), "password")
}
