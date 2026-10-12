package engine

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func keyPEM(t *testing.T, passphrase string) string {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	var b *pem.Block
	var err error
	if passphrase == "" {
		b, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		b, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(b))
}

const fp = "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"

func TestSSHValidate(t *testing.T) {
	key, enc := keyPEM(t, ""), keyPEM(t, "pp")
	ok := []SSH{
		{Host: "bastion", User: "u", HostKey: fp, PrivateKey: key},
		{Host: "bastion", User: "u", HostKey: fp, PrivateKey: enc, Passphrase: "pp"},
		{Host: "bastion", User: "u", HostKey: fp, PrivateKey: enc, PassphraseRef: "env:QH_PP"},
		{Host: "bastion", User: "u", HostKey: fp, PrivateKeyRef: "file:/k", PassphraseRef: "env:P"},
		{Host: "bastion", User: "u", HostKey: fp, Password: "pw"},
		{Host: "bastion", Port: 2222, User: "u", HostKey: fp, PasswordRef: "aws-sm://x#pw"},
		{Host: "bastion", User: "u", PrivateKey: key}, // host key may be empty (fingerprint flow)
	}
	for i, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("ok[%d]: %v", i, err)
		}
	}
	bad := map[string]SSH{
		"ssh.host is required":                 {User: "u", HostKey: fp, Password: "pw"},
		"ssh.user is required":                 {Host: "b", HostKey: fp, Password: "pw"},
		"ssh.port must be between 1 and 65535": {Host: "b", Port: 70000, User: "u", HostKey: fp, Password: "pw"},
		"ssh.host_key must look like SHA256:":  {Host: "b", User: "u", HostKey: "MD5:aa", Password: "pw"},
		"exactly one of":                       {Host: "b", User: "u", HostKey: fp},
		"exactly one of ":                      {Host: "b", User: "u", HostKey: fp, Password: "pw", PrivateKey: key},
		"ssh.password and ssh.password_ref":    {Host: "b", User: "u", HostKey: fp, Password: "pw", PasswordRef: "env:X"},
		"invalid secret reference":             {Host: "b", User: "u", HostKey: fp, PasswordRef: "env:"},
		"is encrypted":                         {Host: "b", User: "u", HostKey: fp, PrivateKey: enc},
		"does not decrypt":                     {Host: "b", User: "u", HostKey: fp, PrivateKey: enc, Passphrase: "wrong"},
		"is not encrypted":                     {Host: "b", User: "u", HostKey: fp, PrivateKey: key, Passphrase: "pp"},
		"not a PEM private key":                {Host: "b", User: "u", HostKey: fp, PrivateKey: "junk"},
		"passphrase applies only":              {Host: "b", User: "u", HostKey: fp, Password: "pw", Passphrase: "pp"},
	}
	for want, s := range bad {
		err := s.Validate()
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(want)) {
			t.Errorf("%q: %v", want, err)
		}
		if err != nil && (strings.Contains(err.Error(), "BEGIN OPENSSH") || strings.Contains(err.Error(), "wrong")) {
			t.Errorf("%q leaked a secret: %v", want, err)
		}
	}
}

func TestSSHInfoAndRefs(t *testing.T) {
	s := &SSH{Host: "b", User: "u", HostKey: fp, PrivateKeyRef: "file:/k", PassphraseRef: "env:P"}
	i := s.Info()
	if i.Auth != "key" || i.Port != 22 || i.Refs["private_key"] != "file:/k" || i.Refs["passphrase"] != "env:P" {
		t.Fatalf("%+v", i)
	}
	if got := strings.Join(s.RefStrings(), ","); got != "file:/k,env:P" {
		t.Fatal(got)
	}
	if s.Addr() != "b:22" || (&SSH{Host: "::1", Port: 2222}).Addr() != "[::1]:2222" {
		t.Fatal(s.Addr())
	}
	if (*SSH)(nil).Info() != nil {
		t.Fatal("nil info")
	}
	if (&SSH{Host: "b", User: "u", Password: "x"}).Info().Auth != "password" {
		t.Fatal("password auth")
	}
}
