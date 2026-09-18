package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"streamtool-relay/internal/pipeline"
)

type fakeRuntime struct {
	events            chan pipeline.RuntimeEvent
	reconnects, stops int
	endAfterReconnect bool
}

func (f *fakeRuntime) Start(context.Context) error          { return nil }
func (f *fakeRuntime) Events() <-chan pipeline.RuntimeEvent { return f.events }
func (f *fakeRuntime) ApplyDestinations(context.Context, []pipeline.DestinationSpec) error {
	return nil
}
func (f *fakeRuntime) ApplySlatePolicy(context.Context, pipeline.SlatePolicy) error { return nil }
func (f *fakeRuntime) ReconnectDestination(_ context.Context, _ string) error {
	f.reconnects++
	if f.endAfterReconnect {
		f.events <- pipeline.RuntimeEvent{Type: pipeline.RuntimeInputError, Err: errors.New("source closed")}
	}
	return nil
}
func (f *fakeRuntime) Stop(context.Context) error { f.stops++; return nil }

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	f.mu.Lock()
	f.sleeps = append(f.sleeps, d)
	f.now = f.now.Add(d)
	f.mu.Unlock()
	return nil
}

func TestServiceReconnectsDestinationWithoutRestartingRuntime(t *testing.T) {
	r := &fakeRuntime{events: make(chan pipeline.RuntimeEvent, 4), endAfterReconnect: true}
	r.events <- pipeline.RuntimeEvent{Type: pipeline.RuntimeInputLive}
	r.events <- pipeline.RuntimeEvent{Type: pipeline.RuntimeDestinationStreaming, DestinationID: "one", Generation: 1}
	r.events <- pipeline.RuntimeEvent{Type: pipeline.RuntimeDestinationError, DestinationID: "one", Generation: 1, Err: errors.New("connection reset")}
	c := &fakeClock{now: time.Unix(1, 0)}
	var got []EventType
	s := Service{SessionID: "s", DestinationIDs: []string{"one"}, Runtime: r, Clock: c, Backoff: Backoff{Min: time.Second, Max: 30 * time.Second}, StableInterval: time.Minute, Events: SinkFunc(func(e Event) { got = append(got, e.Type) })}
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.reconnects != 1 {
		t.Fatalf("reconnects = %d", r.reconnects)
	}
	if r.stops != 1 {
		t.Fatalf("stops = %d", r.stops)
	}
	if len(c.sleeps) != 1 || c.sleeps[0] != time.Second {
		t.Fatalf("sleeps = %v", c.sleeps)
	}
	if !contains(got, DestinationReconnecting) || !contains(got, WorkerStopped) {
		t.Fatalf("events = %v", got)
	}
}

func TestServiceDoesNotRetryPermanentDestinationError(t *testing.T) {
	r := &fakeRuntime{events: make(chan pipeline.RuntimeEvent, 2)}
	r.events <- pipeline.RuntimeEvent{Type: pipeline.RuntimeDestinationError, DestinationID: "one", Generation: 1, Err: errors.New("401 unauthorized")}
	r.events <- pipeline.RuntimeEvent{Type: pipeline.RuntimeInputError}
	c := &fakeClock{now: time.Unix(1, 0)}
	s := Service{SessionID: "s", Runtime: r, Clock: c, Backoff: Backoff{Min: time.Second, Max: time.Second}, StableInterval: time.Second, Events: SinkFunc(func(Event) {})}
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.reconnects != 0 {
		t.Fatal("permanent error was retried")
	}
}

func contains(events []EventType, want EventType) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

func TestFallbackDoesNotStopWorkerAndSourceCanReturn(t *testing.T) {
	r := &fakeRuntime{events: make(chan pipeline.RuntimeEvent, 4)}
	for _, kind := range []pipeline.RuntimeEventType{pipeline.RuntimeInputLive, pipeline.RuntimeFallback, pipeline.RuntimeInputLive, pipeline.RuntimeEOS} {
		r.events <- pipeline.RuntimeEvent{Type: kind}
	}
	var got []EventType
	s := Service{SessionID: "s", Runtime: r, Clock: &fakeClock{}, Backoff: Backoff{Min: time.Second, Max: time.Second}, Events: SinkFunc(func(e Event) { got = append(got, e.Type) })}
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []EventType{InputConnecting, InputLive, FallbackActive, InputLive, InputLost, WorkerStopping, WorkerStopped}
	if len(got) != len(want) {
		t.Fatalf("events: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events: %v", got)
		}
	}
	if r.stops != 1 {
		t.Fatalf("stops: %d", r.stops)
	}
}

func TestDisabledSlateWaitsForSourceReturn(t *testing.T) {
	r := &fakeRuntime{events: make(chan pipeline.RuntimeEvent, 4)}
	for _, kind := range []pipeline.RuntimeEventType{pipeline.RuntimeInputLive, pipeline.RuntimeInputUnavailable, pipeline.RuntimeInputLive, pipeline.RuntimeEOS} {
		r.events <- pipeline.RuntimeEvent{Type: kind}
	}
	var got []EventType
	s := Service{SessionID: "s", Runtime: r, Clock: &fakeClock{}, Backoff: Backoff{Min: time.Second, Max: time.Second}, Events: SinkFunc(func(e Event) { got = append(got, e.Type) })}
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []EventType{InputConnecting, InputLive, InputUnavailable, InputLive, InputLost, WorkerStopping, WorkerStopped}
	if len(got) != len(want) {
		t.Fatalf("events: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events: %v", got)
		}
	}
}
