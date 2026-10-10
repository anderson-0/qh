package engine

import (
	"strings"
	"testing"
)

func TestBuildURLRejects(t *testing.T) {
	for _, c := range []struct {
		f    DSNFields
		want string
	}{
		{DSNFields{Database: "app"}, "host is required"},
		{DSNFields{Host: "db", Port: 70000}, "port must be between 1 and 65535"},
		{DSNFields{Host: "db", Password: "secret-pw"}, "a password needs a username"},
		{DSNFields{Host: "db/evil"}, "host contains characters that are not allowed"},
		{DSNFields{Host: "u@evil"}, "host contains characters that are not allowed"},
		{DSNFields{Host: "[::1]"}, "host contains characters that are not allowed"},
	} {
		_, err := BuildURL("postgres", c.f)
		if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: "+c.want) {
			t.Errorf("%+v: %v", c.f, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-pw") {
			t.Errorf("error leaks the password: %v", err)
		}
	}
}

func TestBuildURL(t *testing.T) {
	got, err := BuildURL("mysql", DSNFields{Host: "::1", Port: 3307, Database: "app", User: "u", Password: "p w", Params: map[string]string{"b": "2", "a": "1"}})
	if err != nil || got != "mysql://u:p%20w@[::1]:3307/app?a=1&b=2" {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := BuildURL("postgres", DSNFields{Host: "::1"}); got != "postgres://[::1]/" {
		t.Fatalf("%q", got)
	}
}
