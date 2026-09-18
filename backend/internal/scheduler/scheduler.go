package scheduler

import (
	"errors"
	"sort"
	"streamtool-relay/internal/domain"
	"sync"
	"time"
)

type Node struct {
	ID, Region                    string
	Capacity, Committed, Reserved domain.ResourceVector
	Draining                      bool
	LastSeen                      time.Time
}
type Reservation struct {
	ID, AllocationID, NodeID string
	Resources                domain.ResourceVector
	ExpiresAt                time.Time
	State                    string
}
type Lease struct {
	ResourceID, Owner string
	FencingToken      uint64
	ExpiresAt         time.Time
}
type Store struct {
	mu           sync.Mutex
	Nodes        map[string]Node
	Reservations map[string]Reservation
	Leases       map[string]Lease
	sequence     uint64
}

func NewStore() *Store {
	return &Store{Nodes: map[string]Node{}, Reservations: map[string]Reservation{}, Leases: map[string]Lease{}}
}
func (s *Store) Place(allocationID, region string, resources domain.ResourceVector, now time.Time, ttl time.Duration) (Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.Reservations {
		if r.State == "HELD" && !r.ExpiresAt.After(now) {
			r.State = "EXPIRED"
			s.Reservations[id] = r
			n := s.Nodes[r.NodeID]
			n.Reserved = n.Reserved.Sub(r.Resources)
			s.Nodes[n.ID] = n
		}
		if r.AllocationID == allocationID && (r.State == "HELD" || r.State == "COMMITTED") {
			return r, nil
		}
	}
	candidates := make([]Node, 0)
	for _, n := range s.Nodes {
		if n.Region != region || n.Draining || now.Sub(n.LastSeen) > 30*time.Second {
			continue
		}
		available := n.Capacity.Sub(n.Committed).Sub(n.Reserved)
		if resources.Fits(available) {
			candidates = append(candidates, n)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i].Committed.Slots + candidates[i].Reserved.Slots
		right := candidates[j].Committed.Slots + candidates[j].Reserved.Slots
		if left == right {
			return candidates[i].ID < candidates[j].ID
		}
		return left < right
	})
	if len(candidates) == 0 {
		return Reservation{}, errors.New("no eligible capacity")
	}
	n := candidates[0]
	s.sequence++
	r := Reservation{ID: allocationID + "-reservation", AllocationID: allocationID, NodeID: n.ID, Resources: resources, ExpiresAt: now.Add(ttl), State: "HELD"}
	n.Reserved = n.Reserved.Add(resources)
	s.Nodes[n.ID] = n
	s.Reservations[r.ID] = r
	return r, nil
}
func (s *Store) Commit(id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.Reservations[id]
	if !ok || r.State != "HELD" || !r.ExpiresAt.After(now) {
		return errors.New("reservation not held")
	}
	r.State = "COMMITTED"
	s.Reservations[id] = r
	n := s.Nodes[r.NodeID]
	n.Reserved = n.Reserved.Sub(r.Resources)
	n.Committed = n.Committed.Add(r.Resources)
	s.Nodes[n.ID] = n
	return nil
}
func (s *Store) AcquireLease(resource, owner string, now time.Time, ttl time.Duration) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.Leases[resource]
	if ok && lease.ExpiresAt.After(now) && lease.Owner != owner {
		return Lease{}, errors.New("lease held")
	}
	lease.ResourceID = resource
	lease.Owner = owner
	lease.FencingToken++
	lease.ExpiresAt = now.Add(ttl)
	s.Leases[resource] = lease
	return lease, nil
}
