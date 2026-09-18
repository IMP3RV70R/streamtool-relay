package runtime_test

import (
	"context"
	"errors"
	"streamtool-relay/internal/domain"
	workerruntime "streamtool-relay/internal/runtime"
	"streamtool-relay/internal/runtime/fake"
	"testing"
)

func TestFakeConformance(t *testing.T) {
	r := fake.New()
	spec := workerruntime.WorkerSpec{AllocationID: "a", SessionID: "s", Image: "worker:pinned", Generation: 1, FencingToken: 2, Resources: domain.ResourceVector{Slots: 1}}
	first, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Start(context.Background(), spec)
	if err != nil || first.ID != second.ID || r.Starts != 1 {
		t.Fatal("start not idempotent")
	}
	spec.FencingToken = 1
	if _, err = r.Start(context.Background(), spec); !errors.Is(err, workerruntime.ErrFenced) {
		t.Fatal("stale command accepted")
	}
	if err = r.Stop(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
}
