package domain

import (
	"errors"
	"time"
)

type SessionPhase string

const (
	PendingIngest SessionPhase = "PENDING_INGEST"
	Scheduling    SessionPhase = "SCHEDULING"
	Starting      SessionPhase = "STARTING"
	Live          SessionPhase = "LIVE"
	Stopping      SessionPhase = "STOPPING"
	Ended         SessionPhase = "ENDED"
	Failed        SessionPhase = "FAILED"
)

var transitions = map[SessionPhase]map[SessionPhase]bool{PendingIngest: {Scheduling: true, Failed: true}, Scheduling: {Starting: true, Failed: true}, Starting: {Live: true, Failed: true, Stopping: true}, Live: {Stopping: true, Failed: true}, Stopping: {Ended: true, Failed: true}}

func CanTransition(from, to SessionPhase) bool { return transitions[from][to] }
func Transition(from, to SessionPhase) error {
	if !CanTransition(from, to) {
		return errors.New("invalid session transition")
	}
	return nil
}

type ResourceVector struct {
	Slots       int   `json:"slots"`
	CPUMillis   int64 `json:"cpu_millis,omitempty"`
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
	IngressBPS  int64 `json:"ingress_bps,omitempty"`
	EgressBPS   int64 `json:"egress_bps,omitempty"`
}

func (a ResourceVector) Fits(capacity ResourceVector) bool {
	return a.Slots <= capacity.Slots && a.CPUMillis <= capacity.CPUMillis && a.MemoryBytes <= capacity.MemoryBytes && a.IngressBPS <= capacity.IngressBPS && a.EgressBPS <= capacity.EgressBPS
}
func (a ResourceVector) Add(b ResourceVector) ResourceVector {
	return ResourceVector{a.Slots + b.Slots, a.CPUMillis + b.CPUMillis, a.MemoryBytes + b.MemoryBytes, a.IngressBPS + b.IngressBPS, a.EgressBPS + b.EgressBPS}
}
func (a ResourceVector) Sub(b ResourceVector) ResourceVector {
	return ResourceVector{a.Slots - b.Slots, a.CPUMillis - b.CPUMillis, a.MemoryBytes - b.MemoryBytes, a.IngressBPS - b.IngressBPS, a.EgressBPS - b.EgressBPS}
}

type Command struct {
	CommandID, ResourceID, TraceID string
	Generation, FencingToken       uint64
	Deadline                       time.Time
}

func (c Command) Validate(now time.Time) error {
	if c.CommandID == "" || c.ResourceID == "" || c.TraceID == "" || c.Generation == 0 || c.FencingToken == 0 {
		return errors.New("incomplete command envelope")
	}
	if !c.Deadline.After(now) {
		return errors.New("command deadline expired")
	}
	return nil
}

type ObservedStatus struct {
	Desired, Observed string
	LastSeen          time.Time
	ErrorCode         string
}

func (s ObservedStatus) Project(now time.Time, staleAfter time.Duration) string {
	if s.LastSeen.IsZero() || now.Sub(s.LastSeen) > staleAfter {
		return "UNKNOWN"
	}
	if s.ErrorCode != "" {
		return "DEGRADED"
	}
	if s.Desired == "STOPPED" && s.Observed == "STOPPED" {
		return "STOPPED"
	}
	if s.Desired == "RUNNING" && s.Observed == "RUNNING" {
		return "LIVE"
	}
	return "TRANSITIONING"
}
