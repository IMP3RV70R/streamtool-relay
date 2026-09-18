package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"streamtool-relay/internal/release"
)

func TestActualLocalBundle(t *testing.T) {
	bundle := os.Getenv("STREAMTOOL_RELEASE_TEST_BUNDLE")
	if bundle == "" {
		t.Skip("optional local immutable release bundle not supplied")
	}
	version := os.Getenv("STREAMTOOL_RELEASE_TEST_VERSION")
	architecture := os.Getenv("STREAMTOOL_RELEASE_TEST_ARCHITECTURE")
	schema, schemaErr := strconv.Atoi(os.Getenv("STREAMTOOL_RELEASE_TEST_SCHEMA"))
	if version == "" || (architecture != "arm64" && architecture != "amd64") || schemaErr != nil || schema < 1 {
		t.Fatal("actual bundle fixture requires explicit version, architecture and schema")
	}
	source, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, source)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "signing-seed")
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(private.Seed())), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := release.Manifest{TargetSchema: schema, Sequence: 9, Version: version, Channel: "stable", Created: now.Add(-time.Minute), Expires: now.Add(time.Hour), Protocol: release.Protocol, MaximumSchema: schema, Artifacts: []release.Artifact{{Architecture: architecture, URL: "https://releases.example.invalid/relay.tar.gz", Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}}}
	raw, _ := json.Marshal(manifest)
	manifestPath := filepath.Join(root, "manifest.json")
	signature := filepath.Join(root, "manifest.sig")
	_ = os.WriteFile(manifestPath, raw, 0600)
	if err := run([]string{"sign", "--key", key, "--manifest", manifestPath, "--signature", signature}); err != nil {
		t.Fatal(err)
	}
	configuration, _ := json.Marshal(trust{PublicKey: base64.StdEncoding.EncodeToString(public), Channel: "stable", ArtifactOrigin: "https://releases.example.invalid"})
	trustPath := filepath.Join(root, "trust.json")
	_ = os.WriteFile(trustPath, configuration, 0600)
	state := filepath.Join(root, "watermark.json")
	stage := filepath.Join(root, "stage")
	arguments := []string{"verify", "--manifest", manifestPath, "--signature", signature, "--trust", trustPath, "--state", state, "--bundle", bundle, "--destination", stage, "--architecture", architecture, "--initial"}
	if err := run(arguments); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(filepath.Join(stage, "VERSION"))
	if err != nil || strings.TrimSpace(string(installed)) != manifest.Version {
		t.Fatal("actual archive version not preserved")
	}
	stamp, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var watermark release.Watermark
	if err := release.Decode(stamp, &watermark); err != nil || watermark.Sequence != 9 {
		t.Fatal("watermark not durable")
	}
	// Identical retry must not overwrite an existing staged release.
	if err := run(arguments); err == nil {
		t.Fatal("existing stage overwritten")
	}
	manifest.Notes = "changed content for same sequence"
	raw, _ = json.Marshal(manifest)
	_ = os.WriteFile(manifestPath, raw, 0600)
	_ = os.Remove(signature)
	if err := run([]string{"sign", "--key", key, "--manifest", manifestPath, "--signature", signature}); err != nil {
		t.Fatal(err)
	}
	if err := run(arguments); err == nil {
		t.Fatal("equivocation accepted by CLI")
	}
	after, err := os.ReadFile(state)
	if err != nil || string(after) != string(stamp) {
		t.Fatal("rejected metadata replaced watermark")
	}
}
func TestSigningKeyMustBePrivateAndSignatureNotOverwritten(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "seed")
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	_ = os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(private.Seed())), 0644)
	data := filepath.Join(root, "manifest")
	_ = os.WriteFile(data, []byte(`{"sequence":1}`), 0600)
	signature := filepath.Join(root, "signature")
	arguments := []string{"sign", "--key", key, "--manifest", data, "--signature", signature}
	if err := run(arguments); err == nil {
		t.Fatal("publicly readable signing seed accepted")
	}
	_ = os.Chmod(key, 0600)
	if err := run(arguments); err != nil {
		t.Fatal(err)
	}
	if err := run(arguments); err == nil {
		t.Fatal("signature overwritten")
	}
}

