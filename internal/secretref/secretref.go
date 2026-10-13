// Package secretref resolves secret references (env:NAME, file:/path,
// aws-sm://id[#key]) and, in server mode, holds the operator's allow-list.
// Resolved values are never put in errors.
package secretref

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
)

// Ref is a parsed secret reference.
type Ref struct {
	Raw    string // as written
	Scheme string // env, file or aws-sm
	Name   string // variable name, absolute path, or AWS secret id
	Key    string // aws-sm: JSON field of the secret, or ""
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const awsPrefix = "aws-sm://"

func invalid(s, msg string) error {
	return qerr.New(qerr.Invalid, fmt.Sprintf("invalid secret reference %q: %s", s, msg)).
		WithHint("Use env:NAME, file:/absolute/path or aws-sm://<secret-id>[#key]")
}

// Parse reads a reference without resolving it.
func Parse(s string) (Ref, error) {
	switch {
	case strings.HasPrefix(s, "env:"):
		if n := s[len("env:"):]; envName.MatchString(n) {
			return Ref{Raw: s, Scheme: "env", Name: n}, nil
		}
		return Ref{}, invalid(s, "not an environment variable name")
	case strings.HasPrefix(s, "file:"):
		if p := s[len("file:"):]; filepath.IsAbs(p) {
			return Ref{Raw: s, Scheme: "file", Name: p}, nil
		}
		return Ref{}, invalid(s, "the path must be absolute")
	case strings.HasPrefix(s, awsPrefix):
		id, key, hasKey := strings.Cut(s[len(awsPrefix):], "#")
		if id == "" {
			return Ref{}, invalid(s, "the secret id is empty")
		}
		if hasKey && key == "" {
			return Ref{}, invalid(s, "the key after # is empty")
		}
		return Ref{Raw: s, Scheme: "aws-sm", Name: id, Key: key}, nil
	}
	return Ref{}, invalid(s, "unknown scheme")
}

// Resolver returns the value a reference points to.
type Resolver interface {
	Resolve(ctx context.Context, r Ref) (string, error)
}

// Set resolves references; when restricted (server mode) only those matching
// an allowed pattern.
type Set struct {
	restricted bool
	allow      []*regexp.Regexp
	resolvers  map[string]Resolver
}

// NewSet returns a Set with the env, file and aws-sm resolvers. Patterns
// use * for any run of characters; check them first with CheckPattern.
func NewSet(restricted bool, allow []string) *Set {
	s := &Set{restricted: restricted, resolvers: map[string]Resolver{
		"env": envResolver{}, "file": fileResolver{}, "aws-sm": newAWS(),
	}}
	for _, p := range allow {
		s.allow = append(s.allow, glob(p))
		if c := canonicalFile(p); c != p {
			s.allow = append(s.allow, glob(c))
		}
	}
	return s
}

// canonicalFile rewrites the directory part of a file: pattern with its
// symlinks resolved, so a pattern under a symlinked root (macOS /var) still
// matches the resolved paths Check compares. It returns p unchanged when that
// is not possible.
func canonicalFile(p string) string {
	if !strings.HasPrefix(p, "file:") {
		return p
	}
	rest := p[len("file:"):]
	if !filepath.IsAbs(rest) {
		return p
	}
	i := strings.IndexByte(rest, '*')
	if i < 0 {
		i = len(rest)
	}
	lit, suffix := rest[:i], rest[i:]
	dir := lit[:strings.LastIndexByte(lit, '/')+1]
	if dir == "" || dir == "/" {
		return p
	}
	resolved, err := filepath.EvalSymlinks(strings.TrimSuffix(dir, "/"))
	if err != nil {
		return p
	}
	return "file:" + resolved + "/" + lit[len(dir):] + suffix
}

// With replaces the resolver for scheme (tests).
func (s *Set) With(scheme string, r Resolver) *Set {
	s.resolvers[scheme] = r
	return s
}

// CheckPattern validates an --allow-secret-ref pattern.
func CheckPattern(p string) error {
	if strings.HasPrefix(p, "file:") && !filepath.IsAbs(p[len("file:"):]) {
		return qerr.New(qerr.Invalid, fmt.Sprintf("invalid --allow-secret-ref pattern %q", p)).
			WithHint("file: patterns need an absolute path, e.g. file:/run/secrets/*")
	}
	for _, prefix := range []string{"env:", "file:", awsPrefix} {
		if strings.HasPrefix(p, prefix) && len(p) > len(prefix) {
			return nil
		}
	}
	return qerr.New(qerr.Invalid, fmt.Sprintf("invalid --allow-secret-ref pattern %q", p)).
		WithHint("Start it with env:, file: or aws-sm://, e.g. env:TENANT_DB_*")
}

func glob(p string) *regexp.Regexp {
	parts := strings.Split(p, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
}

func (s *Set) allowed(raw string) bool {
	for _, re := range s.allow {
		if re.MatchString(raw) {
			return true
		}
	}
	return false
}

func forbidden(raw string) error {
	return qerr.New(qerr.Forbidden, "secret reference "+raw+" is not allowed on this server").
		WithHint("The operator can allow it with qh serve --allow-secret-ref")
}

// Check parses raw and, in server mode, applies the allow-list. For file:
// the cleaned path and, when the file exists, its symlink-resolved path must
// both match.
func (s *Set) Check(raw string) (Ref, error) {
	r, err := Parse(raw)
	if err != nil || !s.restricted {
		return r, err
	}
	if !s.allowed(raw) {
		return r, forbidden(raw)
	}
	if r.Scheme == "file" {
		clean := filepath.Clean(r.Name)
		if !s.allowed("file:" + clean) {
			return r, forbidden(raw)
		}
		if real, err := filepath.EvalSymlinks(clean); err == nil && !s.allowed("file:"+real) {
			return r, forbidden(raw)
		}
	}
	return r, nil
}

// Resolve checks raw and returns its value, within 10 seconds.
func (s *Set) Resolve(ctx context.Context, raw string) (string, error) {
	r, err := s.Check(raw)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, err := s.resolvers[r.Scheme].Resolve(ctx, r)
	if err != nil {
		var qe *qerr.Error
		if errors.As(err, &qe) {
			return "", qe
		}
		return "", failed(raw, err.Error())
	}
	return v, nil
}

// failed is a resolution error; reason must never contain the value.
func failed(raw, reason string) error {
	return qerr.New(qerr.ConnectionFailed, "secret reference "+raw+": "+reason)
}

type envResolver struct{}

func (envResolver) Resolve(_ context.Context, r Ref) (string, error) {
	if v := os.Getenv(r.Name); v != "" {
		return v, nil
	}
	return "", failed(r.Raw, "not set")
}

const maxFile = 64 << 10

type fileResolver struct{}

func (fileResolver) Resolve(_ context.Context, r Ref) (string, error) {
	f, err := os.Open(filepath.Clean(r.Name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", failed(r.Raw, "no such file")
	case err != nil:
		return "", failed(r.Raw, "cannot read the file")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return "", failed(r.Raw, "cannot read the file")
	}
	if len(b) > maxFile {
		return "", failed(r.Raw, "the file is larger than 64 KiB")
	}
	v := strings.TrimSuffix(string(b), "\n")
	return strings.TrimSuffix(v, "\r"), nil
}
