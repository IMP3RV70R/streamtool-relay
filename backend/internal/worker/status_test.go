package worker

import "testing"

func TestSnapshotRecoversCurrentStateWithoutSecrets(t *testing.T) {
	s := &Status{}
	s.Observe(Event{Type: InputLive})
	s.Observe(Event{Type: DestinationStreaming, DestinationID: "d", Generation: 2})
	s.Observe(Event{Type: DestinationFailed, DestinationID: "d", Generation: 1, Fields: map[string]any{"error": "secret-value"}})
	got := s.Snapshot()
	if !got.InputLive || got.Destinations["d"].State != "STREAMING" {
		t.Fatalf("stale event changed snapshot: %+v", got)
	}
	got.Destinations["d"] = DestinationStatus{State: "CORRUPTED"}
	if s.Snapshot().Destinations["d"].State != "STREAMING" {
		t.Fatal("snapshot aliases mutable state")
	}
	s.Observe(Event{Type: DestinationReconnecting, DestinationID: "d", Generation: 2, Code: ErrDestinationNet})
	if s.Snapshot().Destinations["d"].Reconnects != 1 {
		t.Fatal("missing reconnect")
	}
	s.Observe(Event{Type: InputLost})
	if s.Snapshot().InputLive {
		t.Fatal("lost input reported live")
	}
}

func TestFallbackAndSourceReturn(t *testing.T) {
	s := &Status{}
	for _, event := range []EventType{InputLive, FallbackActive, InputLive, FallbackActive, WorkerStopped} {
		s.Observe(Event{Type: event})
		got := s.Snapshot()
		if got.InputLive != (event == InputLive) || got.FallbackActive != (event == FallbackActive) {
			t.Fatalf("%s: %+v", event, got)
		}
	}
}

func TestForcedSlatePreservesObservedSourceState(t *testing.T) {
	s := &Status{}
	s.Observe(Event{Type: InputLive})
	s.Observe(Event{Type: FallbackActive, Fields: map[string]any{"forced": true, "source_live": true}})
	got := s.Snapshot()
	if !got.FallbackActive || !got.FallbackForced || !got.InputLive {
		t.Fatalf("forced slate hid source state: %+v", got)
	}
}

func TestDisabledSlateReportsUnavailableWithoutStopping(t *testing.T) {
	s := &Status{}
	s.Observe(Event{Type: InputLive})
	s.Observe(Event{Type: InputUnavailable})
	got := s.Snapshot()
	if got.InputLive || got.FallbackActive || !got.InputUnavailable {
		t.Fatalf("unavailable source state lost: %+v", got)
	}
}

func TestRejectedSourcePreservesFallbackUntilCompatibleSource(t *testing.T) {
	s := &Status{}
	s.Observe(Event{Type: FallbackActive})
	s.Observe(Event{Type: InputRejected, Code: ErrUnsupportedMedia})
	s.Observe(Event{Type: FallbackActive})
	got := s.Snapshot()
	if !got.FallbackActive || got.InputLive || got.InputError != ErrUnsupportedMedia {
		t.Fatalf("rejected source lost safe fallback: %+v", got)
	}
	s.Observe(Event{Type: InputLive})
	got = s.Snapshot()
	if !got.InputLive || got.FallbackActive || got.InputError != "" {
		t.Fatalf("compatible source did not recover: %+v", got)
	}
}
