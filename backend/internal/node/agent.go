package node

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"streamtool-relay/internal/buildinfo"
	"streamtool-relay/internal/domain"
	"streamtool-relay/internal/maintenance"
	"streamtool-relay/internal/pipeline"
	workerruntime "streamtool-relay/internal/runtime"
	"sync"
	"time"
)

type Agent struct {
	MaintenanceDirectory string
	NodeID               string
	Runtime              workerruntime.Runtime
	Capacity             domain.ResourceVector
	// StateFile stores the node-wide fencing high watermark across agent restarts.
	StateFile    string
	AllowedImage string
	mu           sync.Mutex
	draining     bool
	fence        uint64
}

func New(id string, r workerruntime.Runtime, capacity domain.ResourceVector) *Agent {
	return &Agent{NodeID: id, Runtime: r, Capacity: capacity}
}

type StartRequest struct {
	Command domain.Command           `json:"command"`
	Spec    workerruntime.WorkerSpec `json:"spec"`
	Secrets map[string][]byte        `json:"secrets"`
}

func (a *Agent) readFence() error {
	if a.StateFile != "" {
		b, err := os.ReadFile(a.StateFile)
		if err == nil {
			var n uint64
			if json.Unmarshal(b, &n) != nil {
				return errors.New("invalid fencing state")
			}
			if n > a.fence {
				a.fence = n
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
func (a *Agent) accept(c domain.Command) error {
	if err := c.Validate(time.Now()); err != nil {
		return err
	}
	if err := a.readFence(); err != nil {
		return err
	}
	if c.FencingToken < a.fence {
		return workerruntime.ErrFenced
	}
	if c.FencingToken > a.fence && a.StateFile != "" {
		if err := os.MkdirAll(filepath.Dir(a.StateFile), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(a.StateFile+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		err = json.NewEncoder(f).Encode(c.FencingToken)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = os.Rename(a.StateFile+".tmp", a.StateFile); err != nil {
			return err
		}
		// Persist the rename itself before accepting the command. File fsync alone
		// does not guarantee the high watermark survives an abrupt host power loss.
		directory, err := os.Open(filepath.Dir(a.StateFile))
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr = directory.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}

	}
	a.fence = c.FencingToken
	return nil
}
func (a *Agent) Start(r *http.Request) (workerruntime.Worker, error) {
	leave, err := maintenance.Enter(a.MaintenanceDirectory)
	if err != nil {
		return workerruntime.Worker{}, err
	}
	defer leave()

	var input StartRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return workerruntime.Worker{}, err
	}
	if input.Command.ResourceID != input.Spec.AllocationID || input.Command.Generation != input.Spec.Generation || input.Command.FencingToken != input.Spec.FencingToken {
		return workerruntime.Worker{}, errors.New("command does not match worker spec")
	}
	if input.Spec.NodeID != "" && input.Spec.NodeID != a.NodeID {
		return workerruntime.Worker{}, errors.New("wrong node")
	}
	if a.AllowedImage != "" && input.Spec.Image != a.AllowedImage {
		return workerruntime.Worker{}, errors.New("worker image not allowed")
	}
	input.Spec.Files = input.Secrets
	if err := workerruntime.ValidateStart(input.Spec); err != nil {
		return workerruntime.Worker{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.accept(input.Command); err != nil {
		return workerruntime.Worker{}, err
	}
	workers, err := a.Runtime.List(r.Context())
	if err != nil {
		return workerruntime.Worker{}, err
	}
	available := a.Capacity
	existing := false
	for _, w := range workers {
		if w.Spec.NodeID != "" && w.Spec.NodeID != a.NodeID {
			continue
		}
		if w.Spec.AllocationID == input.Spec.AllocationID {
			existing = w.State == "RUNNING"
			continue
		}
		if w.State == "RUNNING" {
			available = available.Sub(w.Spec.Resources)
		}
	}
	if a.draining && !existing {
		return workerruntime.Worker{}, errors.New("node draining")
	}
	if !input.Spec.Resources.Fits(available) {
		return workerruntime.Worker{}, errors.New("node capacity exceeded")
	}
	return a.Runtime.Start(r.Context(), input.Spec)
}
func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/workers", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.Start(r)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeJSON(w, 202, result)
	})
	mux.HandleFunc("DELETE /v1/workers/{id}", func(w http.ResponseWriter, r *http.Request) {
		var c domain.Command
		if json.NewDecoder(r.Body).Decode(&c) != nil || c.ResourceID != r.PathValue("id") {
			http.Error(w, "invalid command", 400)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.accept(c); err != nil {
			http.Error(w, "stale command", 409)
			return
		}
		err := a.Runtime.Stop(r.Context(), c.ResourceID)
		if err != nil && !errors.Is(err, workerruntime.ErrNotFound) {
			http.Error(w, "stop failed", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("PUT /v1/workers/{id}/destinations", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Command   domain.Command
			Secrets   map[string][]byte
			Resources domain.ResourceVector
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Command.ResourceID != r.PathValue("id") {
			http.Error(w, "invalid command", 400)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.accept(input.Command); err != nil {
			http.Error(w, "stale command", 409)
			return
		}
		if input.Resources.Slots != 1 || input.Resources.CPUMillis <= 0 || input.Resources.MemoryBytes <= 0 || input.Resources.IngressBPS <= 0 || input.Resources.EgressBPS <= 0 {
			http.Error(w, "invalid resources", 400)
			return
		}
		if err := a.readFence(); err != nil {
			http.Error(w, "fencing state unavailable", 503)
			return
		}
		workers, err := a.Runtime.List(r.Context())
		if err != nil {
			slog.Warn("worker runtime unavailable", "error", err)
			http.Error(w, "runtime unavailable", 503)
			return
		}
		available := a.Capacity
		for _, worker := range workers {
			if worker.ID != input.Command.ResourceID && worker.State == "RUNNING" {
				available = available.Sub(worker.Spec.Resources)
			}
		}
		if !input.Resources.Fits(available) {
			http.Error(w, "node capacity exceeded", 409)
			return
		}
		updater, ok := a.Runtime.(interface {
			ApplyDestinations(context.Context, string, map[string][]byte, domain.ResourceVector) error
		})
		if !ok {
			http.Error(w, "update unsupported", 501)
			return
		}
		if err := updater.ApplyDestinations(r.Context(), input.Command.ResourceID, input.Secrets, input.Resources); err != nil {
			http.Error(w, "update failed", 503)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("PUT /v1/workers/{id}/slate", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Command domain.Command
			Policy  pipeline.SlatePolicy
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Command.ResourceID != r.PathValue("id") {
			http.Error(w, "invalid command", 400)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.accept(input.Command); err != nil {
			http.Error(w, "stale command", 409)
			return
		}
		updater, ok := a.Runtime.(interface {
			ApplySlatePolicy(context.Context, string, pipeline.SlatePolicy) error
		})
		if !ok {
			http.Error(w, "update unsupported", 501)
			return
		}
		if err := updater.ApplySlatePolicy(r.Context(), input.Command.ResourceID, input.Policy); err != nil {
			http.Error(w, "update failed", 503)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /v1/workers/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.Runtime.Inspect(r.Context(), r.PathValue("id"))
		if errors.Is(err, workerruntime.ErrNotFound) {
			http.Error(w, "not found", 404)
			return
		}
		if err != nil {
			slog.Warn("worker inspection unavailable", "error", err)
			http.Error(w, "inspect unavailable", 503)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /v1/workers", func(w http.ResponseWriter, r *http.Request) {
		if err := a.readFence(); err != nil {
			http.Error(w, "fencing state unavailable", 503)
			return
		}
		workers, err := a.Runtime.List(r.Context())
		if err != nil {
			slog.Warn("worker listing unavailable", "error", err)
			http.Error(w, "list failed", 503)
			return
		}
		writeJSON(w, 200, workers)
	})
	mux.HandleFunc("PUT /v1/draining", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Command  domain.Command
			Draining bool
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid command", 400)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.accept(input.Command); err != nil {
			http.Error(w, "stale command", 409)
			return
		}
		a.draining = input.Draining
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /v1/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.readFence(); err != nil {
			http.Error(w, "fencing state unavailable", 503)
			return
		}
		workers, err := a.Runtime.List(r.Context())
		if err != nil {
			slog.Warn("worker runtime unavailable", "error", err)
			http.Error(w, "runtime unavailable", 503)
			return
		}
		used := domain.ResourceVector{}
		for _, v := range workers {
			if v.State == "RUNNING" && (v.Spec.NodeID == "" || v.Spec.NodeID == a.NodeID) {
				used = used.Add(v.Spec.Resources)
			}
		}
		writeJSON(w, 200, map[string]any{"maintenance_directory": a.MaintenanceDirectory, "version": buildinfo.Version, "worker_image": a.AllowedImage, "node_id": a.NodeID, "fencing_token": a.fence, "draining": a.draining, "capacity": a.Capacity, "used": used, "timestamp": time.Now().UTC()})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(2 << 20)
		if r.Method == "POST" && r.URL.Path == "/v1/workers" {
			limit = 72 << 20
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		mux.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
