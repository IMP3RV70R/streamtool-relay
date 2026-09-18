package telemetry

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
)

type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

func NewRedactor(values ...string) *Redactor { r := &Redactor{}; r.Add(values...); return r }
func (r *Redactor) Add(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			for _, variant := range []string{v, url.QueryEscape(v), url.PathEscape(v)} {
				if variant != "" {
					r.secrets = append(r.secrets, variant)
				}
			}
		}
	}
}
func (r *Redactor) String(v string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, secret := range r.secrets {
		v = strings.ReplaceAll(v, secret, "[REDACTED]")
	}
	return v
}

type redactingWriter struct {
	dst      io.Writer
	redactor *Redactor
}

func (w redactingWriter) Write(p []byte) (int, error) {
	redacted := []byte(w.redactor.String(string(p)))
	_, err := w.dst.Write(redacted)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func NewLogger(dst io.Writer, redactor *Redactor, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(redactingWriter{dst: dst, redactor: redactor}, &slog.HandlerOptions{Level: level}))
}

func LogEvent(logger *slog.Logger, event any) {
	b, err := json.Marshal(event)
	if err != nil {
		logger.Error("event serialization failed", "error", err)
		return
	}
	logger.Info("worker lifecycle", "event_payload", string(b))
}
