package engine

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/anderson-0/qh/internal/tunnel/sshtest"
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
		"ssh.host is required":                  {User: "u", HostKey: fp, Password: "pw"},
		"ssh.user is required":                  {Host: "b", HostKey: fp, Password: "pw"},
		"ssh.port must be between 1 and 65535":  {Host: "b", Port: 70000, User: "u", HostKey: fp, Password: "pw"},
		"ssh.host_key must look like SHA256:":   {Host: "b", User: "u", HostKey: "MD5:aa", Password: "pw"},
		"exactly one of":                        {Host: "b", User: "u", HostKey: fp},
		"exactly one of ":                       {Host: "b", User: "u", HostKey: fp, Password: "pw", PrivateKey: key},
		"ssh.password and ssh.password_ref":     {Host: "b", User: "u", HostKey: fp, Password: "pw", PasswordRef: "env:X"},
		"invalid secret reference":              {Host: "b", User: "u", HostKey: fp, PasswordRef: "env:"},
		"is encrypted":                          {Host: "b", User: "u", HostKey: fp, PrivateKey: enc},
		"does not decrypt":                      {Host: "b", User: "u", HostKey: fp, PrivateKey: enc, Passphrase: "wrong"},
		"is not encrypted":                      {Host: "b", User: "u", HostKey: fp, PrivateKey: key, Passphrase: "pp"},
		"not a PEM private key":                 {Host: "b", User: "u", HostKey: fp, PrivateKey: "junk"},
		"the private key is larger than 16 KiB": {Host: "b", User: "u", HostKey: fp, PrivateKey: key + strings.Repeat("\n", 16<<10)},
		"passphrase applies only":               {Host: "b", User: "u", HostKey: fp, Password: "pw", Passphrase: "pp"},
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

// ValidateServer checks a tenant's key without decrypting it (the tunnel
// decrypts it once, when connecting) and applies server mode's limits.
func TestSSHValidateServer(t *testing.T) {
	key, enc := keyPEM(t, ""), keyPEM(t, "pp")
	for i, s := range []SSH{
		{Host: "b", User: "u", HostKey: fp, PrivateKey: key},
		{Host: "b", User: "u", HostKey: fp, PrivateKey: enc, Passphrase: "pp"},
		// Not decrypted here, so a wrong passphrase is found when connecting.
		{Host: "b", User: "u", HostKey: fp, PrivateKey: enc, Passphrase: "wrong"},
		{Host: "b", User: "u", HostKey: fp, PrivateKey: enc, PassphraseRef: "env:P"},
		{Host: "b", User: "u", HostKey: fp, Password: "pw"},
	} {
		if err := s.ValidateServer(); err != nil {
			t.Errorf("ok[%d]: %v", i, err)
		}
	}
	for want, s := range map[string]SSH{
		"is encrypted":            {Host: "b", User: "u", HostKey: fp, PrivateKey: enc},
		"is not encrypted":        {Host: "b", User: "u", HostKey: fp, PrivateKey: key, Passphrase: "pp"},
		"not a PEM private key":   {Host: "b", User: "u", HostKey: fp, PrivateKey: "junk"},
		"larger than 16 KiB":      {Host: "b", User: "u", HostKey: fp, PrivateKey: key + strings.Repeat("\n", 16<<10)},
		"KDF uses 2048 rounds":    {Host: "b", User: "u", HostKey: fp, PrivateKey: sshtest.KeyWithRounds(2048), Passphrase: "pp"},
		"8704-bit RSA key":        {Host: "b", User: "u", HostKey: fp, PrivateKey: sshtest.RSA8704},
		"ssh.user is required":    {Host: "b", HostKey: fp, Password: "pw"},
		"passphrase applies only": {Host: "b", User: "u", HostKey: fp, Password: "pw", Passphrase: "pp"},
	} {
		err := s.ValidateServer()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", want, err)
		}
	}
	// Local mode still decrypts early, and accepts what server mode caps.
	if err := (&SSH{Host: "b", User: "u", HostKey: fp, PrivateKey: enc, Passphrase: "wrong"}).Validate(); err == nil || !strings.Contains(err.Error(), "does not decrypt") {
		t.Errorf("local wrong passphrase: %v", err)
	}
	if err := (&SSH{Host: "b", User: "u", HostKey: fp, PrivateKey: sshtest.RSA8704}).Validate(); err != nil {
		t.Errorf("local big RSA key: %v", err)
	}
}
