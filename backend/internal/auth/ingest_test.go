package auth

import "testing"

func TestHashKey(t *testing.T) {
	hash, err := HashKey("long-test-ingest-key")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyKey(hash, "long-test-ingest-key") || VerifyKey(hash, "wrong-key-xxxxxxxx") {
		t.Fatal("verification mismatch")
	}
}
