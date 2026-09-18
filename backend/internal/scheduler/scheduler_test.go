package scheduler

import (
	"streamtool-relay/internal/domain"
	"sync"
	"testing"
	"time"
)

func TestConcurrentPlacementDoesNotOverbook(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Nodes["a"] = Node{ID: "a", Region: "r", Capacity: domain.ResourceVector{Slots: 1}, LastSeen: now}
	var successes int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Place(id, "r", domain.ResourceVector{Slots: 1}, now, time.Minute); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("placements=%d", successes)
	}
}
func TestDeterministicPlacementLeaseAndExpiry(t *testing.T) {
	s := NewStore()
	now := time.Now()
	for _, id := range []string{"b", "a"} {
		s.Nodes[id] = Node{ID: id, Region: "r", Capacity: domain.ResourceVector{Slots: 2}, LastSeen: now}
	}
	r, err := s.Place("x", "r", domain.ResourceVector{Slots: 1}, now, time.Second)
	if err != nil || r.NodeID != "a" {
		t.Fatal(r, err)
	}
	first, _ := s.AcquireLease("x", "one", now, time.Second)
	if _, err = s.AcquireLease("x", "two", now, time.Second); err == nil {
		t.Fatal("stole live lease")
	}
	second, err := s.AcquireLease("x", "two", now.Add(2*time.Second), time.Second)
	if err != nil || second.FencingToken <= first.FencingToken {
		t.Fatal("fencing did not advance")
	}
}
func TestDrainingNodeGetsNoNewPlacementAndLoadSpreads(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Nodes["drain"] = Node{ID: "drain", Region: "r", Capacity: domain.ResourceVector{Slots: 10}, Draining: true, LastSeen: now}
	s.Nodes["a"] = Node{ID: "a", Region: "r", Capacity: domain.ResourceVector{Slots: 2}, LastSeen: now}
	s.Nodes["b"] = Node{ID: "b", Region: "r", Capacity: domain.ResourceVector{Slots: 2}, LastSeen: now}
	one, err := s.Place("one", "r", domain.ResourceVector{Slots: 1}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Place("two", "r", domain.ResourceVector{Slots: 1}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if one.NodeID == "drain" || two.NodeID == "drain" || one.NodeID == two.NodeID {
		t.Fatalf("bad spread: %s %s", one.NodeID, two.NodeID)
	}
}
