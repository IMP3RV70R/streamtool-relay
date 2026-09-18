package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"streamtool-relay/internal/application"
	"streamtool-relay/internal/auth"
	"streamtool-relay/internal/control"
	appcrypto "streamtool-relay/internal/crypto"
	"streamtool-relay/internal/persistence"
	"streamtool-relay/internal/transport"
	"streamtool-relay/internal/updaterclient"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api terminated", "error", err)
		os.Exit(1)
	}
}
func run() error {
	recovering := len(os.Args) == 2 && os.Args[1] == "recover-owner"
	if len(os.Args) > 1 && !recovering {
		return errors.New("unknown command")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	databaseURL := os.Getenv("SQLITE_PATH")
	if databaseURL == "" {
		return errors.New("SQLITE_PATH required")
	}
	store, err := persistence.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if err = store.Migrate(ctx, valueOr("SQLITE_MIGRATIONS_DIR", "sqlite-migrations")); err != nil {
		return err
	}

	if recovering {
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 259))
		if err != nil || len(input) > 258 {
			return errors.New("password input unavailable")
		}
		password := strings.TrimSuffix(strings.TrimSuffix(string(input), "\n"), "\r")
		if err := application.RecoverOwner(ctx, store, password); err != nil {
			return errors.New("owner recovery failed; API must be stopped and password must contain 12–256 bytes")
		}
		slog.Info("owner recovered; enroll TOTP with the installation token and new password")
		return nil
	}
	environment := valueOr("STREAMTOOL_ENV", "production")
	if environment != "production" && environment != "development" {
		return errors.New("invalid STREAMTOOL_ENV")
	}
	providerMode := valueOr("ENVELOPE_PROVIDER", "file")
	if environment == "production" && (providerMode != "file" || os.Getenv("AUTH_INSECURE_COOKIES") == "true") {
		return errors.New("self-hosted production requires versioned file keys and secure cookies")
	}
	keys, err := appcrypto.NewVersionedProvider(os.Getenv("ENVELOPE_MANIFEST_FILE"), providerMode)
	if err != nil {
		return err
	}
	if _, err = keys.CurrentKeyID(ctx); err != nil {
		return err
	}
	adminToken, err := auth.ReadToken(os.Getenv("API_ADMIN_TOKEN_FILE"))
	if err != nil {
		return err
	}
	edgeToken, err := auth.ReadToken(os.Getenv("EDGE_CONTROL_TOKEN_FILE"))
	if err != nil {
		return err
	}
	readKey, err := os.ReadFile(os.Getenv("EDGE_READ_KEY_FILE"))
	if err != nil || len(readKey) < 32 {
		return errors.New("EDGE_READ_KEY_FILE must contain at least 32 bytes")
	}
	grace := 5 * time.Second
	api := &application.API{MaintenanceDirectory: os.Getenv("MAINTENANCE_DIRECTORY"), PublicRTMPURL: os.Getenv("PUBLIC_RTMP_URL"), MediaConfigured: os.Getenv("MEDIA_NODES_FILE") != "", PublicSRTURL: os.Getenv("PUBLIC_SRT_URL"), WebDir: valueOr("WEB_DIR", "../apps/web"), InsecureCookies: os.Getenv("AUTH_INSECURE_COOKIES") == "true", Store: store, Keys: keys, DestinationPolicy: application.DefaultDestinationPolicy(strings.Split(os.Getenv("ALLOWED_DESTINATION_HOSTS"), ",")), Grace: grace, AdminToken: adminToken, EdgeToken: edgeToken, ReadKey: readKey}
	api.UpdatesEnabled = os.Getenv("UPDATE_OWNER_ENABLED") == "true"
	if api.UpdatesEnabled {
		if os.Getenv("UPDATE_SOCKET") == "" || api.MaintenanceDirectory == "" {
			return errors.New("owner updates require host socket and maintenance gate")
		}
		api.Updater = &updaterclient.Client{Path: os.Getenv("UPDATE_SOCKET")}
	}

	setupToken, err := auth.ReadToken(os.Getenv("SETUP_TOKEN_FILE"))
	if err != nil {
		return err
	}
	api.SetupToken = setupToken
	api.SourceRegion = valueOr("NODE_REGION", "local")
	api.DestinationPolicy.Development = environment == "development"
	api.TrustedProxies, err = application.ParsePrefixes(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return err
	}
	api.EdgePeers, err = application.ParsePrefixes(os.Getenv("EDGE_AUTH_PEER_CIDRS"))
	if err != nil {
		return err
	}
	for _, prefix := range append(api.TrustedProxies, api.EdgePeers...) {
		if prefix.Bits() == 0 {
			return errors.New("trusting all addresses is forbidden")
		}
	}
	if environment == "production" && len(api.TrustedProxies) == 0 {
		return errors.New("production requires trusted HTTPS reverse proxy CIDRs")
	}
	if environment == "production" && (os.Getenv("API_TLS_CERT_FILE") == "" || os.Getenv("API_TLS_KEY_FILE") == "") {
		return errors.New("production API requires TLS certificate and key")
	}
	if edgeAddr := os.Getenv("EDGE_AUTH_ADDR"); edgeAddr != "" {
		if len(api.EdgePeers) == 0 {
			return errors.New("EDGE_AUTH_PEER_CIDRS required")
		}
		if environment == "production" && (os.Getenv("EDGE_AUTH_TLS_CERT_FILE") == "" || os.Getenv("EDGE_AUTH_TLS_KEY_FILE") == "") {
			return errors.New("production edge authorization requires TLS")
		}
		edgeServer := &http.Server{Addr: edgeAddr, Handler: api.EdgeHandler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 15 * time.Second}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			edgeServer.Shutdown(shutdown)
		}()
		edgeServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13}
		go func() {
			var err error
			if os.Getenv("EDGE_AUTH_TLS_CERT_FILE") != "" {
				err = edgeServer.ListenAndServeTLS(os.Getenv("EDGE_AUTH_TLS_CERT_FILE"), os.Getenv("EDGE_AUTH_TLS_KEY_FILE"))
			} else {
				err = edgeServer.ListenAndServe()
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("private edge listener failed")
				stop()
			}
		}()
	}
	if inventory := os.Getenv("MEDIA_NODES_FILE"); inventory != "" {
		nodes, err := control.LoadNodes(inventory)
		if err != nil {
			return err
		}
		if os.Getenv("WORKER_IMAGE") == "" || os.Getenv("EDGE_API_URL") == "" {
			return errors.New("WORKER_IMAGE and EDGE_API_URL are required")
		}
		if os.Getenv("EDGE_AUTH_ADDR") == "" {
			return errors.New("media requires private EDGE_AUTH_ADDR")
		}
		source, parseErr := url.Parse(os.Getenv("EDGE_SRT_URL"))
		if parseErr != nil || source.Scheme != "srt" || source.Host == "" || source.User != nil || source.RawQuery != "" {
			return errors.New("EDGE_SRT_URL must be an SRT endpoint without credentials or query")
		}
		client, err := transport.Client(os.Getenv("AGENT_CA_FILE"), os.Getenv("CONTROLLER_CERT_FILE"), os.Getenv("CONTROLLER_KEY_FILE"))
		if err != nil {
			return err
		}
		api.EdgeID = valueOr("EDGE_ID", "local-edge")
		edgeClient, err := edgeHTTPClient(environment, os.Getenv("EDGE_API_URL"), os.Getenv("EDGE_API_CA_FILE"))
		if err != nil {
			return err
		}
		controller := &control.Controller{MaintenanceDirectory: os.Getenv("MAINTENANCE_DIRECTORY"), Store: store, Keys: keys, Client: client, EdgeClient: edgeClient, Region: valueOr("NODE_REGION", "local"), NodeID: nodes[0].ID, AgentURL: nodes[0].AgentURL, Slots: nodes[0].Slots, WorkerImage: os.Getenv("WORKER_IMAGE"), EdgeURL: os.Getenv("EDGE_API_URL"), SourceURL: os.Getenv("EDGE_SRT_URL"), EdgeID: api.EdgeID, EdgeToken: edgeToken, ReadKey: readKey, Grace: grace}
		controller.Environment = environment
		controller.AllowedDestinationHosts = os.Getenv("ALLOWED_DESTINATION_HOSTS")
		go controller.Run(ctx)
	}

	go api.RunUpdateDispatch(ctx)
	server := &http.Server{Addr: valueOr("API_ADDR", ":8080"), Handler: api.Handler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				clean, cancel := context.WithTimeout(ctx, 3*time.Second)
				_, err := store.Pool.Exec(clean, `DELETE FROM auth_attempts WHERE expires_at<strftime('%Y-%m-%dT%H:%M:%fZ','now') AND key IN (SELECT key FROM auth_attempts WHERE expires_at<strftime('%Y-%m-%dT%H:%M:%fZ','now') LIMIT 10000)`)
				if err == nil {
					_, err = store.Pool.Exec(clean, `DELETE FROM user_sessions WHERE expires_at<strftime('%Y-%m-%dT%H:%M:%fZ','now') AND token_hash IN (SELECT token_hash FROM user_sessions WHERE expires_at<strftime('%Y-%m-%dT%H:%M:%fZ','now') LIMIT 10000)`)
				}
				cancel()
				if err != nil {
					slog.Warn("request limiter cleanup failed")
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	if os.Getenv("API_TLS_CERT_FILE") != "" {
		err = server.ListenAndServeTLS(os.Getenv("API_TLS_CERT_FILE"), os.Getenv("API_TLS_KEY_FILE"))
	} else {
		err = server.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func edgeHTTPClient(environment, endpoint, caFile string) (*http.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(environment == "development" && u.Scheme == "http")) {
		return nil, errors.New("invalid edge API endpoint: production requires HTTPS")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13}
	if caFile != "" {
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid edge API CA")
		}
		config.RootCAs = roots
	}
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: config}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func valueOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
