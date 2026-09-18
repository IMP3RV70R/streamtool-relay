package ingest

import (
	"errors"
	"sync"
	"time"
)

type Protocol string

const (
	SRT  Protocol = "srt"
	RTMP Protocol = "rtmp"
)

type Observation struct {
	EdgeID, ConnectionID, StreamID string
	Protocol                       Protocol
	Connected                      bool
	SeenAt                         time.Time
}
type Session struct {
	ID, StreamID, ConnectionID string
	StartedAt                  time.Time
	EndAfter                   *time.Time
	EndedAt                    *time.Time
}
type State struct {
	mu          sync.Mutex
	connections map[string]Observation
	sessions    map[string]*Session
	sequence    uint64
	Grace       time.Duration
}

func NewState(grace time.Duration) *State {
	return &State{connections: map[string]Observation{}, sessions: map[string]*Session{}, Grace: grace}
}
func (s *State) Observe(o Observation) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.EdgeID == "" || o.ConnectionID == "" || o.StreamID == "" || (o.Protocol != SRT && o.Protocol != RTMP) {
		return nil, errors.New("invalid ingest observation")
	}
	key := o.EdgeID + "/" + o.ConnectionID
	if old, ok := s.connections[key]; ok && old.SeenAt.After(o.SeenAt) {
		return s.sessions[o.StreamID], nil
	}
	s.connections[key] = o
	session := s.sessions[o.StreamID]
	if o.Connected {
		for k, c := range s.connections {
			if k != key && c.StreamID == o.StreamID && c.Connected {
				return session, errors.New("active publisher already exists")
			}
		}
		if session == nil || session.EndedAt != nil {
			s.sequence++
			session = &Session{ID: o.StreamID + "-session-" + time.Unix(int64(s.sequence), 0).Format("150405"), StreamID: o.StreamID, ConnectionID: o.ConnectionID, StartedAt: o.SeenAt}
			s.sessions[o.StreamID] = session
		}
		session.EndAfter = nil
		return session, nil
	}
	if session != nil && session.EndedAt == nil {
		deadline := o.SeenAt.Add(s.Grace)
		session.EndAfter = &deadline
	}
	return session, nil
}
func (s *State) Reconcile(now time.Time) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ended []Session
	for _, session := range s.sessions {
		if session.EndedAt == nil && session.EndAfter != nil && !now.Before(*session.EndAfter) {
			endedAt := *session.EndAfter
			session.EndedAt = &endedAt
			ended = append(ended, *session)
		}
	}
	return ended
}
