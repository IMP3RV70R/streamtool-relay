package application

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"streamtool-relay/internal/auth"
	"streamtool-relay/internal/buildinfo"
	appcrypto "streamtool-relay/internal/crypto"
	"streamtool-relay/internal/maintenance"
	"streamtool-relay/internal/netpolicy"
	"streamtool-relay/internal/persistence"
	"streamtool-relay/internal/updaterclient"
	"strings"
	"time"
)

type API struct {
	Updater                       updaterclient.Service
	UpdatesEnabled                bool
	MaintenanceDirectory          string
	SetupToken                    string
	TrustedProxies, EdgePeers     []netip.Prefix
	PublicRTMPURL                 string
	MediaConfigured               bool
	SourceRegion                  string
	PublicSRTURL                  string
	WebDir                        string
	InsecureCookies               bool
	Store                         *persistence.Store
	Keys                          appcrypto.KeyProvider
	DestinationPolicy             netpolicy.RTMPPolicy
	Grace                         time.Duration
	AdminToken, EdgeToken, EdgeID string
	ReadKey                       []byte
}

func (a *API) Handler() http.Handler {
	requests := make(chan struct{}, 64)
	passwords := make(chan struct{}, 2)
	uploads := make(chan struct{}, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /operator", operatorPage)
	mux.Handle("GET /", a.webHandler())
	mux.HandleFunc("GET /v1/installation", func(w http.ResponseWriter, r *http.Request) {
		var schema int
		if err := a.Store.Pool.QueryRow(r.Context(), "SELECT count(*) FROM schema_migrations").Scan(&schema); err != nil {
			problem(w, 503, "installation unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"version": buildinfo.Version, "schema": schema, "maintenance_directory": a.MaintenanceDirectory})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /v1/accounts", a.createAccount)
	mux.HandleFunc("POST /v1/streams", a.createStream)
	mux.HandleFunc("GET /v1/streams/{id}", a.getStream)
	mux.HandleFunc("PATCH /v1/streams/{id}", a.updateStream)
	mux.HandleFunc("POST /v1/streams/{id}/destinations", a.createDestination)
	mux.HandleFunc("GET /v1/streams/{id}/status", a.status)
	mux.HandleFunc("POST /v1/sessions/{id}/stop", a.stopSession)
	mux.HandleFunc("POST /v1/nodes/{id}/drain", a.drainNode)
	mux.HandleFunc("POST /v1/destinations/{id}/retry", a.retryDestination)
	mux.HandleFunc("POST /v1/edge/observations", a.edgeObservation)
	return recoverJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			leave, err := maintenance.Enter(a.MaintenanceDirectory)
			if err != nil {
				w.Header().Set("Retry-After", "30")
				problem(w, 503, "server maintenance")
				return
			}
			defer leave()
		}
		if !acquire(w, requests) {
			return
		}
		defer func() { <-requests }()
		if r.Method == "PUT" && r.URL.Path == "/v1/me/source/fallback" {
			if !acquire(w, uploads) {
				return
			}
			defer func() { <-uploads }()
		}
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") || r.URL.Path == "/v1/me" || strings.HasPrefix(r.URL.Path, "/v1/me/") {
			a.userRoutes(w, r, passwords)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/") && r.Method == "GET" {
			mux.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/v1/edge/auth" || strings.HasPrefix(r.URL.Path, "/v1/twitch/") || strings.HasPrefix(r.URL.Path, "/v1/channels/") {
			problem(w, 404, "not found")
			return
		}
		if r.URL.Path == "/healthz" || r.URL.Path == "/" {
			mux.ServeHTTP(w, r)
			return
		}
		token := a.AdminToken
		if r.URL.Path == "/v1/edge/observations" {
			token = a.EdgeToken
		}
		if !auth.Bearer(r, token) {
			problem(w, 401, "unauthorized")
			return
		}
		mux.ServeHTTP(w, r)
	}))
}
func (a *API) createAccount(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if decode(w, r, &input) != nil || input.Name == "" {
		problem(w, 400, "invalid account")
		return
	}
	id, err := a.Store.CreateAccount(r.Context(), input.Name)
	if err != nil {
		problem(w, 409, "account not created")
		return
	}
	writeJSON(w, 201, map[string]string{"id": id, "name": input.Name})
}
func (a *API) createStream(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AccountID string `json:"account_id"`
		Name      string `json:"name"`
		Region    string `json:"region"`
	}
	if decode(w, r, &input) != nil || input.AccountID == "" || input.Name == "" {
		problem(w, 400, "invalid stream")
		return
	}
	keyBytes := make([]byte, 24)
	rand.Read(keyBytes)
	key := base64.RawURLEncoding.EncodeToString(keyBytes)
	hash, err := auth.HashKey(key)
	if err != nil {
		problem(w, 500, "key generation failed")
		return
	}
	if input.Region == "" {
		input.Region = "local"
	}
	stream, err := a.Store.CreateStream(r.Context(), input.AccountID, input.Name, input.Region, hash)
	if err != nil {
		problem(w, 409, "stream not created")
		return
	}
	writeJSON(w, 201, map[string]any{"stream": publicStream(stream), "ingest_key": key})
}
func (a *API) getStream(w http.ResponseWriter, r *http.Request) {
	stream, _, err := a.Store.GetStream(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, "stream not found")
		return
	}
	w.Header().Set("ETag", strconv.FormatInt(stream.Generation, 10))
	writeJSON(w, 200, publicStream(stream))
}
func (a *API) updateStream(w http.ResponseWriter, r *http.Request) {
	generation, err := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), "\""), 10, 64)
	if err != nil {
		problem(w, 428, "If-Match generation required")
		return
	}
	var input struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if decode(w, r, &input) != nil {
		problem(w, 400, "invalid stream")
		return
	}
	stream, err := a.Store.UpdateStream(r.Context(), r.PathValue("id"), generation, input.Name, input.Enabled)
	if err != nil {
		problem(w, 409, "generation conflict")
		return
	}
	w.Header().Set("ETag", strconv.FormatInt(stream.Generation, 10))
	writeJSON(w, 200, publicStream(stream))
}
func (a *API) createDestination(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name     string `json:"name"`
		Endpoint string `json:"endpoint"`
		Secret   string `json:"secret"`
	}
	if decode(w, r, &input) != nil || input.Name == "" || input.Secret == "" {
		problem(w, 400, "invalid destination")
		return
	}
	normalized, err := a.DestinationPolicy.Validate(r.Context(), input.Endpoint)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	streamID := r.PathValue("id")
	envelope, err := appcrypto.Encrypt(r.Context(), a.Keys, []byte(input.Secret), []byte(streamID+":"+input.Name))
	if err != nil {
		problem(w, 500, "secret encryption failed")
		return
	}
	d, err := a.Store.CreateDestination(r.Context(), persistence.Destination{StreamID: streamID, Name: input.Name, Endpoint: normalized, KeyID: envelope.KeyID, Ciphertext: envelope.Ciphertext, Nonce: envelope.Nonce})
	if err != nil {
		problem(w, 409, "destination not created")
		return
	}
	writeJSON(w, 201, map[string]any{"id": d.ID, "stream_id": d.StreamID, "name": d.Name, "endpoint": d.Endpoint, "enabled": d.Enabled, "generation": d.Generation})
}

