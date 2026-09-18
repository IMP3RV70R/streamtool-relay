package telemetry

import (
	"fmt"
	"net/http"
	"sync"

	"streamtool-relay/internal/worker"
)

type Metrics struct {
	mu         sync.RWMutex
	state      map[worker.EventType]uint64
	reconnects uint64
}

func NewMetrics() *Metrics { return &Metrics{state: make(map[worker.EventType]uint64)} }
func (m *Metrics) Observe(e worker.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state[e.Type]++
	if e.Type == worker.DestinationReconnecting {
		m.reconnects++
	}
}
func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintln(w, "# HELP stream_worker_lifecycle_events_total Worker lifecycle events.")
	fmt.Fprintln(w, "# TYPE stream_worker_lifecycle_events_total counter")
	for event, n := range m.state {
		fmt.Fprintf(w, "stream_worker_lifecycle_events_total{event=%q} %d\n", event, n)
	}
	fmt.Fprintln(w, "# HELP stream_worker_destination_reconnects_total Destination reconnect attempts.")
	fmt.Fprintln(w, "# TYPE stream_worker_destination_reconnects_total counter")
	fmt.Fprintf(w, "stream_worker_destination_reconnects_total %d\n", m.reconnects)
}
