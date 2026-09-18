package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"streamtool-relay/internal/application"
	"sync/atomic"
	"testing"
	"time"

	"streamtool-relay/internal/auth"
	appcrypto "streamtool-relay/internal/crypto"
	"streamtool-relay/internal/domain"
	"streamtool-relay/internal/node"
	"streamtool-relay/internal/persistence"
	workerruntime "streamtool-relay/internal/runtime"
	"streamtool-relay/internal/runtime/fake"
	"streamtool-relay/internal/worker"
)

type reportingRuntime struct {
	*fake.Runtime
	loseResponse atomic.Bool
	fallback     atomic.Bool
}

func (r *reportingRuntime) Inspect(ctx context.Context, id string) (workerruntime.Worker, error) {
	w, err := r.Runtime.Inspect(ctx, id)
	if err == nil && w.State == "RUNNING" {
		w.Report = &worker.Snapshot{InputLive: !r.fallback.Load(), FallbackActive: r.fallback.Load(), Timestamp: time.Now()}
	}
	return w, err
}
func TestDurableSingleNodeReconciliation(t *testing.T) {
	database := filepath.Join(t.TempDir(), "control.sqlite")
	ctx := context.Background()
	store, err := persistence.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx, "../../sqlite-migrations"); err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateAccount(ctx, "control-test")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashKey("test-ingest-key-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := store.CreateStream(ctx, account, "stream", "local", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `INSERT INTO source_slates(source_id) VALUES(?1)`, stream.ID); err != nil {
		t.Fatal(err)
	}
	key := []byte("01234567890123456789012345678901")
	keys := appcrypto.LocalProvider{ID: "test", Keys: map[string][]byte{"test": key}}
	envelope, _ := appcrypto.Encrypt(ctx, keys, []byte("destination-secret"), []byte(stream.ID+":target"))
	_, err = store.CreateDestination(ctx, persistence.Destination{StreamID: stream.ID, Name: "target", Endpoint: "rtmp://receiver/live", KeyID: envelope.KeyID, Ciphertext: envelope.Ciphertext, Nonce: envelope.Nonce})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.ObserveIngest(ctx, "test-edge", "conn", stream.ID, "srt", true, time.Now(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &reportingRuntime{Runtime: fake.New()}
	runtime.loseResponse.Store(true)
	agent := node.New("00000000-0000-4000-8000-000000000009", runtime, testCapacity(1))
	server := httptest.NewServer(agent.Handler())
	defer server.Close()
	var edgeActive atomic.Bool
	edgeActive.Store(true)
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := []any{}
		if edgeActive.Load() {
			items = append(items, map[string]any{"name": stream.ID, "ready": true, "source": map[string]string{"type": "srtConn", "id": "conn"}})
		}
		json.NewEncoder(w).Encode(map[string]any{"pageCount": 1, "items": items})
	}))
	defer edge.Close()
	c := &Controller{Store: store, Keys: keys, Client: server.Client(), EdgeClient: edge.Client(), NodeID: agent.NodeID, Region: "local", AgentURL: server.URL, WorkerImage: "worker", EdgeURL: edge.URL, SourceURL: "srt://edge:8890", EdgeID: "test-edge", ReadKey: key, Grace: time.Second, Slots: 1}
	once := func() {
		t.Helper()
		if err := c.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}
	once()
	once()
	status, err := store.Status(ctx, stream.ID)
	if err != nil || status.Phase != "LIVE" {
		t.Fatalf("not live: %+v %v", status, err)
	}
	if runtime.Starts != 1 {
		t.Fatalf("starts: %d", runtime.Starts)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE source_slates SET forced=true,generation=generation+1 WHERE source_id=?1`, stream.ID); err != nil {
		t.Fatal(err)
	}
	once()
	var currentAllocation string
	if err = store.Pool.QueryRow(ctx, `SELECT id FROM worker_allocations WHERE session_id=?1 AND state NOT IN ('STOPPED','FAILED')`, session).Scan(&currentAllocation); err != nil {
		t.Fatal(err)
	}
	workerState, err := runtime.Inspect(ctx, currentAllocation)
	if err != nil || workerState.Spec.Environment["WORKER_SLATE_FORCED"] != "true" || runtime.Starts != 1 {
		t.Fatalf("slate policy did not update in place: %+v starts=%d err=%v", workerState.Spec.Environment, runtime.Starts, err)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE source_slates SET forced=false,generation=generation+1 WHERE source_id=?1`, stream.ID); err != nil {
		t.Fatal(err)
	}
	once()

	// Disconnecting the last destination must close the outgoing stream, while
	// retaining the ingest session so reauthorization can resume the same source.
	if _, err = store.Pool.Exec(ctx, `UPDATE destinations SET enabled=false WHERE stream_id=?1`, stream.ID); err != nil {
		t.Fatal(err)
	}
	once()
	status, _ = store.Status(ctx, stream.ID)
	if status.Phase != "SCHEDULING" || status.Desired != "RUNNING" || status.AllocationState != "PENDING" {
		t.Fatalf("last destination remained active: %+v", status)
	}
	workers, _ := runtime.List(ctx)
	for _, w := range workers {
		if w.State == "RUNNING" {
			t.Fatal("output worker survived disconnect")
		}
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE destinations SET enabled=true,generation=generation+1 WHERE stream_id=?1`, stream.ID); err != nil {
		t.Fatal(err)
	}
	once()
	once()
	status, _ = store.Status(ctx, stream.ID)
	if status.SessionID != session || status.Phase != "LIVE" || runtime.Starts != 2 {
		t.Fatalf("destination reconnect failed: %+v", status)
	}
	// A fresh controller and agent reconstruct state from the same database/runtime.
	server.Close()
	server = httptest.NewServer(node.New(agent.NodeID, runtime, testCapacity(1)).Handler())
	defer server.Close()
	c.AgentURL = server.URL
	c.Client = server.Client()
	recovered := *c
	c = &recovered
	once()
	if runtime.Starts != 2 {
		t.Fatal("adoption restarted worker")
	}
	// Ingest connection identity must not be reassigned to another stream.
	other, err := store.CreateStream(ctx, account, "other", "local", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ObserveIngest(ctx, "test-edge", "conn", other.ID, "srt", true, time.Now(), time.Second); err == nil {
		t.Fatal("cross-stream connection accepted")
	}
	// Stale observations must not stop current media.
	if _, err = store.ObserveIngest(ctx, "test-edge", "conn", stream.ID, "srt", false, time.Now().Add(-time.Minute), time.Second); err != nil {
		t.Fatal(err)
	}
	once()
	status, _ = store.Status(ctx, stream.ID)
	if status.Phase != "LIVE" {
		t.Fatal("stale disconnect stopped session")
	}

	// A fallback snapshot survives the old grace deadline and API reconstruction.
	edgeActive.Store(false)
	runtime.fallback.Store(true)
	_, err = store.Pool.Exec(ctx, `UPDATE stream_sessions SET stop_after=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 minute') WHERE id=?1`, session)
	if err != nil {
		t.Fatal(err)
	}
	once()
	status, err = store.Status(ctx, stream.ID)
	if err != nil || !status.FallbackActive || status.Desired != "RUNNING" || runtime.Starts != 2 {
		t.Fatalf("fallback lost: %+v %v", status, err)
	}
	handler := (&application.API{Store: store, ReadKey: key, AdminToken: "operator"}).Handler()
	req := httptest.NewRequest("GET", "/v1/streams/"+stream.ID+"/status", nil)
	req.Header.Set("Authorization", "Bearer operator")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	var projected struct{ Status string }
	if json.Unmarshal(response.Body.Bytes(), &projected) != nil || projected.Status != "FALLBACK" {
		t.Fatalf("projection: %s", response.Body.String())
	}
	var allocationID string
	var generation uint64
	if err = store.Pool.QueryRow(ctx, `SELECT id,desired_generation FROM worker_allocations WHERE session_id=?1 AND state NOT IN ('STOPPED','FAILED')`, session).Scan(&allocationID, &generation); err != nil {
		t.Fatal(err)
	}
	claims := auth.EdgeClaims{StreamID: stream.ID, Action: "read", AllocationID: allocationID, Generation: generation}
	checkRead := func(claims auth.EdgeClaims, want int) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"action": "read", "protocol": "srt", "path": stream.ID, "password": auth.SignEdgeToken(claims, key)})
		req := httptest.NewRequest("POST", "/v1/edge/auth", bytes.NewReader(body))
		response := httptest.NewRecorder()
		privateAPI := &application.API{Store: store, ReadKey: key, EdgePeers: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}
		privateAPI.EdgeHandler().ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("read auth: got %d want %d", response.Code, want)
		}
	}
	checkRead(claims, 204)
	stale := claims
	stale.Generation++
	checkRead(stale, 403)
	edgeActive.Store(true)
	runtime.fallback.Store(false)
	once()
	status, _ = store.Status(ctx, stream.ID)
	if status.FallbackActive || status.Phase != "LIVE" {
		t.Fatalf("source return: %+v", status)
	}
	if err = store.StopSession(ctx, session); !errors.Is(err, persistence.ErrSourceConnected) {
		t.Fatalf("live source stop permitted: %v", err)
	}
	edgeActive.Store(false)
	if _, err = store.ObserveIngest(ctx, "test-edge", "conn", stream.ID, "srt", false, time.Now(), time.Second); err != nil {
		t.Fatal(err)
	}
	once()
	if _, err = store.Pool.Exec(ctx, `UPDATE edge_observation_health SET last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 minute') WHERE edge_id='test-edge'`); err != nil {
		t.Fatal(err)
	}
	if err = store.StopSession(ctx, session); !errors.Is(err, persistence.ErrSourceStateUnknown) {
		t.Fatalf("stale edge allowed stop: %v", err)
	}
	once()
	if err = store.StopSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	checkRead(claims, 403)
	once()
	once()
	status, _ = store.Status(ctx, stream.ID)
	if status.Phase != "ENDED" || runtime.Starts != 2 {
		t.Fatalf("operator stop resurrected: %+v starts %d", status, runtime.Starts)
	}
	// Reconstruct an exhausted, stopped allocation to exercise the terminal transaction.
	if _, err = store.Pool.Exec(ctx, `UPDATE stream_sessions SET phase='STARTING',desired='RUNNING',operator_stopped=false WHERE id=?1`, session); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE worker_allocations SET state='STARTING',desired='RUNNING',restart_count=5,retry_after=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 second') WHERE id=?1`, allocationID); err != nil {
		t.Fatal(err)
	}
	once()
	once()
	status, _ = store.Status(ctx, stream.ID)
	if status.Phase != "FAILED" || runtime.Starts != 2 {
		t.Fatalf("exhausted allocation restarted: %+v starts %d", status, runtime.Starts)
	}

}

func (r *reportingRuntime) Start(ctx context.Context, s workerruntime.WorkerSpec) (workerruntime.Worker, error) {
	w, err := r.Runtime.Start(ctx, s)
	if err == nil && r.loseResponse.Swap(false) {
		return workerruntime.Worker{}, errors.New("simulated lost start response")
	}
	return w, err
}

func testCapacity(slots int) domain.ResourceVector {
	return domain.ResourceVector{Slots: slots, CPUMillis: 8000, MemoryBytes: 8 << 30, IngressBPS: 100000000, EgressBPS: 100000000}
}