type edgeAuthRequest struct {
	User      string `json:"user"`
	Password  string `json:"password"`
	Token     string `json:"token"`
	IP        string `json:"ip"`
	Action    string `json:"action"`
	Path      string `json:"path"`
	Protocol  string `json:"protocol"`
	ID        string `json:"id"`
	Query     string `json:"query"`
	UserAgent string `json:"userAgent"`
}

func (a *API) edgeAuth(w http.ResponseWriter, r *http.Request, ip netip.Addr) {
	var input edgeAuthRequest
	if decode(w, r, &input) != nil {
		problem(w, 403, "denied")
		return
	}
	if input.Action == "api" && input.User == "controller" && auth.Matches(input.Password, a.EdgeToken) {
		w.WriteHeader(204)
		return
	}
	// The controller capability is stateless and must remain available for host
	// readiness under exclusive maintenance. Media admission and limiter writes
	// still require the shared fence.
	leave, err := maintenance.Enter(a.MaintenanceDirectory)
	if err != nil {
		problem(w, 503, "server maintenance")
		return
	}
	defer leave()
	if a.Store != nil {
		allowed, err := a.allow(r.Context(), "edge:"+ip.String(), 6000, 60)
		if err != nil {
			problem(w, 503, "authorization unavailable")
			return
		}
		if !allowed {
			problem(w, 429, "too many requests")
			return
		}
	}
	if input.Action == "read" && input.Protocol == "srt" && len(a.ReadKey) >= 32 {
		claims, err := auth.VerifyEdgeToken(input.Password, a.ReadKey, time.Now())
		if err == nil && claims.Action == "read" && claims.StreamID == input.Path && claims.AllocationID != "" && claims.Generation > 0 && a.Store != nil {
			var allowed bool
			err = a.Store.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM worker_allocations wa JOIN stream_sessions ss ON ss.id=wa.session_id JOIN streams s ON s.id=ss.stream_id WHERE wa.id=?1 AND wa.desired_generation=?2 AND ss.stream_id=?3 AND wa.desired='RUNNING' AND wa.state NOT IN ('STOPPED','FAILED') AND ss.desired='RUNNING' AND ss.phase NOT IN ('ENDED','FAILED') AND s.enabled)`, claims.AllocationID, claims.Generation, claims.StreamID).Scan(&allowed)
			if err != nil || !allowed {
				problem(w, 403, "denied")
				return
			}
			w.WriteHeader(204)
			return
		}
	}
	if input.Action != "publish" || (input.Protocol != "srt" && input.Protocol != "rtmp") {
		problem(w, 403, "denied")
		return
	}
	sourceIP, ipErr := netip.ParseAddr(input.IP)
	if ipErr != nil || len(input.Password) > 256 || len(input.Path) > 80 || len(input.ID) > 80 {
		problem(w, 403, "invalid publisher request")
		return
	}
	if a.Store != nil {
		allowed, err := a.allow(r.Context(), "publisher:"+sourceIP.Unmap().String(), 100, 900)
		if err != nil {
			problem(w, 503, "publisher limiter unavailable")
			return
		}
		if !allowed {
			problem(w, 429, "too many attempts")
			return
		}
	}
	stream, hash, err := a.Store.GetStream(r.Context(), input.Path)
	if err != nil || !stream.Enabled || !auth.VerifyKey(hash, input.Password) {
		problem(w, 403, "denied")
		return
	}
	if a.EdgeID != "" {
		if _, err = a.Store.ObserveIngest(r.Context(), a.EdgeID, input.ID, input.Path, input.Protocol, true, time.Now().UTC(), a.Grace, hash); err != nil {
			problem(w, 403, "publisher not admitted")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *API) edgeObservation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		EdgeID       string    `json:"edge_id"`
		ConnectionID string    `json:"connection_id"`
		StreamID     string    `json:"stream_id"`
		Protocol     string    `json:"protocol"`
		Connected    bool      `json:"connected"`
		SeenAt       time.Time `json:"seen_at"`
	}
	if decode(w, r, &input) != nil || (a.EdgeID != "" && input.EdgeID != a.EdgeID) {
		problem(w, 400, "invalid observation")
		return
	}
	id, err := a.Store.ObserveIngest(r.Context(), input.EdgeID, input.ConnectionID, input.StreamID, input.Protocol, input.Connected, input.SeenAt, a.Grace)
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"session_id": id})
}
func (a *API) status(w http.ResponseWriter, r *http.Request) {
	status, err := a.Store.Status(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, "session not found")
		return
	}
	projected := "TRANSITIONING"
	if status.Phase == "ENDED" {
		projected = "STOPPED"
	} else if status.Phase == "FAILED" {
		projected = "FAILED"
	} else if time.Since(status.LastSeen) > 30*time.Second {
		projected = "UNKNOWN"
	} else if status.Phase == "LIVE" && status.AllocationState == "RUNNING" {
		projected = "LIVE"
		if status.FallbackActive {
			projected = "FALLBACK"
		} else if status.InputUnavailable {
			projected = "NO_SIGNAL"
		}
		for _, d := range status.Destinations {
			if d.State != "STREAMING" {
				projected = "DEGRADED"
			}
		}
	} else if status.Phase == "FAILED" {
		projected = "FAILED"
	}
	canStop, stopReason, err := a.Store.StopEligibility(r.Context(), status.SessionID)
	if err != nil {
		problem(w, 503, "source state unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"status": projected, "session": status, "can_stop": canStop, "stop_block_reason": stopReason})
}
func (a *API) stopSession(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.StopSession(r.Context(), r.PathValue("id")); err != nil {
		switch {
		case errors.Is(err, persistence.ErrSourceConnected):
			problem(w, 409, persistence.ErrSourceConnected.Error())
		case errors.Is(err, persistence.ErrSourceStateUnknown):
			problem(w, 409, persistence.ErrSourceStateUnknown.Error())
		case errors.Is(err, sql.ErrNoRows):
			problem(w, 404, "session not found")
		default:
			problem(w, 503, "broadcast stop unavailable")
		}
		return
	}
	w.WriteHeader(202)
}
func (a *API) drainNode(w http.ResponseWriter, r *http.Request) {
	if a.Store.DrainNode(r.Context(), r.PathValue("id")) != nil {
		problem(w, 404, "node not found")
		return
	}
	w.WriteHeader(202)
}
func (a *API) retryDestination(w http.ResponseWriter, r *http.Request) {
	if a.Store.RetryDestination(r.Context(), r.PathValue("id")) != nil {
		problem(w, 404, "destination not found")
		return
	}
	w.WriteHeader(202)
}
func publicStream(s persistence.Stream) map[string]any {
	return map[string]any{"id": s.ID, "account_id": s.AccountID, "name": s.Name, "region": s.Region, "enabled": s.Enabled, "generation": s.Generation, "created_at": s.CreatedAt, "updated_at": s.UpdatedAt}
}
func decode(w http.ResponseWriter, r *http.Request, target any) error {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message, "status": status})
}
func recoverJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				problem(w, 500, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func DefaultDestinationPolicy(allowed []string) netpolicy.RTMPPolicy {
	hosts := map[string]bool{}
	for _, h := range allowed {
		hosts[h] = true
	}
	return netpolicy.RTMPPolicy{Resolver: net.DefaultResolver, AllowHosts: hosts}
}
