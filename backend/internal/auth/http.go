package auth

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"strings"
)

func ReadToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 24 {
		return "", errors.New("token must contain at least 24 characters")
	}
	return token, nil
}
func Matches(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
func Bearer(r *http.Request, token string) bool {
	return Matches(r.Header.Get("Authorization"), "Bearer "+token) && token != ""
}
