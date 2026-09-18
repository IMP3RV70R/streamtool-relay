package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func validEnv() map[string]string {
	return map[string]string{
		"WORKER_SESSION_ID":        "123e4567-e89b-12d3-a456-426614174000",
		"WORKER_SOURCE_URL":        "srt://edge:8890?streamid=read:test",
		"WORKER_SOURCE_TOKEN_FILE": "/source",
		"WORKER_DESTINATIONS_FILE": "/destinations.json",
	}
}

func validFiles() map[string][]byte {
	return map[string][]byte{
		"/source":            []byte("source-token\n"),
		"/destination":       []byte("stream-key\n"),
		"/destinations.json": []byte(`[{"id":"primary","generation":1,"url":"rtmp://receiver:1935/live","secret_file":"/destination"}]`),
	}
}

func TestLoadValidAndDefaults(t *testing.T) {
	env := validEnv()
	files := validFiles()
	c, err := Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, func(path string) ([]byte, error) {
		return files[path], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.ReconnectMin != time.Second || c.ReconnectMax != 30*time.Second {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.ReconnectAttempts != 10 {
		t.Fatalf("reconnect attempts = %d", c.ReconnectAttempts)
	}
	if !c.SlateOnSourceLoss || c.SlateForced {
		t.Fatalf("unexpected slate defaults: %+v", c)
	}
	if got := c.Destinations[0].Location(); got != "rtmp://receiver:1935/live/stream-key" {
		t.Fatalf("destination = %q", got)
	}
	if got := c.SourceLocation(); !strings.Contains(got, "passphrase=source-token") {
		t.Fatalf("source location missing token: %q", got)
	}
	if strings.Contains(c.Destinations[0].Secret.String(), "stream-key") {
		t.Fatal("secret String leaked")
	}
	if rendered := fmt.Sprintf("%+v", c); strings.Contains(rendered, "stream-key") || strings.Contains(rendered, "source-token") {
		t.Fatalf("formatted config leaked a secret: %s", rendered)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	env := validEnv()
	files := validFiles()
	files["/destinations.json"] = []byte(`[{"id":"primary","generation":1,"url":"http://127.0.0.1","secret_file":"/destination"}]`)
	_, err := Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, func(path string) ([]byte, error) { return files[path], nil })
	if err == nil || !strings.Contains(err.Error(), "rtmp") {
		t.Fatalf("unexpected error: %v", err)
	}
	env = validEnv()
	env["WORKER_SESSION_ID"] = "not-a-uuid"
	_, err = Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, func(path string) ([]byte, error) { return validFiles()[path], nil })
	if err == nil || !strings.Contains(err.Error(), "UUID") {
		t.Fatalf("unexpected error: %v", err)
	}
	env = validEnv()
	env["WORKER_SLATE_FORCED"] = "sometimes"
	_, err = Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, func(path string) ([]byte, error) { return validFiles()[path], nil })
	if err == nil || !strings.Contains(err.Error(), "WORKER_SLATE_FORCED") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadDoesNotIncludeSecretOnReadError(t *testing.T) {
	env := validEnv()
	_, err := Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, func(string) ([]byte, error) { return nil, errors.New("denied") })
	if err == nil || strings.Contains(err.Error(), "stream-key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRequiresDestinationSnapshot(t *testing.T) {
	env := validEnv()
	delete(env, "WORKER_DESTINATIONS_FILE")
	env["WORKER_DESTINATION_URL"] = "rtmp://receiver:1935/live"
	env["WORKER_DESTINATION_SECRET_FILE"] = "/destination"
	_, err := Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, func(path string) ([]byte, error) { return validFiles()[path], nil })
	if err == nil || !strings.Contains(err.Error(), "WORKER_DESTINATIONS_FILE is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}
