package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"streamtool-relay/internal/auth"
	"streamtool-relay/internal/config"
	"streamtool-relay/internal/netpolicy"
	"streamtool-relay/internal/pipeline"
	"streamtool-relay/internal/runtime/workerio"
	"streamtool-relay/internal/telemetry"
	"streamtool-relay/internal/worker"
)

var (
	version = "dev"
	commit  = "none"
	builtAt = "unknown"
)

func main() {
	if len(os.Args) > 1 {
		if len(os.Args) != 3 || os.Args[1] != "runtime-io" {
			slog.Error("invalid private worker command")
			os.Exit(1)
		}
		if err := workerio.Execute(os.Args[2], os.Stdin, os.Stdout); err != nil {
			slog.Error("private worker transport failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("stream worker terminated", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// librtmp is native code and can otherwise terminate the whole worker with
	// SIGPIPE when a destination closes its socket during a write.
	signal.Ignore(syscall.SIGPIPE)
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	redactor := telemetry.NewRedactor(cfg.SecretValues()...)
	logger := telemetry.NewLogger(os.Stdout, redactor, slog.LevelInfo).With("component", "stream-worker", "session_id", cfg.SessionID, "version", version, "commit", commit, "built_at", builtAt)
	slog.SetDefault(logger)

	relay := &netpolicy.Relay{Policy: netpolicy.RTMPPolicy{Resolver: netpolicy.NetResolver{Resolver: net.DefaultResolver}, Development: os.Getenv("STREAMTOOL_ENV") == "development", AllowHosts: map[string]bool{}}}
	for _, host := range strings.Split(os.Getenv("ALLOWED_DESTINATION_HOSTS"), ",") {
		relay.Policy.AllowHosts[strings.TrimSpace(host)] = true
	}
	defer relay.Close()
	destinations, currentKeys, err := protectedSpecs(relay, cfg.Destinations)
	if err != nil {
		return err
	}
	var destinationMu sync.Mutex
	runtime, err := pipeline.NewRuntime(pipeline.Spec{MediaProfile: cfg.MediaProfile, FallbackImage: cfg.FallbackImage, FallbackVideo: cfg.FallbackVideo, SlatePolicy: pipeline.SlatePolicy{OnSourceLoss: cfg.SlateOnSourceLoss, Forced: cfg.SlateForced}, SourceLocation: cfg.SourceLocation(), Destinations: destinations, QueueMaxBytes: cfg.QueueMaxBytes})
	if err != nil {
		return err
	}
	metrics := telemetry.NewMetrics()
	status := &worker.Status{}
	token, err := auth.ReadToken(os.Getenv("WORKER_CONTROL_TOKEN_FILE"))
	if err != nil {
		return err
	}
	events := worker.SinkFunc(func(e worker.Event) { metrics.Observe(e); status.Observe(e); telemetry.LogEvent(logger, e) })

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(status.Snapshot()) })
	mux.HandleFunc("PUT /v1/destinations", func(w http.ResponseWriter, request *http.Request) {
		destinationMu.Lock()
		defer destinationMu.Unlock()
		defer request.Body.Close()
		var docs []config.DestinationDocument
		decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&docs); err != nil {
			http.Error(w, "invalid destination snapshot", http.StatusBadRequest)
			return
		}
		for _, doc := range docs {
			if !strings.HasPrefix(doc.SecretFile, "/run/secrets/") || strings.Contains(doc.SecretFile, "..") {
				http.Error(w, "secret_file must be below /run/secrets", http.StatusBadRequest)
				return
			}
		}
		resolved, err := config.ResolveDestinationDocuments(docs, os.ReadFile)
		if err != nil {
			logger.Warn("destination snapshot rejected", "error", err)
			http.Error(w, "invalid destination snapshot", http.StatusBadRequest)
			return
		}
		for _, d := range resolved {
			redactor.Add(d.Secret.Reveal())
		}
		specs, keys, err := protectedSpecs(relay, resolved)
		if err != nil {
			relay.Retain(currentKeys)
			http.Error(w, "invalid relay target", 400)
			return
		}
		if err := runtime.ApplyDestinations(request.Context(), specs); err != nil {
			relay.Retain(currentKeys)
			logger.Warn("destination snapshot not applied", "error", err)
			http.Error(w, "destination snapshot conflict", http.StatusConflict)
			return
		}
		currentKeys = keys
		relay.Retain(keys)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /v1/slate", func(w http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var policy pipeline.SlatePolicy
		decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&policy); err != nil {
			http.Error(w, "invalid slate policy", http.StatusBadRequest)
			return
		}
		if err := runtime.ApplySlatePolicy(request.Context(), policy); err != nil {
			logger.Warn("slate policy not applied", "error", err)
			http.Error(w, "slate policy conflict", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: cfg.MetricsAddress, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.Bearer(r, token) {
			http.Error(w, "unauthorized", 401)
			return
		}
		mux.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 2 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server failed", "error", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	service := worker.Service{
		SessionID: cfg.SessionID, DestinationIDs: destinationIDs(cfg.Destinations), Runtime: runtime, Events: events, Clock: worker.RealClock{},
		Backoff:         worker.Backoff{Min: cfg.ReconnectMin, Max: cfg.ReconnectMax, Jitter: func() float64 { return rand.Float64()*2 - 1 }},
		StableInterval:  cfg.StableInterval,
		ShutdownTimeout: cfg.ShutdownTimeout,
		MaxAttempts:     cfg.ReconnectAttempts,
	}
	err = service.Run(ctx)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if shutdownErr := server.Shutdown(shutdownCtx); err == nil {
		err = shutdownErr
	}
	return err
}

func protectedSpecs(relay *netpolicy.Relay, destinations []config.Destination) ([]pipeline.DestinationSpec, map[string]bool, error) {
	specs := make([]pipeline.DestinationSpec, 0, len(destinations))
	keys := map[string]bool{}
	for _, d := range destinations {
		key := d.ID + ":" + strconv.FormatUint(d.Generation, 10)
		local, err := relay.Location(key, d.BaseURL)
		if err != nil {
			return nil, nil, err
		}
		d.BaseURL = local
		keys[key] = true
		specs = append(specs, pipeline.DestinationSpec{ID: d.ID, Generation: d.Generation, Location: d.Location()})
	}
	return specs, keys, nil
}

func pipelineSpecs(destinations []config.Destination) []pipeline.DestinationSpec {
	result := make([]pipeline.DestinationSpec, 0, len(destinations))
	for _, d := range destinations {
		result = append(result, pipeline.DestinationSpec{ID: d.ID, Generation: d.Generation, Location: d.Location()})
	}
	return result
}
func destinationIDs(destinations []config.Destination) []string {
	result := make([]string, 0, len(destinations))
	for _, d := range destinations {
		result = append(result, d.ID)
	}
	return result
}
