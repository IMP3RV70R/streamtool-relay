package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"
)

type Stream struct {
	ID, AccountID, Name, Region string
	Enabled                     bool
	Generation                  int64
	CreatedAt, UpdatedAt        time.Time
}
type Destination struct {
	ID, StreamID, Name, Endpoint, KeyID string
	Enabled                             bool
	Generation                          int64
	Ciphertext, Nonce                   []byte
	CreatedAt, UpdatedAt                time.Time
}
type SessionStatus struct {
	InputError       string              `json:"input_error,omitempty"`
	FallbackActive   bool                `json:"fallback_active"`
	FallbackForced   bool                `json:"fallback_forced"`
	InputLive        bool                `json:"input_live"`
	InputUnavailable bool                `json:"input_unavailable"`
	SessionID        string              `json:"session_id"`
	Phase            string              `json:"phase"`
	Desired          string              `json:"desired"`
	AllocationState  string              `json:"allocation_state"`
	Generation       int64               `json:"generation"`
	NodeID           *string             `json:"node_id,omitempty"`
	LastSeen         time.Time           `json:"last_seen"`
	Destinations     []DestinationStatus `json:"destinations"`
}
type DestinationStatus struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	State      string    `json:"state"`
	LastError  string    `json:"last_error,omitempty"`
	Reconnects int64     `json:"reconnects"`
	BytesSent  int64     `json:"bytes_sent"`
	Generation int64     `json:"generation"`
	LastSeen   time.Time `json:"last_seen"`
}

