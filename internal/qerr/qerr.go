// Package qerr defines the classified errors agents see as one line:
// error[<category>]: <message>. <hint>
package qerr

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// lineBreaks matches whitespace runs containing a line break or tab.
var lineBreaks = regexp.MustCompile(`[ \t\r\n]*[\r\n\t][ \t\r\n]*`)

func oneLine(s string) string { return lineBreaks.ReplaceAllString(s, " ") }

// Category is the machine-readable error class.
type Category string

const (
	Auth             Category = "auth"
	Forbidden        Category = "forbidden"
	UnknownSource    Category = "unknown_source"
	Invalid          Category = "invalid"
	ReadOnly         Category = "read_only"
	Timeout          Category = "timeout"
	ConnectionFailed Category = "connection_failed"
	QueryError       Category = "query_error"
	Busy             Category = "busy"
)

// Error is a classified error.
type Error struct {
	Category Category
	Message  string
	Hint     string
	// UnknownColumn and UnknownTable carry the identifier the database could
	// not resolve, so the core can add suggestions.
	UnknownColumn string
	UnknownTable  string
	// Err is the underlying error. It is never shown to agents.
	Err error
}

// New returns an Error with no underlying cause.
func New(c Category, msg string) *Error { return &Error{Category: c, Message: msg} }

// Wrap returns an Error that wraps err.
func Wrap(c Category, msg string, err error) *Error {
	return &Error{Category: c, Message: msg, Err: err}
}

// WithHint sets the hint and returns e.
func (e *Error) WithHint(h string) *Error {
	e.Hint = h
	return e
}

func (e *Error) Error() string {
	msg := strings.TrimRight(strings.TrimSpace(oneLine(e.Message)), ".")
	s := fmt.Sprintf("error[%s]: %s", e.Category, msg)
	if h := strings.TrimSpace(oneLine(e.Hint)); h != "" {
		s += ". " + h
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// From returns err as an *Error, classifying unknown errors as query_error.
func From(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Wrap(QueryError, err.Error(), err)
}
