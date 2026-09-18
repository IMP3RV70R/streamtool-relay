package node

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"streamtool-relay/internal/domain"
	workerruntime "streamtool-relay/internal/runtime"
	"streamtool-relay/internal/runtime/fake"
	"sync"
	"testing"
	"time"
)

func request(id string, fence uint64) StartRequest {
	return StartRequest{Command: domain.Command{CommandID: id, ResourceID: id, TraceID: "t", Generation: 1, FencingToken: fence, Deadline: time.Now().Add(time.Minute)}, Spec: workerruntime.WorkerSpec{AllocationID: id, SessionID: "s", Image: "worker", Generation: 1, FencingToken: fence, Resources: domain.ResourceVector{Slots: 1}}}
}
func start(a *Agent, input StartRequest) error {
	b, _ := json.Marshal(input)
	_, err := a.Start(httptest.NewRequest("POST", "/v1/workers", bytes.NewReader(b)))
	return err
}
func TestConcurrentAdmissionAndRestart(t *testing.T) {
	runtime := fake.New()
	a := New("n", runtime, domain.ResourceVector{Slots: 1})
	a.StateFile = filepath.Join(t.TempDir(), "fence.json")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); errs <- start(a, request(id, 10)) }(id)
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("admitted %d requests", successes)
	}
	recovered := New("n", runtime, domain.ResourceVector{Slots: 1})
	recovered.StateFile = a.StateFile
	if err := start(recovered, request("c", 9)); err == nil {
		t.Fatal("stale command survived agent restart")
	}
	if err := start(recovered, request("c", 11)); err == nil {
		t.Fatal("agent restart forgot occupied capacity")
	}
}
func TestCommandBindingAndFencedStop(t *testing.T) {
	a := New("n", fake.New(), domain.ResourceVector{Slots: 1})
	r := request("a", 10)
	r.Command.ResourceID = "b"
	if start(a, r) == nil {
		t.Fatal("unbound spec accepted")
	}
	if err := start(a, request("a", 10)); err != nil {
		t.Fatal(err)
	}
	old := request("a", 9).Command
	b, _ := json.Marshal(old)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("DELETE", "/v1/workers/a", bytes.NewReader(b)))
	if w.Code != 409 {
		t.Fatalf("stale stop: %d", w.Code)
	}
}
