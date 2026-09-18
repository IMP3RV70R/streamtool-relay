package runtime

import (
	"context"
	"errors"
	"regexp"
	"streamtool-relay/internal/domain"
	"streamtool-relay/internal/worker"
	"time"
)

type WorkerSpec struct {
	AllocationID, SessionID, Image, NodeID string
	Generation, FencingToken               uint64
	Environment                            map[string]string
	Resources                              domain.ResourceVector
	Files                                  map[string][]byte `json:"-"`
}
type Worker struct {
	ID        string
	Spec      WorkerSpec
	State     string
	StartedAt time.Time
	ExitCode  int
	Report    *worker.Snapshot `json:",omitempty"`
}
type Runtime interface {
	Start(context.Context, WorkerSpec) (Worker, error)
	Stop(context.Context, string) error
	Inspect(context.Context, string) (Worker, error)
	List(context.Context) ([]Worker, error)
}

var (
	ErrNotFound = errors.New("worker not found")
	ErrFenced   = errors.New("stale fencing token")
)

var safeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

func ValidID(id string) bool { return safeID.MatchString(id) }
func ValidateStart(spec WorkerSpec) error {
	// The private handoff permits 32 entries, including the Agent's snapshot.
	if len(spec.Files) > 31 {
		return errors.New("worker file count exceeds limit")
	}
	if !ValidID(spec.AllocationID) || spec.Resources.Slots <= 0 || spec.Resources.CPUMillis < 0 || spec.Resources.MemoryBytes < 0 || spec.Resources.IngressBPS < 0 || spec.Resources.EgressBPS < 0 {
		return errors.New("invalid identity or resources")
	}
	var total int
	for name, content := range spec.Files {
		total += len(content)
		if (name == "fallback_asset" && len(content) > 50<<20) || (name != "fallback_asset" && len(content) > 1<<20) || total > 54<<20 {
			return errors.New("worker file payload exceeds limit")
		}
		if name == "ready" || (!ValidID(name) && name != "destinations.json") {
			return errors.New("invalid secret filename")
		}
	}
	if spec.AllocationID == "" || spec.SessionID == "" || spec.Image == "" || spec.Generation == 0 || spec.FencingToken == 0 {
		return errors.New("invalid worker spec")
	}
	for key := range spec.Environment {
		if key == "WORKER_SOURCE_TOKEN" || key == "WORKER_DESTINATION_SECRET" {
			return errors.New("plaintext secret environment is forbidden")
		}
	}
	return nil
}
