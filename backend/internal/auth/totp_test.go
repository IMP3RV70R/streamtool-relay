package auth

import (
	"testing"
	"time"
)

func TestRFC6238SHA1Vectors(t *testing.T) {
	for _, v := range []struct {
		unix int64
		code string
	}{{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"}, {1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"}} {
		if got := TOTPCode([]byte("12345678901234567890"), v.unix/30, 8); got != v.code {
			t.Fatalf("%d: %s", v.unix, got)
		}
	}
}
func TestTOTPWindowAndFormat(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1234567890, 0)
	for _, offset := range []int64{-2, -1, 0, 1, 2} {
		step, ok := VerifyTOTP(secret, TOTPCode(secret, now.Unix()/30+offset, 6), now)
		if ok != (offset >= -1 && offset <= 1) || (ok && step != now.Unix()/30+offset) {
			t.Fatal(offset, step, ok)
		}
	}
	for _, code := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := VerifyTOTP(secret, code, now); ok {
			t.Fatal(code)
		}
	}
}
