package sessions

import (
	"context"
	"time"
)

type Item struct {
	ID                string
	Desired, Observed string
	Generation        uint64
	UpdatedAt         time.Time
}
type Repository interface {
	Pending(context.Context, int) ([]Item, error)
	EnsureAllocation(context.Context, Item) error
	CloseExpired(context.Context, time.Time) error
}
type Reconciler struct {
	Repository Repository
	Interval   time.Duration
}

func (r Reconciler) Run(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.Once(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (r Reconciler) Once(ctx context.Context) error {
	items, err := r.Repository.Pending(ctx, 100)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := r.Repository.EnsureAllocation(ctx, item); err != nil {
			return err
		}
	}
	return r.Repository.CloseExpired(ctx, time.Now())
}
