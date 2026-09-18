package fake

import (
	"context"
	"strconv"
	"streamtool-relay/internal/domain"
	"streamtool-relay/internal/pipeline"
	workerruntime "streamtool-relay/internal/runtime"
	"sync"
	"time"
)

func (r *Runtime) ApplySlatePolicy(_ context.Context, id string, policy pipeline.SlatePolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[id]
	if !ok {
		return workerruntime.ErrNotFound
	}
	w.Spec.Environment["WORKER_SLATE_ON_SOURCE_LOSS"] = strconv.FormatBool(policy.OnSourceLoss)
	w.Spec.Environment["WORKER_SLATE_FORCED"] = strconv.FormatBool(policy.Forced)
	r.workers[id] = w
	return nil
}

func (r *Runtime) ApplyDestinations(_ context.Context, id string, _ map[string][]byte, resources domain.ResourceVector) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[id]
	if !ok {
		return workerruntime.ErrNotFound
	}
	w.Spec.Resources = resources
	r.workers[id] = w
	return nil
}

type Runtime struct {
	mu      sync.Mutex
	workers map[string]workerruntime.Worker
	Starts  int
}

func New() *Runtime { return &Runtime{workers: map[string]workerruntime.Worker{}} }
func (r *Runtime) Start(_ context.Context, s workerruntime.WorkerSpec) (workerruntime.Worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := workerruntime.ValidateStart(s); err != nil {
		return workerruntime.Worker{}, err
	}
	if current, ok := r.workers[s.AllocationID]; ok {
		if s.FencingToken < current.Spec.FencingToken {
			return workerruntime.Worker{}, workerruntime.ErrFenced
		}
		if s.Generation == current.Spec.Generation && s.FencingToken == current.Spec.FencingToken && current.State == "RUNNING" {
			return current, nil
		}
	}
	r.Starts++
	w := workerruntime.Worker{ID: s.AllocationID, Spec: s, State: "RUNNING", StartedAt: time.Now()}
	r.workers[s.AllocationID] = w
	return w, nil
}
func (r *Runtime) Stop(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[id]
	if !ok {
		return workerruntime.ErrNotFound
	}
	w.State = "STOPPED"
	r.workers[id] = w
	return nil
}
func (r *Runtime) Inspect(_ context.Context, id string) (workerruntime.Worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[id]
	if !ok {
		return w, workerruntime.ErrNotFound
	}
	return w, nil
}
func (r *Runtime) List(context.Context) ([]workerruntime.Worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]workerruntime.Worker, 0, len(r.workers))
	for _, w := range r.workers {
		out = append(out, w)
	}
	return out, nil
}
