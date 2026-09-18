package ingest

import (
	"testing"
	"time"
)

func TestIdempotentSinglePublisherAndGrace(t *testing.T) {
	s := NewState(5 * time.Second)
	now := time.Unix(100, 0)
	o := Observation{EdgeID: "e", ConnectionID: "1", StreamID: "s", Protocol: SRT, Connected: true, SeenAt: now}
	first, err := s.Observe(o)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Observe(o)
	if err != nil || again.ID != first.ID {
		t.Fatal("duplicate did not converge")
	}
	_, err = s.Observe(Observation{EdgeID: "e", ConnectionID: "2", StreamID: "s", Protocol: RTMP, Connected: true, SeenAt: now})
	if err == nil {
		t.Fatal("second publisher accepted")
	}
	o.Connected = false
	o.SeenAt = now.Add(time.Second)
	s.Observe(o)
	if len(s.Reconcile(now.Add(4*time.Second))) != 0 {
		t.Fatal("ended before grace")
	}
	if len(s.Reconcile(now.Add(7*time.Second))) != 1 {
		t.Fatal("not ended")
	}
}
