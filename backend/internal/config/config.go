package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultQueueBytes     = 8 * 1024 * 1024
	defaultReconnectMin   = time.Second
	defaultReconnectMax   = 30 * time.Second
	defaultShutdown       = 10 * time.Second
	defaultStableInterval = 30 * time.Second
	defaultMaxAttempts    = 10
)

// Config is deliberately safe to serialize: secret values live in Secret and
// its String method never reveals them.
type Config struct {
	MediaProfile      MediaProfile
	FallbackImage     string
	FallbackVideo     string
	SlateOnSourceLoss bool
	SlateForced       bool
	SessionID         string
	SourceURL         string
	SourceAuthToken   bool
	SourceToken       Secret
	QueueMaxBytes     uint64
	ReconnectMin      time.Duration
	ReconnectMax      time.Duration
	ReconnectAttempts int
	StableInterval    time.Duration
	ShutdownTimeout   time.Duration
	MetricsAddress    string
	Destinations      []Destination
}

type Destination struct {
	ID         string
	Generation uint64
	BaseURL    string
	Secret     Secret
}

type DestinationDocument struct {
	ID         string `json:"id"`
	Generation uint64 `json:"generation"`
	URL        string `json:"url"`
	SecretFile string `json:"secret_file"`
}

type Secret struct{ value string }

func (Secret) String() string   { return "[REDACTED]" }
func (Secret) GoString() string { return "[REDACTED]" }
func (s Secret) Empty() bool    { return s.value == "" }

// Reveal is intended only for the trusted pipeline construction path.
func (s Secret) Reveal() string { return s.value }

type Lookup func(string) (string, bool)

func FromEnv() (Config, error) { return Load(os.LookupEnv, os.ReadFile) }

func Load(lookup Lookup, readFile func(string) ([]byte, error)) (Config, error) {
	c := Config{
		MediaProfile:      DefaultMediaProfile(),
		SessionID:         value(lookup, "WORKER_SESSION_ID"),
		FallbackImage:     value(lookup, "WORKER_FALLBACK_IMAGE"),
		FallbackVideo:     value(lookup, "WORKER_FALLBACK_VIDEO"),
		SlateOnSourceLoss: true,
		SourceAuthToken:   value(lookup, "WORKER_SOURCE_AUTH") == "token",
		SourceURL:         value(lookup, "WORKER_SOURCE_URL"),
		MetricsAddress:    valueOr(lookup, "WORKER_METRICS_ADDR", ":9090"),
	}
	destinationsFile := value(lookup, "WORKER_DESTINATIONS_FILE")

	var err error
	if raw := value(lookup, "WORKER_MEDIA_PROFILE"); raw != "" {
		if err = json.Unmarshal([]byte(raw), &c.MediaProfile); err != nil {
			return Config{}, errors.New("invalid media profile")
		}
	}
	if c.SlateOnSourceLoss, err = parseBool(lookup, "WORKER_SLATE_ON_SOURCE_LOSS", true); err != nil {
		return Config{}, err
	}
	if c.SlateForced, err = parseBool(lookup, "WORKER_SLATE_FORCED", false); err != nil {
		return Config{}, err
	}
	if c.SourceToken, err = secretFile(lookup, readFile, "WORKER_SOURCE_TOKEN_FILE"); err != nil {
		return Config{}, err
	}
	if destinationsFile == "" {
		return Config{}, errors.New("WORKER_DESTINATIONS_FILE is required")
	}
	if c.Destinations, err = LoadDestinationSnapshot(destinationsFile, readFile); err != nil {
		return Config{}, err
	}
	if c.QueueMaxBytes, err = parseUint(lookup, "WORKER_QUEUE_MAX_BYTES", defaultQueueBytes); err != nil {
		return Config{}, err
	}
	if c.ReconnectMin, err = parseDuration(lookup, "WORKER_RECONNECT_MIN", defaultReconnectMin); err != nil {
		return Config{}, err
	}
	if c.ReconnectMax, err = parseDuration(lookup, "WORKER_RECONNECT_MAX", defaultReconnectMax); err != nil {
		return Config{}, err
	}
	attempts, err := parseUint(lookup, "WORKER_RECONNECT_MAX_ATTEMPTS", defaultMaxAttempts)
	if err != nil {
		return Config{}, err
	}
	c.ReconnectAttempts = int(attempts)
	if c.StableInterval, err = parseDuration(lookup, "WORKER_STABLE_INTERVAL", defaultStableInterval); err != nil {
		return Config{}, err
	}
	if c.ShutdownTimeout, err = parseDuration(lookup, "WORKER_SHUTDOWN_TIMEOUT", defaultShutdown); err != nil {
		return Config{}, err
	}
	return c, c.Validate()
}

func parseBool(lookup Lookup, key string, fallback bool) (bool, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return parsed, nil
}

