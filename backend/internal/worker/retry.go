package worker

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
)

type ErrorClass int

const (
	Retryable ErrorClass = iota
	Permanent
)

func ClassifyDestinationError(err error) ErrorClass {
	if err == nil {
		return Permanent
	}
	s := strings.ToLower(err.Error())
	for _, marker := range []string{"auth", "unauthorized", "forbidden", "invalid stream key", "unsupported protocol", "policy denied"} {
		if strings.Contains(s, marker) {
			return Permanent
		}
	}
	return Retryable
}

type Backoff struct {
	Min, Max time.Duration
	Jitter   func() float64 // expected in [-1, 1]
}

func (b Backoff) Duration(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	factor := math.Pow(2, float64(attempt))
	d := time.Duration(float64(b.Min) * factor)
	if d > b.Max || d < 0 {
		d = b.Max
	}
	if b.Jitter != nil {
		j := b.Jitter()
		if j < -1 {
			j = -1
		}
		if j > 1 {
			j = 1
		}
		d = time.Duration(float64(d) * (1 + 0.2*j))
	}
	return d
}

type Clock interface {
	Sleep(context.Context, time.Duration) error
	Now() time.Time
}
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var ErrRetriesExhausted = errors.New("destination retries exhausted")
