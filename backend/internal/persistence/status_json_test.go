package persistence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSessionStatusJSONUsesPublicFieldNames(t *testing.T) {
	status := SessionStatus{
		SessionID:        "session",
		FallbackActive:   true,
		FallbackForced:   true,
		InputLive:        true,
		InputUnavailable: true,
		LastSeen:         time.Unix(1, 0).UTC(),
		Destinations: []DestinationStatus{{
			ID:         "output",
			State:      "STREAMING",
			Generation: 2,
			LastSeen:   time.Unix(1, 0).UTC(),
		}},
	}
	b, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	for _, field := range []string{`"session_id"`, `"fallback_active"`, `"fallback_forced"`, `"input_live"`, `"input_unavailable"`, `"last_seen"`, `"destinations"`, `"id"`, `"state"`, `"generation"`} {
		if !strings.Contains(raw, field) {
			t.Fatalf("missing %s in %s", field, raw)
		}
	}
	for _, legacy := range []string{"SessionID", "FallbackActive", "LastSeen", "Destinations", "Generation"} {
		if strings.Contains(raw, legacy) {
			t.Fatalf("internal Go field %s leaked into %s", legacy, raw)
		}
	}
}
