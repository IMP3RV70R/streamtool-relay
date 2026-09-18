package crypto

import (
	"bytes"
	"context"
	"testing"
)

func TestEnvelope(t *testing.T) {
	p := LocalProvider{ID: "v1", Keys: map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32)}}
	e, err := Encrypt(context.Background(), p, []byte("secret"), []byte("destination"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(context.Background(), p, e, []byte("destination"))
	if err != nil || string(got) != "secret" {
		t.Fatal(err, string(got))
	}
	if bytes.Contains(e.Ciphertext, []byte("secret")) {
		t.Fatal("plaintext leaked")
	}
	for _, nonce := range [][]byte{nil, {1}, bytes.Repeat([]byte{1}, 64)} {
		corrupt := e
		corrupt.Nonce = nonce
		if _, err := Decrypt(context.Background(), p, corrupt, []byte("destination")); err == nil {
			t.Fatal("invalid nonce accepted")
		}
	}
}
