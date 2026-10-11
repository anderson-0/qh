package core

import (
	"bytes"
	"encoding/json"

	"github.com/anderson-0/qh/internal/engine"
)

// envelopePrefix starts every secret sealed since Plan 4. Older secrets are
// the raw connection string, which never starts with it.
var envelopePrefix = []byte(`{"v":2,`)

// envelope is the plaintext of a sealed secret: the connection string, the
// TLS and SSH settings (whose keys and passwords are secret too) and the
// secret references.
type envelope struct {
	V           int         `json:"v"`
	DSN         string      `json:"dsn"`
	TLS         *engine.TLS `json:"tls,omitempty"`
	SSH         *engine.SSH `json:"ssh,omitempty"`
	DSNRef      string      `json:"dsn_ref,omitempty"`
	PasswordRef string      `json:"password_ref,omitempty"`
}

func encodeSecret(in ConnInput) ([]byte, error) {
	return json.Marshal(envelope{V: 2, DSN: in.DSN, TLS: in.TLS, SSH: in.SSH, DSNRef: in.DSNRef, PasswordRef: in.PasswordRef})
}

func decodeSecret(pt []byte) (envelope, error) {
	if !bytes.HasPrefix(pt, envelopePrefix) {
		return envelope{DSN: string(pt)}, nil
	}
	var e envelope
	err := json.Unmarshal(pt, &e)
	return e, err
}
