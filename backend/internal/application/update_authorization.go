package application

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"streamtool-relay/internal/maintenance"
	"strings"
	"time"
)

var (
	errUpdateProof    = errors.New("invalid update authorization")
	errUpdateConflict = errors.New("update request conflict")
)

// authorizeUpdate is the durable authorization boundary, not a host start command.
// Its caller must check same-origin policy and authAdmission before invoking it,
// and verify digest against the independent coordinator's trusted release catalog.
// The owner HTTP route/dispatcher are disabled by default until host acceptance.
func (a *API) authorizeUpdate(ctx context.Context, token, requestID, digest, code string) (bool, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return false, errUpdateProof
	}
	if len(requestID) != 36 || requestID != strings.ToLower(requestID) || requestID[8] != '-' || requestID[13] != '-' || requestID[18] != '-' || requestID[23] != '-' {
		return false, errUpdateProof
	}
	rawID, err := hex.DecodeString(strings.ReplaceAll(requestID, "-", ""))
	if err != nil || len(rawID) != 16 {
		return false, errUpdateProof
	}
	rawDigest, err := hex.DecodeString(digest)
	if err != nil || len(rawDigest) != 32 || digest != strings.ToLower(digest) {
		return false, errUpdateProof
	}
	leave, err := maintenance.Enter(a.MaintenanceDirectory)
	if err != nil {
		return false, err
	}
	defer leave()
	tx, err := a.Store.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	hash := sessionHash(token)
	var uid string
	err = tx.QueryRow(ctx, `SELECT s.user_id FROM user_sessions s JOIN installation i ON i.owner_user=s.user_id JOIN owner_mfa m ON m.user_id=s.user_id WHERE s.token_hash=?1 AND s.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')`, hash).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errUpdateProof
	}
	if err != nil {
		return false, err
	}
	var oldDigest, oldUID string
	var oldHash []byte
	var oldState string
	var expired bool
	err = tx.QueryRow(ctx, `SELECT release_digest,user_id,session_hash,state,expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM update_authorizations WHERE request_id=?1`, requestID).Scan(&oldDigest, &oldUID, &oldHash, &oldState, &expired)
	if err == nil {
		if oldDigest != digest || oldUID != uid || subtle.ConstantTimeCompare(oldHash, hash) != 1 {
			return false, errUpdateConflict
		}
		if oldState == "CANCELLED" || (oldState == "PENDING" && expired) {
			return false, errUpdateConflict
		}
		// Exact authenticated retry is a receipt lookup, not a new factor grant.
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE update_authorizations SET state='CANCELLED' WHERE state='PENDING' AND expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')`); err != nil {
		return false, err
	}
	var pending int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM update_authorizations WHERE state='PENDING'`).Scan(&pending); err != nil {
		return false, err
	}
	if pending != 0 {
		return false, errUpdateConflict
	}
	var o ownerCredential
	o.id = uid
	if err = tx.QueryRow(ctx, `SELECT secret,last_step FROM owner_mfa WHERE user_id=?1`, uid).Scan(&o.encrypted, &o.lastStep); err != nil {
		return false, err
	}
	input := ownerCredentials{Code: code}
	step, ok, err := a.secondFactor(ctx, o, input)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errUpdateProof
	}
	ok, err = consumeFactor(ctx, tx, o, input, step)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errUpdateProof
	}
	if _, err = tx.Exec(ctx, `INSERT INTO update_authorizations(request_id,release_digest,user_id,session_hash,expires_at) VALUES(?1,?2,?3,?4,?5)`, requestID, digest, uid, hash, time.Now().Add(24*time.Hour)); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return false, nil
}
