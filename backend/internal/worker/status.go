package worker

import (
	"sync"
	"time"
)

type DestinationStatus struct {
	State      string    `json:"state"`
	Generation uint64    `json:"generation"`
	Reconnects int       `json:"reconnects"`
	ErrorCode  ErrorCode `json:"error_code,omitempty"`
}
type Snapshot struct {
	InputError       ErrorCode                    `json:"input_error,omitempty"`
	FallbackActive   bool                         `json:"fallback_active"`
	FallbackForced   bool                         `json:"fallback_forced"`
	InputLive        bool                         `json:"input_live"`
	InputUnavailable bool                         `json:"input_unavailable"`
	Timestamp        time.Time                    `json:"timestamp"`
	Destinations     map[string]DestinationStatus `json:"destinations"`
}
type Status struct {
	mu       sync.Mutex
	snapshot Snapshot
}

func (s *Status) Observe(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.Destinations == nil {
		s.snapshot.Destinations = map[string]DestinationStatus{}
	}
	switch e.Type {
	case InputLive:
		s.snapshot.InputError = ""
		s.snapshot.InputLive = true
		s.snapshot.FallbackActive = false
		s.snapshot.FallbackForced = false
		s.snapshot.InputUnavailable = false
	case InputRejected:
		s.snapshot.InputError = ErrUnsupportedMedia
		s.snapshot.InputLive = false
	case FallbackActive:
		s.snapshot.InputLive, _ = e.Fields["source_live"].(bool)
		if s.snapshot.InputLive {
			s.snapshot.InputError = ""
		}
		s.snapshot.FallbackActive = true
		s.snapshot.FallbackForced, _ = e.Fields["forced"].(bool)
		s.snapshot.InputUnavailable = false
	case InputUnavailable:
		s.snapshot.InputLive = false
		s.snapshot.FallbackActive = false
		s.snapshot.FallbackForced = false
		s.snapshot.InputUnavailable = true
	case InputLost, WorkerStopped:
		s.snapshot.InputError = ""
		s.snapshot.InputLive = false
		s.snapshot.FallbackActive = false
		s.snapshot.FallbackForced = false
		s.snapshot.InputUnavailable = false
	}
	states := map[EventType]string{DestinationConnecting: "CONNECTING", DestinationStreaming: "STREAMING", DestinationReconnecting: "RECONNECTING", DestinationFailed: "FAILED", DestinationStopped: "STOPPED"}
	if state, ok := states[e.Type]; ok {
		old := s.snapshot.Destinations[e.DestinationID]
		if e.Generation < old.Generation {
			return
		}
		old.State = state
		old.Generation = e.Generation
		old.ErrorCode = e.Code
		if e.Type == DestinationReconnecting {
			old.Reconnects++
		}
		s.snapshot.Destinations[e.DestinationID] = old
	}
}
func (s *Status) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{InputError: s.snapshot.InputError, FallbackActive: s.snapshot.FallbackActive, FallbackForced: s.snapshot.FallbackForced, InputLive: s.snapshot.InputLive, InputUnavailable: s.snapshot.InputUnavailable, Timestamp: time.Now().UTC(), Destinations: map[string]DestinationStatus{}}
	for id, d := range s.snapshot.Destinations {
		out.Destinations[id] = d
	}
	return out
}
