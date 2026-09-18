package worker

import (
	"errors"
	"testing"
	"time"
)

func TestBackoffCapsAndJitters(t *testing.T) {
	b := Backoff{Min: time.Second, Max: 30 * time.Second}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := b.Duration(i); got != w {
			t.Fatalf("attempt %d = %s, want %s", i, got, w)
		}
	}
	b.Jitter = func() float64 { return 1 }
	if got := b.Duration(0); got != 1200*time.Millisecond {
		t.Fatalf("jitter = %s", got)
	}
}

func TestDestinationErrorClassification(t *testing.T) {
	if ClassifyDestinationError(errors.New("401 unauthorized: invalid stream key")) != Permanent {
		t.Fatal("auth error should be permanent")
	}
	if ClassifyDestinationError(errors.New("connection reset by peer")) != Retryable {
		t.Fatal("reset should retry")
	}
}
