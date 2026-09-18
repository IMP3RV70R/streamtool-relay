package telemetry

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerRedactsEveryOccurrence(t *testing.T) {
	var out bytes.Buffer
	r := NewRedactor("super-secret", "token 123")
	l := NewLogger(&out, r, slog.LevelInfo)
	l.Error("failed super-secret", "url", "srt://host?passphrase=token+123")
	got := out.String()
	if strings.Contains(got, "super-secret") || strings.Contains(got, "token+123") {
		t.Fatalf("secret leaked: %s", got)
	}
	if strings.Count(got, "[REDACTED]") != 2 {
		t.Fatalf("redaction missing: %s", got)
	}
}