func (c Config) Validate() error {
	var errs []error
	if err := c.MediaProfile.Validate(); err != nil {
		errs = append(errs, err)
	}
	if !isUUID(c.SessionID) {
		errs = append(errs, errors.New("WORKER_SESSION_ID must be a UUID"))
	}
	if err := validateURL(c.SourceURL, "srt", "WORKER_SOURCE_URL"); err != nil {
		errs = append(errs, err)
	}
	seenDestinations := map[string]bool{}
	for _, d := range c.Destinations {
		if d.ID == "" || d.Generation == 0 || seenDestinations[d.ID] {
			errs = append(errs, fmt.Errorf("invalid or duplicate destination %q", d.ID))
		}
		seenDestinations[d.ID] = true
		if err := validateURL(d.BaseURL, "rtmp", "destination URL"); err != nil {
			errs = append(errs, err)
		}
		if d.Secret.Empty() {
			errs = append(errs, fmt.Errorf("destination %q secret must not be empty", d.ID))
		}
	}
	if c.SourceToken.Empty() {
		errs = append(errs, errors.New("source token file must not be empty"))
	}
	if len(c.Destinations) < 1 || len(c.Destinations) > 8 {
		errs = append(errs, errors.New("one to eight destinations are required"))
	}
	if c.QueueMaxBytes == 0 {
		errs = append(errs, errors.New("WORKER_QUEUE_MAX_BYTES must be positive"))
	}
	if c.ReconnectMin <= 0 || c.ReconnectMax < c.ReconnectMin {
		errs = append(errs, errors.New("reconnect durations must be positive and max >= min"))
	}
	if c.ReconnectAttempts <= 0 {
		errs = append(errs, errors.New("WORKER_RECONNECT_MAX_ATTEMPTS must be positive"))
	}
	if c.StableInterval <= 0 || c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("stable and shutdown durations must be positive"))
	}
	return errors.Join(errs...)
}

func (c Config) SourceLocation() string {
	u, _ := url.Parse(c.SourceURL)
	q := u.Query()
	if c.SourceAuthToken {
		q.Set("streamid", q.Get("streamid")+":worker:"+c.SourceToken.Reveal())
	} else {
		q.Set("passphrase", c.SourceToken.Reveal())
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (d Destination) Location() string {
	return strings.TrimRight(d.BaseURL, "/") + "/" + url.PathEscape(d.Secret.Reveal())
}

func (c Config) SecretValues() []string {
	values := []string{c.SourceToken.Reveal()}
	for _, d := range c.Destinations {
		values = append(values, d.Secret.Reveal())
	}
	return values
}

func LoadDestinationSnapshot(path string, readFile func(string) ([]byte, error)) ([]Destination, error) {
	b, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("read destination snapshot: %w", err)
	}
	var docs []DestinationDocument
	if err := json.Unmarshal(b, &docs); err != nil {
		return nil, fmt.Errorf("decode destination snapshot: %w", err)
	}
	return ResolveDestinationDocuments(docs, readFile)
}

func ResolveDestinationDocuments(docs []DestinationDocument, readFile func(string) ([]byte, error)) ([]Destination, error) {
	if len(docs) > 8 {
		return nil, errors.New("at most eight outputs allowed")
	}
	result := make([]Destination, 0, len(docs))
	seen := map[string]bool{}
	for _, doc := range docs {
		if doc.ID == "" || doc.Generation == 0 || seen[doc.ID] {
			return nil, fmt.Errorf("invalid or duplicate destination %q", doc.ID)
		}
		seen[doc.ID] = true
		if err := validateURL(doc.URL, "rtmp", "destination URL"); err != nil {
			return nil, err
		}
		secretBytes, err := readFile(doc.SecretFile)
		if err != nil {
			return nil, fmt.Errorf("read destination %q secret: %w", doc.ID, err)
		}
		secret := strings.TrimSpace(string(secretBytes))
		if secret == "" {
			return nil, fmt.Errorf("destination %q secret must not be empty", doc.ID)
		}
		result = append(result, Destination{ID: doc.ID, Generation: doc.Generation, BaseURL: doc.URL, Secret: Secret{value: secret}})
	}
	return result, nil
}

func secretFile(lookup Lookup, readFile func(string) ([]byte, error), key string) (Secret, error) {
	path := value(lookup, key)
	if path == "" {
		return Secret{}, fmt.Errorf("%s is required", key)
	}
	b, err := readFile(path)
	if err != nil {
		return Secret{}, fmt.Errorf("read %s: %w", key, err)
	}
	return Secret{value: strings.TrimSpace(string(b))}, nil
}

func validateURL(raw, scheme, key string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != scheme && !(scheme == "rtmp" && u.Scheme == "rtmps")) || u.Host == "" || u.User != nil || u.Fragment != "" || (scheme == "rtmp" && u.RawQuery != "") {
		return fmt.Errorf("%s must be an absolute %s URL without credentials or fragment", key, scheme)
	}
	return nil
}

func isUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func value(lookup Lookup, key string) string { v, _ := lookup(key); return strings.TrimSpace(v) }
func valueOr(lookup Lookup, key, fallback string) string {
	if v := value(lookup, key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(lookup Lookup, key string, fallback time.Duration) (time.Duration, error) {
	v := value(lookup, key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func parseUint(lookup Lookup, key string, fallback uint64) (uint64, error) {
	v := value(lookup, key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}