func (s *Store) CreateAccount(ctx context.Context, name string) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, "INSERT INTO accounts(name) VALUES(?1) RETURNING id", name).Scan(&id)
	return id, err
}
func (s *Store) CreateStream(ctx context.Context, accountID, name, region string, hash []byte) (Stream, error) {
	var v Stream
	err := s.Pool.QueryRow(ctx, `INSERT INTO streams(account_id,name,region,ingest_key_hash) VALUES(?1,?2,?3,?4) RETURNING id,account_id,name,region,enabled,generation,created_at,updated_at`, accountID, name, region, hash).Scan(&v.ID, &v.AccountID, &v.Name, &v.Region, &v.Enabled, &v.Generation, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}
func (s *Store) GetStream(ctx context.Context, id string) (Stream, []byte, error) {
	var v Stream
	var hash []byte
	err := s.Pool.QueryRow(ctx, `SELECT id,account_id,name,region,enabled,generation,created_at,updated_at,ingest_key_hash FROM streams WHERE id=?1`, id).Scan(&v.ID, &v.AccountID, &v.Name, &v.Region, &v.Enabled, &v.Generation, &v.CreatedAt, &v.UpdatedAt, &hash)
	return v, hash, err
}
func (s *Store) UpdateStream(ctx context.Context, id string, generation int64, name string, enabled bool) (Stream, error) {
	var v Stream
	err := s.Pool.QueryRow(ctx, `UPDATE streams SET name=?3,enabled=?4,generation=generation+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1 AND generation=?2 RETURNING id,account_id,name,region,enabled,generation,created_at,updated_at`, id, generation, name, enabled).Scan(&v.ID, &v.AccountID, &v.Name, &v.Region, &v.Enabled, &v.Generation, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, errors.New("generation conflict")
	}
	return v, err
}
func (s *Store) CreateDestination(ctx context.Context, d Destination) (Destination, error) {
	err := s.Pool.QueryRow(ctx, `INSERT INTO destinations(stream_id,name,endpoint,secret_ciphertext,secret_nonce,key_id) VALUES(?1,?2,?3,?4,?5,?6) RETURNING id,stream_id,name,endpoint,key_id,enabled,generation,secret_ciphertext,secret_nonce,created_at,updated_at`, d.StreamID, d.Name, d.Endpoint, d.Ciphertext, d.Nonce, d.KeyID).Scan(&d.ID, &d.StreamID, &d.Name, &d.Endpoint, &d.KeyID, &d.Enabled, &d.Generation, &d.Ciphertext, &d.Nonce, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}
func (s *Store) ObserveIngest(ctx context.Context, edgeID, connectionID, streamID, protocol string, connected bool, seen time.Time, grace time.Duration, expectedHash ...[]byte) (string, error) {
	if edgeID == "" || connectionID == "" || (protocol != "srt" && protocol != "rtmp") || seen.IsZero() || seen.After(time.Now().Add(5*time.Second)) {
		return "", errors.New("invalid observation")
	}
	seen = seen.UTC().Truncate(time.Millisecond)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var locked string
	var currentHash []byte
	if err = tx.QueryRow(ctx, "SELECT id,ingest_key_hash FROM streams WHERE id=?1 AND enabled", streamID).Scan(&locked, &currentHash); err != nil {
		return "", err
	}
	if len(expectedHash) > 0 && !bytes.Equal(currentHash, expectedHash[0]) {
		return "", errors.New("publisher credential changed")
	}
	var oldStream, oldStatus string
	var oldSeen time.Time
	err = tx.QueryRow(ctx, "SELECT stream_id,status,last_seen_at FROM ingest_connections WHERE edge_id=?1 AND connection_id=?2", edgeID, connectionID).Scan(&oldStream, &oldStatus, &oldSeen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil && oldStream != streamID {
		return "", errors.New("connection belongs to another stream")
	}
	if err == nil && (seen.Before(oldSeen) || (seen.Equal(oldSeen) && oldStatus == "DISCONNECTED" && connected)) {
		return "", tx.Commit(ctx)
	}
	if connected {
		var other bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ingest_connections WHERE stream_id=?1 AND status='CONNECTED' AND (edge_id,connection_id)<>(?2,?3))`, streamID, edgeID, connectionID).Scan(&other); err != nil {
			return "", err
		}
		if other {
			return "", errors.New("active publisher already exists")
		}
	}
	status := "DISCONNECTED"
	if connected {
		status = "CONNECTED"
	}
	var connectionUUID string
	err = tx.QueryRow(ctx, `INSERT INTO ingest_connections(edge_id,connection_id,stream_id,protocol,status,connected_at,last_seen_at,disconnected_at) VALUES(?1,?2,?3,?4,?5,?6,?6,CASE WHEN ?5='DISCONNECTED' THEN ?6 END) ON CONFLICT(edge_id,connection_id) DO UPDATE SET status=excluded.status,last_seen_at=excluded.last_seen_at,disconnected_at=excluded.disconnected_at RETURNING id`, edgeID, connectionID, streamID, protocol, status, seen).Scan(&connectionUUID)
	if err != nil {
		return "", err
	}
	var sessionID string
	if connected {
		// A stopped publisher must disconnect and obtain a new connection before a new session.
		err = tx.QueryRow(ctx, `SELECT id FROM stream_sessions WHERE ingest_connection_id=?1 AND (operator_stopped OR phase IN ('ENDED','FAILED')) ORDER BY started_at DESC LIMIT 1`, connectionUUID).Scan(&sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRow(ctx, `UPDATE stream_sessions SET stop_after=NULL,ingest_connection_id=?2,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE stream_id=?1 AND phase NOT IN ('ENDED','FAILED') AND NOT operator_stopped RETURNING id`, streamID, connectionUUID).Scan(&sessionID)
			if errors.Is(err, sql.ErrNoRows) {
				err = tx.QueryRow(ctx, `INSERT INTO stream_sessions(stream_id,ingest_connection_id,phase) VALUES(?1,?2,'SCHEDULING') RETURNING id`, streamID, connectionUUID).Scan(&sessionID)
			}
		}
	} else {
		err = tx.QueryRow(ctx, `UPDATE stream_sessions SET stop_after=COALESCE(stop_after,?2),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE stream_id=?1 AND ingest_connection_id=?3 AND phase NOT IN ('ENDED','FAILED') RETURNING id`, streamID, seen.Add(grace), connectionUUID).Scan(&sessionID)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return sessionID, tx.Commit(ctx)
}

func (s *Store) Status(ctx context.Context, streamID string) (SessionStatus, error) {
	var status SessionStatus
	err := s.Pool.QueryRow(ctx, `SELECT ss.id,ss.phase,ss.desired,ss.generation,COALESCE(wa.state,'PENDING'),wa.node_id,COALESCE(wa.last_healthy_at,ss.updated_at),COALESCE(wa.fallback_active,false),COALESCE(wa.fallback_forced,false),COALESCE(wa.input_live,false),COALESCE(wa.input_unavailable,false),COALESCE(wa.input_error,'') FROM stream_sessions ss LEFT JOIN worker_allocations wa ON wa.session_id=ss.id AND wa.state NOT IN ('STOPPED','FAILED') WHERE ss.stream_id=?1 ORDER BY ss.started_at DESC LIMIT 1`, streamID).Scan(&status.SessionID, &status.Phase, &status.Desired, &status.Generation, &status.AllocationState, &status.NodeID, &status.LastSeen, &status.FallbackActive, &status.FallbackForced, &status.InputLive, &status.InputUnavailable, &status.InputError)
	if err != nil {
		return status, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT d.id,d.name,dr.state,dr.generation,dr.reconnect_count,dr.bytes_sent,COALESCE(dr.last_error_code,''),dr.last_seen_at FROM destination_runtimes dr JOIN destinations d ON d.id=dr.destination_id JOIN worker_allocations wa ON wa.id=dr.allocation_id WHERE wa.session_id=?1 AND wa.state NOT IN ('STOPPED','FAILED') AND d.enabled AND NOT d.archived ORDER BY d.name`, status.SessionID)
	if err != nil {
		return status, err
	}
	defer rows.Close()
	for rows.Next() {
		var d DestinationStatus
		if err := rows.Scan(&d.ID, &d.Name, &d.State, &d.Generation, &d.Reconnects, &d.BytesSent, &d.LastError, &d.LastSeen); err != nil {
			return status, err
		}
		status.Destinations = append(status.Destinations, d)
	}
	return status, rows.Err()
}
func (s *Store) DrainNode(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE media_nodes SET state='DRAINING',generation=generation+1 WHERE id=?1 AND state<>'OFFLINE' AND NOT EXISTS(SELECT 1 FROM node_fences WHERE node_id=?1)`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return sql.ErrNoRows
	}
	return err
}
func (s *Store) RetryDestination(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE destinations SET generation=generation+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return sql.ErrNoRows
	}
	return err
}
