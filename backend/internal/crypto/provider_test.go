package crypto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestRotationPreservesExistingCiphertext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	oldFile, newFile := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	os.WriteFile(oldFile, []byte(base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))), 0600)
	os.WriteFile(newFile, []byte(base64.StdEncoding.EncodeToString([]byte("abcdefghijklmnopqrstuvwxyz123456"))), 0600)
	write := func(current string, refs map[string]KeyReference) {
		b, _ := json.Marshal(KeyManifest{Current: current, Keys: refs})
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	refs := map[string]KeyReference{"local-v1": {File: oldFile}}
	write("local-v1", refs)
	provider, err := NewVersionedProvider(path, "file")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	aad := []byte("source:output")
	encrypted, err := Encrypt(ctx, provider, []byte("existing-secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	refs["v2"] = KeyReference{File: newFile}
	write("v2", refs)
	current, err := Encrypt(ctx, provider, []byte("new-secret"), aad)
	if err != nil || current.KeyID != "v2" {
		t.Fatal("write rotation failed", err)
	}
	plain, err := Decrypt(ctx, provider, encrypted, aad)
	if err != nil || string(plain) != "existing-secret" {
		t.Fatal("old key lost", err)
	}
	refs["local-v1"] = KeyReference{File: newFile}
	write("v2", refs)
	if _, err = provider.Key(ctx, "local-v1"); err == nil {
		t.Fatal("immutable key ID rebound")
	}
}
func TestLockboxVersionAndWorkloadIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	b, _ := json.Marshal(KeyManifest{Current: "v2", Keys: map[string]KeyReference{"v2": {SecretID: "secret", VersionID: "version", Entry: "envelope"}}})
	os.WriteFile(path, b, 0600)
	version := "version"
	status := 200
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			if r.Header.Get("Metadata-Flavor") != "Google" {
				t.Error("workload header missing")
			}
			w.Write([]byte(`{"access_token":"workload-token"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer workload-token" || r.URL.Query().Get("versionId") != "version" {
			t.Error("version or credentials missing")
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"versionId": version, "entries": []map[string]string{{"key": "envelope", "textValue": base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))}}})
	}))
	defer upstream.Close()
	makeProvider := func() *VersionedProvider {
		p, err := NewVersionedProvider(path, "lockbox")
		if err != nil {
			t.Fatal(err)
		}
		p.metadataURL = upstream.URL + "/metadata"
		p.payloadURL = upstream.URL + "/secrets/"
		return p
	}
	if key, err := makeProvider().Key(ctx, "v2"); err != nil || len(key) != 32 {
		t.Fatal("Lockbox key unavailable", err)
	}
	version = "other"
	if _, err := makeProvider().Key(ctx, "v2"); err == nil {
		t.Fatal("wrong version accepted")
	}
	version = "version"
	status = 403
	if _, err := makeProvider().Key(ctx, "v2"); err == nil {
		t.Fatal("IAM denial hidden")
	}
}
