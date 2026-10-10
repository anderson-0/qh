package engine

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
)

// DSNFields are connection details given one by one instead of as a
// connection string.
type DSNFields struct {
	Host     string
	Port     int // 0 for the engine's default port
	Database string
	User     string
	Password string
	Params   map[string]string
}

// DSNBuilder is implemented by engines that build a connection string from
// DSNFields. Errors never contain the password.
type DSNBuilder interface {
	BuildDSN(f DSNFields) (string, error)
}

// BuildURL builds scheme://user:password@host:port/database?params with every
// part escaped, for engines whose connection strings are URLs. IPv6 hosts
// are given without brackets.
func BuildURL(scheme string, f DSNFields) (string, error) {
	switch {
	case f.Host == "":
		return "", qerr.New(qerr.Invalid, "host is required")
	case strings.ContainsAny(f.Host, "/?#@[]\\ \t\r\n"):
		return "", qerr.New(qerr.Invalid, "host contains characters that are not allowed").
			WithHint("Give a host name or an IP address, without brackets or a port")
	case f.Port < 0 || f.Port > 65535:
		return "", qerr.New(qerr.Invalid, "port must be between 1 and 65535")
	case f.User == "" && f.Password != "":
		return "", qerr.New(qerr.Invalid, "a password needs a username")
	}
	host := f.Host
	if f.Port != 0 {
		host = net.JoinHostPort(f.Host, strconv.Itoa(f.Port))
	} else if strings.Contains(f.Host, ":") {
		host = "[" + f.Host + "]"
	}
	u := url.URL{Scheme: scheme, Host: host, Path: "/" + f.Database}
	switch {
	case f.Password != "":
		u.User = url.UserPassword(f.User, f.Password)
	case f.User != "":
		u.User = url.User(f.User)
	}
	if len(f.Params) > 0 {
		q := url.Values{}
		for k, v := range f.Params {
			q.Set(k, v)
		}
		// Encode writes a space as "+", which pgx does not read back as a
		// space. A literal "+" is already escaped as %2B, so every "+" left
		// in the output is a space.
		u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20") // sorted by key
	}
	return u.String(), nil
}
