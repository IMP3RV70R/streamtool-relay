package updaterclient

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The optional probe runs as UID 65532 against a root Python server in an isolated
// Linux container. The catalog/wake are fixtures; this is real IPC/peer acceptance.
func TestBridgeInterop(t *testing.T) {
	path := os.Getenv("STREAMTOOL_BRIDGE_INTEROP_SOCKET")
	if path == "" {
		t.Skip("isolated Linux bridge fixture only")
	}
	c := &Client{Path: path}
	ctx := context.Background()
	r, err := c.Catalog(ctx)
	if err != nil || r == nil || r.Digest != strings.Repeat("a", 64) {
		t.Fatal("catalog", r, err)
	}
	s, err := c.Status(ctx, "")
	if err != nil || s.Phase != "IDLE" {
		t.Fatal("idle", s, err)
	}
	id := "11111111-1111-4111-8111-111111111111"
	s, err = c.Start(ctx, id, r.Digest)
	if err != nil || s.Phase != "REQUESTED" {
		t.Fatal("start", s, err)
	}
	s, err = c.Start(ctx, id, r.Digest)
	if err != nil || s.Phase != "REQUESTED" {
		t.Fatal("retry", s, err)
	}
	s, err = c.Status(ctx, id)
	if err != nil || s.ID != id || s.Digest != r.Digest {
		t.Fatal("status", s, err)
	}
}