func verificationFixture(t *testing.T) ([]string, string, string, ed25519.PrivateKey, release.Manifest) {
	t.Helper()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	configuration, _ := json.Marshal(trust{base64.StdEncoding.EncodeToString(public), "stable", "https://releases.example.invalid"})
	trustPath := filepath.Join(root, "trust")
	_ = os.WriteFile(trustPath, configuration, 0600)
	data := []byte("not a tar archive")
	sum := sha256.Sum256(data)
	bundle := filepath.Join(root, "bundle")
	_ = os.WriteFile(bundle, data, 0600)
	now := time.Now().UTC()
	manifest := release.Manifest{TargetSchema: 6, Sequence: 9, Version: "test", Channel: "stable", Created: now.Add(-time.Minute), Expires: now.Add(time.Hour), Protocol: 1, MaximumSchema: 6, Artifacts: []release.Artifact{{Architecture: "arm64", URL: "https://releases.example.invalid/bundle", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
	manifestPath := filepath.Join(root, "manifest")
	raw, _ := json.Marshal(manifest)
	_ = os.WriteFile(manifestPath, raw, 0600)
	signature := filepath.Join(root, "signature")
	_ = os.WriteFile(signature, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, raw))), 0600)
	state := filepath.Join(root, "watermark")
	return []string{"verify", "--manifest", manifestPath, "--signature", signature, "--trust", trustPath, "--state", state, "--bundle", bundle, "--destination", filepath.Join(root, "stage"), "--architecture", "arm64", "--initial"}, root, state, private, manifest
}
func TestFailedStagingKeepsDurableWatermarkAndRejectsOlderFeed(t *testing.T) {
	arguments, root, state, key, manifest := verificationFixture(t)
	if err := run(arguments); err == nil {
		t.Fatal("invalid archive accepted")
	}
	stamp, err := os.ReadFile(state)
	if err != nil {
		t.Fatal("valid signed metadata watermark lost", err)
	}
	var accepted release.Watermark
	if err := release.Decode(stamp, &accepted); err != nil || accepted.Sequence != 9 {
		t.Fatal("incorrect watermark")
	}
	manifest.Sequence = 8
	raw, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(root, "manifest"), raw, 0600)
	_ = os.WriteFile(filepath.Join(root, "signature"), []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw))), 0600)
	if err := run(arguments); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatal("older feed not rejected before staging", err)
	}
	after, _ := os.ReadFile(state)
	if string(after) != string(stamp) {
		t.Fatal("rejected feed rewound watermark")
	}
	if _, err := os.Stat(filepath.Join(root, "stage")); !os.IsNotExist(err) {
		t.Fatal("partial stage retained")
	}
}
func TestCorruptWatermarkAndUntrustedDirectoriesFailClosed(t *testing.T) {
	arguments, root, state, _, _ := verificationFixture(t)
	_ = os.WriteFile(state, []byte("corrupt state"), 0600)
	if err := run(arguments); err == nil || !strings.Contains(err.Error(), "watermark") {
		t.Fatal("corrupt watermark ignored", err)
	}
	_ = os.Remove(state)
	_ = os.Chmod(root, 0755)
	if err := run(arguments); err == nil || !strings.Contains(err.Error(), "private directory") {
		t.Fatal("public staging directory accepted", err)
	}
	_ = os.Chmod(root, 0700)
	_ = os.Chmod(filepath.Join(root, "trust"), 0666)
	if err := run(arguments); err == nil || !strings.Contains(err.Error(), "trust configuration") {
		t.Fatal("writable trust configuration accepted", err)
	}
}

func TestMetadataRevalidationDoesNotStageOrExecuteArtifact(t *testing.T) {
	arguments, root, state, _, _ := verificationFixture(t)
	arguments = append(arguments, "--metadata-only")
	if err := run(arguments); err != nil {
		t.Fatal("metadata revalidation failed", err)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatal("watermark not durable")
	}
	if _, err := os.Stat(filepath.Join(root, "stage")); !os.IsNotExist(err) {
		t.Fatal("metadata-only command touched bundle stage")
	}
}
