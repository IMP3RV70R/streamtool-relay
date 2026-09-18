package auth

import (
	stdpbkdf2 "crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
)

const (
	iterations = 210000
	saltSize   = 16
	keySize    = 32
)

func HashKey(key string) ([]byte, error) {
	if len(key) < 16 {
		return nil, errors.New("ingest key must be at least 16 characters")
	}
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	derived := pbkdf2([]byte(key), salt, iterations, keySize)
	out := make([]byte, 4+saltSize+keySize)
	binary.BigEndian.PutUint32(out, uint32(iterations))
	copy(out[4:], salt)
	copy(out[4+saltSize:], derived)
	return out, nil
}
func VerifyKey(encoded []byte, key string) bool {
	if len(encoded) != 4+saltSize+keySize || len(key) < 16 || len(key) > 256 {
		return false
	}
	n := int(binary.BigEndian.Uint32(encoded))
	if n < 100000 || n > 1000000 {
		return false
	}
	want := encoded[4+saltSize:]
	got := pbkdf2([]byte(key), encoded[4:4+saltSize], n, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}
func pbkdf2(password, salt []byte, n, size int) []byte {
	result, _ := stdpbkdf2.Key(sha256.New, string(password), salt, n, size)
	return result
}
