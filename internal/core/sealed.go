package core

import (
	"bytes"
	"encoding/json"

	"github.com/anderson-0/qh/internal/engine"
)

// envelopePrefix starts every secret sealed since Plan 4. Older secrets are
// the raw connection string, which never starts with it.
var envelopePrefix = []byte(`{"v":2,`)

// envelope is the plaintext of a sealed secret: the connection string and
// the TLS settings, whose private key is secret too.
type envelope struct {
	V   int         `json:"v"`
	DSN string      `json:"dsn"`
	TLS *engine.TLS `json:"tls,omitempty"`
}

func encodeSecret(dsn string, tls *engine.TLS) ([]byte, error) {
	return json.Marshal(envelope{V: 2, DSN: dsn, TLS: tls})
}

func decodeSecret(pt []byte) (string, *engine.TLS, error) {
	if !bytes.HasPrefix(pt, envelopePrefix) {
		return string(pt), nil, nil
	}
	var e envelope
	if err := json.Unmarshal(pt, &e); err != nil {
		return "", nil, err
	}
	return e.DSN, e.TLS, nil
}
