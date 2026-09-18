package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

type KeyProvider interface {
	Key(context.Context, string) ([]byte, error)
	CurrentKeyID(context.Context) (string, error)
}
type Envelope struct {
	KeyID             string `json:"key_id"`
	Nonce, Ciphertext []byte
}

func Encrypt(ctx context.Context, p KeyProvider, plaintext, aad []byte) (Envelope, error) {
	id, err := p.CurrentKeyID(ctx)
	if err != nil {
		return Envelope{}, err
	}
	key, err := p.Key(ctx, id)
	if err != nil {
		return Envelope{}, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return Envelope{}, err
	}
	return Envelope{KeyID: id, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plaintext, aad)}, nil
}
func Decrypt(ctx context.Context, p KeyProvider, e Envelope, aad []byte) ([]byte, error) {
	key, err := p.Key(ctx, e.KeyID)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(e.Nonce) != aead.NonceSize() {
		return nil, errors.New("invalid ciphertext nonce")
	}
	plaintext, err := aead.Open(nil, e.Nonce, e.Ciphertext, aad)
	if err != nil {
		return nil, errors.New("decrypt destination secret")
	}
	return plaintext, nil
}
func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("envelope key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

type LocalProvider struct {
	ID   string
	Keys map[string][]byte
}

func (p LocalProvider) CurrentKeyID(context.Context) (string, error) {
	if len(p.Keys[p.ID]) == 0 {
		return "", errors.New("current key unavailable")
	}
	return p.ID, nil
}
func (p LocalProvider) Key(_ context.Context, id string) ([]byte, error) {
	key := p.Keys[id]
	if len(key) == 0 {
		return nil, errors.New("key unavailable")
	}
	return key, nil
}
