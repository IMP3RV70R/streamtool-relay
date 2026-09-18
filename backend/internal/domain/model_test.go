package domain

import (
	"testing"
	"time"
)

func TestTransitionsAndProjection(t *testing.T) {
	if Transition(Live, Ended) == nil {
		t.Fatal("skipped stopping")
	}
	if Transition(Live, Stopping) != nil {
		t.Fatal("valid transition rejected")
	}
	if got := (ObservedStatus{Desired: "RUNNING", Observed: "RUNNING", LastSeen: time.Unix(1, 0)}).Project(time.Unix(20, 0), time.Second); got != "UNKNOWN" {
		t.Fatal(got)
	}
}
func TestCapacity(t *testing.T) {
	if (ResourceVector{Slots: 2, EgressBPS: 10}).Fits(ResourceVector{Slots: 1, EgressBPS: 20}) {
		t.Fatal("overbook accepted")
	}
}
