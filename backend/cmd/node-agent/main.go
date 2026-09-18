package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"streamtool-relay/internal/domain"
	"streamtool-relay/internal/node"
	workerruntime "streamtool-relay/internal/runtime"
	dockerruntime "streamtool-relay/internal/runtime/docker"
	"time"
)

func main() {
	runtime := dockerruntime.Runtime{CLI: dockerruntime.CLI{}, Network: os.Getenv("WORKER_NETWORK")}
	environment := valueOr("STREAMTOOL_ENV", "production")
	if environment != "development" && environment != "production" {
		log.Fatal("invalid STREAMTOOL_ENV")
	}
	if environment == "production" {
		if runtime.Network == "" {
			log.Fatal("production requires dedicated WORKER_NETWORK")
		}
		runtime.Policy = dockerruntime.CLI{Binary: "sudo", PrefixArgs: []string{"-n", "/usr/local/libexec/streamtool-worker-policy"}}
	}
	slots, err := strconv.Atoi(valueOr("NODE_SLOTS", "1"))
	if err != nil || slots < 1 {
		log.Fatal("NODE_SLOTS must be positive")
	}
	if os.Getenv("WORKER_IMAGE") == "" {
		log.Fatal("WORKER_IMAGE required")
	}
	if !workerruntime.ValidID(os.Getenv("NODE_ID")) {
		log.Fatal("NODE_ID must be a UUID")
	}
	capacity := domain.ResourceVector{Slots: slots, CPUMillis: positive("NODE_CPU_MILLIS", "2000"), MemoryBytes: positive("NODE_MEMORY_BYTES", "1073741824"), IngressBPS: positive("NODE_INGRESS_BPS", "100000000"), EgressBPS: positive("NODE_EGRESS_BPS", "100000000")}
	agent := node.New(os.Getenv("NODE_ID"), runtime, capacity)
	agent.MaintenanceDirectory = os.Getenv("MAINTENANCE_DIRECTORY")
	agent.StateFile = valueOr("AGENT_STATE_FILE", "/var/lib/streamtool/fence.json")
	agent.AllowedImage = os.Getenv("WORKER_IMAGE")
	handler := agent.Handler()
	server := &http.Server{Addr: valueOr("AGENT_ADDR", ":8443"), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != valueOr("CONTROLLER_ID", "streamtool-controller") {
			http.Error(w, "forbidden", 403)
			return
		}
		handler.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 3 * time.Second, TLSConfig: mustTLS()}
	log.Fatal(server.ListenAndServeTLS(os.Getenv("AGENT_CERT_FILE"), os.Getenv("AGENT_KEY_FILE")))
}
func positive(key, fallback string) int64 {
	n, err := strconv.ParseInt(valueOr(key, fallback), 10, 64)
	if err != nil || n <= 0 {
		log.Fatalf("%s must be a positive integer", key)
	}
	return n
}
func mustTLS() *tls.Config {
	ca, err := os.ReadFile(os.Getenv("AGENT_CA_FILE"))
	if err != nil {
		panic(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		panic(errors.New("invalid agent CA"))
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
}
func valueOr(k, v string) string {
	if got := os.Getenv(k); got != "" {
		return got
	}
	return v
}
