package mongodb

import (
	"slices"
	"strings"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/redact"
)

var _ engine.CredentialExtractor = Engine{}

// Credentials returns the secrets written in dsn: the userinfo password, the
// value of each option in secretParams, and each AWS_SESSION_TOKEN entry of
// authMechanismProperties, each as written and decoded, once, in order. A
// value that decodes to empty is no secret, so it yields nothing. It reads
// dsn with parseURI, as Inspect does, so it never resolves SRV records or
// reads the files the options name. Errors are fixed text.
func (Engine) Credentials(dsn string) ([]string, error) {
	u, err := parseURI(dsn)
	if err != nil {
		return nil, badURI()
	}
	var out []string
	add := func(values ...string) {
		for _, v := range values {
			if v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	if u.hasPassword && u.password != "" {
		add(dsn[u.pwStart:u.pwEnd], u.password)
	}
	for _, o := range u.options {
		if !contains(secretParams, o.lkey) || o.value == "" {
			continue
		}
		_, raw, _ := strings.Cut(o.raw, "=")
		add(raw, o.value)
		if o.lkey != "authmechanismproperties" {
			continue
		}
		// The driver splits the properties on "," and each one at its
		// first ":". Other properties, such as SERVICE_NAME, are not
		// secrets on their own.
		for _, prop := range strings.Split(o.value, ",") {
			if k, v, ok := strings.Cut(prop, ":"); ok && strings.EqualFold(k, "AWS_SESSION_TOKEN") {
				add(v)
			}
		}
	}
	return out, nil
}

// cleaner returns the function that removes spec's secrets from text.
// core.OpenConn sets spec.Redact to the connection's full set: its
// connection string and the credentials in it, the password, and SSH and TLS
// secrets. Without it (engine tests), the set holds spec.DSN, spec.Password
// and Credentials(spec.DSN). Never both: a first pass with a smaller set
// could cut a longer secret that only the full set knows, and the second
// pass would then miss what is left of it.
func cleaner(spec engine.ConnSpec) (func(string) string, error) {
	if spec.Redact != nil {
		return spec.Redact, nil
	}
	creds, err := Engine{}.Credentials(spec.DSN)
	if err != nil {
		return nil, badURI()
	}
	return redact.New(append([]string{spec.DSN, spec.Password}, creds...)...).Text, nil
}
