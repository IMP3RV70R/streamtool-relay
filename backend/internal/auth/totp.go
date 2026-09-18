package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"time"
)

// RFC 6238: SHA-1, six digits, 30 seconds. SHA-1 is used within HMAC,
// not for password hashing. The secret contains 160 random bits.
func NewTOTPSecret() ([]byte, error) { b := make([]byte, 20); _, err := rand.Read(b); return b, err }
func TOTPSecret(secret []byte) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
}
func TOTPCode(secret []byte, step int64, digits int) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	modulus := uint32(1000000)
	if digits == 8 {
		modulus = 100000000
	}
	return fmt.Sprintf("%0*d", digits, value%modulus)
}
func VerifyTOTP(secret []byte, code string, now time.Time) (int64, bool) {
	if len(code) != 6 || len(secret) != 20 {
		return 0, false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	current := now.Unix() / 30
	for _, step := range []int64{current, current - 1, current + 1} {
		if step >= 0 && subtle.ConstantTimeCompare([]byte(TOTPCode(secret, step, 6)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}
