package node

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"streamtool-relay/internal/domain"
	workerruntime "streamtool-relay/internal/runtime"
	"streamtool-relay/internal/runtime/fake"
	"testing"
	"time"
)

func TestAgentIdempotencyFencingAndCapacity(t *testing.T) {
	runtime := fake.New()
	a := New("n", runtime, domain.ResourceVector{Slots: 1})
	req := StartRequest{Command: domain.Command{CommandID: "c", ResourceID: "a", TraceID: "t", Generation: 1, FencingToken: 2, Deadline: time.Now().Add(time.Minute)}, Spec: workerruntime.WorkerSpec{AllocationID: "a", SessionID: "s", Image: "worker", Generation: 1, FencingToken: 2, Resources: domain.ResourceVector{Slots: 1}}}
	body, _ := json.Marshal(req)
	for i := 0; i < 2; i++ {
		request := httptest.NewRequest("POST", "/v1/workers", bytes.NewReader(body))
		if _, err := a.Start(request); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.Starts != 1 {
		t.Fatal("duplicate worker")
	}
	req.Command.CommandID = "c2"
	req.Command.ResourceID = "b"
	req.Command.FencingToken = 3
	req.Spec.AllocationID = "b"
	req.Spec.FencingToken = 3
	body, _ = json.Marshal(req)
	if _, err := a.Start(httptest.NewRequest("POST", "/", bytes.NewReader(body))); err == nil {
		t.Fatal("overbook accepted")
	}
	_, _ = runtime.List(context.Background())
}
