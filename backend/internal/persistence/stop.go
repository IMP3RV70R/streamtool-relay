package persistence

import (
	"context"
	"errors"
)

var ErrSourceConnected = errors.New("stop source transmission before ending broadcast")
var ErrSourceStateUnknown = errors.New("source disconnection has not been confirmed")

type rowReader interface {
	QueryRow(context.Context, string, ...any) Row
}

// A stalled video pipeline is not proof that the publisher disconnected. Require
// a fresh successful edge snapshot and no admitted connection, even for a forced
// slate or a disabled automatic slate. Never infer disconnection from stale data.
func stopReason(ctx context.Context, q rowReader, id string) (string, error) {
	var reason string
	err := q.QueryRow(ctx, `SELECT CASE
 WHEN ss.phase IN ('ENDED','FAILED') OR ss.desired='STOPPED' THEN 'SESSION_INACTIVE'
WHEN NOT EXISTS(SELECT 1 FROM edge_observation_health WHERE edge_id=ic.edge_id AND last_seen_at>strftime('%Y-%m-%dT%H:%M:%fZ','now','-10 seconds')) THEN 'SOURCE_STATE_UNKNOWN'
WHEN EXISTS(SELECT 1 FROM ingest_connections WHERE stream_id=ss.stream_id AND status='CONNECTED') THEN 'SOURCE_CONNECTED'
 ELSE '' END FROM stream_sessions ss JOIN ingest_connections ic ON ic.id=ss.ingest_connection_id WHERE ss.id=?1`, id).Scan(&reason)
	return reason, err
}

func (s *Store) StopEligibility(ctx context.Context, id string) (bool, string, error) {
	reason, err := stopReason(ctx, s.Pool, id)
	return err == nil && reason == "", reason, err
}

func (s *Store) StopSession(ctx context.Context, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var stream string
	if err = tx.QueryRow(ctx, `SELECT stream_id FROM stream_sessions WHERE id=?1`, id).Scan(&stream); err != nil {
		return err
	}
	// Same lock/order as publisher admission: a concurrent reconnect cannot be
	// admitted between the eligibility check and committing the stop intent.
	if err = tx.QueryRow(ctx, `SELECT id FROM streams WHERE id=?1`, stream).Scan(&stream); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM stream_sessions WHERE id=?1`, id); err != nil {
		return err
	}
	reason, err := stopReason(ctx, tx, id)
	if err != nil {
		return err
	}
	switch reason {
	case "SOURCE_CONNECTED":
		return ErrSourceConnected
	case "SOURCE_STATE_UNKNOWN":
		return ErrSourceStateUnknown
	case "SESSION_INACTIVE":
		return tx.Commit(ctx) // idempotent retry
	}
	if _, err = tx.Exec(ctx, `UPDATE stream_sessions SET operator_stopped=true,desired='STOPPED',phase='STOPPING',generation=generation+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
