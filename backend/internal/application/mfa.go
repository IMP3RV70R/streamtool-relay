package application

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	qrcode "github.com/skip2/go-qrcode"
	"net/http"
	"net/url"
	"streamtool-relay/internal/auth"
	appcrypto "streamtool-relay/internal/crypto"
	"streamtool-relay/internal/persistence"
	"strings"
	"time"
)

type ownerCredentials struct {
	Password        string `json:"password"`
	Code            string `json:"code"`
	RecoveryCode    string `json:"recovery_code"`
	EnrollmentToken string `json:"enrollment_token"`
}
type ownerCredential struct {
	id                    string
	hash, salt, encrypted []byte
	lastStep              int64
}

func (a *API) owner(ctx context.Context) (ownerCredential, error) {
	var o ownerCredential
	err := a.Store.Pool.QueryRow(ctx, `SELECT u.id,u.password_hash,u.password_salt,COALESCE(m.secret,x''),COALESCE(m.last_step,-1) FROM installation i JOIN users u ON u.id=i.owner_user LEFT JOIN owner_mfa m ON m.user_id=u.id WHERE i.singleton=1`).Scan(&o.id, &o.hash, &o.salt, &o.encrypted, &o.lastStep)
	return o, err
}
func (a *API) sealTOTP(ctx context.Context, secret []byte, purpose string) ([]byte, error) {
	if a.Keys == nil {
		return nil, errors.New("key unavailable")
	}
	e, err := appcrypto.Encrypt(ctx, a.Keys, secret, []byte(purpose))
	if err != nil {
		return nil, err
	}
	return json.Marshal(e)
}
func (a *API) openTOTP(ctx context.Context, encrypted []byte, purpose string) ([]byte, error) {
	if a.Keys == nil {
		return nil, errors.New("key unavailable")
	}
	var e appcrypto.Envelope
	if json.Unmarshal(encrypted, &e) != nil {
		return nil, errors.New("invalid encrypted authenticator")
	}
	return appcrypto.Decrypt(ctx, a.Keys, e, []byte(purpose))
}
func opaqueToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), err
}
func recoveryHash(code string) []byte {
	return sessionHash("owner-recovery:" + strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", "")))
}
func newRecoveryCodes() ([]string, [][]byte, error) {
	var codes []string
	var hashes [][]byte
	for i := 0; i < 10; i++ {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		raw := auth.TOTPSecret(b)
		code := raw[:5] + "-" + raw[5:10] + "-" + raw[10:15] + "-" + raw[15:20] + "-" + raw[20:]
		codes = append(codes, code)
		hashes = append(hashes, recoveryHash(code))
	}
	return codes, hashes, nil
}

// Verification and consumption are separate so consumption and session/enrollment
// creation commit together. Concurrent replay cannot create two sessions.
func (a *API) secondFactor(ctx context.Context, o ownerCredential, input ownerCredentials) (int64, bool, error) {
	if (input.Code == "") == (input.RecoveryCode == "") || len(o.encrypted) == 0 {
		return 0, false, nil
	}
	if input.RecoveryCode != "" {
		if len(input.RecoveryCode) > 40 {
			return 0, false, nil
		}
		return -1, true, nil
	}
	secret, err := a.openTOTP(ctx, o.encrypted, "owner-totp:"+o.id)
	if err != nil {
		return 0, false, err
	}
	step, ok := auth.VerifyTOTP(secret, input.Code, time.Now())
	return step, ok && step > o.lastStep, nil
}
func consumeFactor(ctx context.Context, tx *persistence.Tx, o ownerCredential, input ownerCredentials, step int64) (bool, error) {
	var tag persistence.Tag
	var err error
	if input.RecoveryCode != "" {
		tag, err = tx.Exec(ctx, `DELETE FROM owner_recovery WHERE user_id=?1 AND code_hash=?2`, o.id, recoveryHash(input.RecoveryCode))
	} else {
		tag, err = tx.Exec(ctx, `UPDATE owner_mfa SET last_step=?2 WHERE user_id=?1 AND last_step<?2 AND secret=?3`, o.id, step, o.encrypted)
	}
	return tag.RowsAffected() == 1, err
}
func insertSession(ctx context.Context, tx *persistence.Tx, uid, raw string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE token_hash IN (SELECT token_hash FROM user_sessions WHERE expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') LIMIT 128)`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO user_sessions(token_hash,user_id,expires_at) VALUES(?1,?2,?3)`, sessionHash(raw), uid, time.Now().Add(7*24*time.Hour))
	return err
}
func (a *API) signIn(w http.ResponseWriter, r *http.Request, passwords chan struct{}) {
	if a.Store == nil {
		problem(w, 503, "authentication unavailable")
		return
	}
	if !a.authAdmission(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	setup := r.URL.Path == "/v1/auth/setup"
	if setup && !auth.Matches(r.Header.Get("X-Setup-Token"), a.SetupToken) {
		problem(w, 403, "invalid setup token")
		return
	}
	var input ownerCredentials
	if decode(w, r, &input) != nil {
		problem(w, 400, "invalid credentials")
		return
	}
	if r.URL.Path == "/v1/auth/setup/confirm" {
		a.confirmEnrollment(w, r, input)
		return
	}
	if len(input.Password) < 12 || len(input.Password) > 256 {
		problem(w, 400, "password (12–256 bytes) required")
		return
	}
	if !acquire(w, passwords) {
		return
	}
	defer func() { <-passwords }()
	o, err := a.owner(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		problem(w, 503, "authentication unavailable")
		return
	}
	if setup {
		if len(o.encrypted) != 0 {
			problem(w, 409, "installation already configured")
			return
		}
		if o.id != "" && subtle.ConstantTimeCompare(passwordHash(input.Password, o.salt), o.hash) != 1 {
			problem(w, 401, "invalid password or code")
			return
		}
		a.beginEnrollment(w, r, o, input, false, 0)
		return
	}
	salt, hash := o.salt, o.hash
	if o.id == "" {
		salt = make([]byte, 16)
		hash = make([]byte, 32)
	}
	passwordOK := subtle.ConstantTimeCompare(passwordHash(input.Password, salt), hash) == 1 && o.id != ""
	step, factorOK, factorErr := a.secondFactor(r.Context(), o, input)
	if factorErr != nil {
		problem(w, 503, "authentication unavailable")
		return
	}
	if !passwordOK || !factorOK {
		problem(w, 401, "invalid password or code")
		return
	}
	if r.URL.Path == "/v1/auth/totp/replace" {
		a.beginEnrollment(w, r, o, input, true, step)
		return
	}
	raw, err := opaqueToken()
	if err != nil {
		problem(w, 503, "authentication unavailable")
		return
	}
	tx, err := a.Store.Pool.Begin(r.Context())
	if err != nil {
		problem(w, 503, "authentication unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	ok, err := consumeFactor(r.Context(), tx, o, input, step)
	if err != nil {
		problem(w, 503, "authentication unavailable")
		return
	}
	if !ok {
		problem(w, 401, "invalid password or code")
		return
	}
	if insertSession(r.Context(), tx, o.id, raw) != nil || tx.Commit(r.Context()) != nil {
		problem(w, 503, "authentication unavailable")
		return
	}
	a.sessionCookie(w, raw, 7*24*3600)
	writeJSON(w, 200, map[string]bool{"authenticated": true})
}
func (a *API) beginEnrollment(w http.ResponseWriter, r *http.Request, o ownerCredential, input ownerCredentials, replace bool, step int64) {
	ctx := r.Context()
	raw, err := opaqueToken()
	if err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	purpose := "totp-enrollment:" + base64.RawURLEncoding.EncodeToString(sessionHash(raw))
	encrypted, err := a.sealTOTP(ctx, secret, purpose)
	if err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	encodedHashes, _ := json.Marshal(hashes)
	salt, hash := o.salt, o.hash
	if o.id == "" {
		salt = make([]byte, 16)
		if _, err = rand.Read(salt); err != nil {
			problem(w, 503, "setup unavailable")
			return
		}
		hash = passwordHash(input.Password, salt)
	}
	uri := "otpauth://totp/streamtool-relay:owner?" + url.Values{"secret": {auth.TOTPSecret(secret)}, "issuer": {"streamtool-relay"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode()
	png, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	tx, err := a.Store.Pool.Begin(ctx)
	if err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	defer tx.Rollback(ctx)
	if replace {
		ok, err := consumeFactor(ctx, tx, o, input, step)
		if err != nil {
			problem(w, 503, "setup unavailable")
			return
		}
		if !ok {
			problem(w, 401, "invalid password or code")
			return
		}
	} else {
		var enabled bool
		if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM owner_mfa)`).Scan(&enabled) != nil {
			problem(w, 503, "setup unavailable")
			return
		}
		if enabled {
			problem(w, 409, "installation already configured")
			return
		}
	}
	var uid any
	if o.id != "" {
		uid = o.id
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth_enrollment(singleton,token_hash,user_id,password_hash,password_salt,secret,recovery_hashes,expires_at) VALUES(1,?1,?2,?3,?4,?5,?6,?7) ON CONFLICT(singleton) DO UPDATE SET token_hash=excluded.token_hash,user_id=excluded.user_id,password_hash=excluded.password_hash,password_salt=excluded.password_salt,secret=excluded.secret,recovery_hashes=excluded.recovery_hashes,expires_at=excluded.expires_at`, sessionHash(raw), uid, hash, salt, encrypted, encodedHashes, time.Now().Add(10*time.Minute))
	if err != nil || tx.Commit(ctx) != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"enrollment_token": raw, "secret": auth.TOTPSecret(secret), "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), "recovery_codes": codes, "expires_in": 600})
}
func (a *API) confirmEnrollment(w http.ResponseWriter, r *http.Request, input ownerCredentials) {
	ctx := r.Context()
	if len(input.EnrollmentToken) != 43 || input.RecoveryCode != "" {
		problem(w, 401, "invalid enrollment or code")
		return
	}
	tx, err := a.Store.Pool.Begin(ctx)
	if err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var uid sql.NullString
	var hash, salt, encrypted, encodedHashes []byte
	err = tx.QueryRow(ctx, `SELECT user_id,password_hash,password_salt,secret,recovery_hashes FROM auth_enrollment WHERE singleton=1 AND token_hash=?1 AND expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')`, sessionHash(input.EnrollmentToken)).Scan(&uid, &hash, &salt, &encrypted, &encodedHashes)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 401, "invalid enrollment or code")
		return
	}
	if err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	secret, err := a.openTOTP(ctx, encrypted, "totp-enrollment:"+base64.RawURLEncoding.EncodeToString(sessionHash(input.EnrollmentToken)))
	if err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	step, ok := auth.VerifyTOTP(secret, input.Code, time.Now())
	if !ok {
		problem(w, 401, "invalid enrollment or code")
		return
	}
	if !uid.Valid {
		var account string
		if tx.QueryRow(ctx, `INSERT INTO accounts(name) VALUES('Owner') RETURNING id`).Scan(&account) != nil || tx.QueryRow(ctx, `INSERT INTO users(account_id,email,password_hash,password_salt) VALUES(?1,'',?2,?3) RETURNING id`, account, hash, salt).Scan(&uid.String) != nil {
			problem(w, 409, "installation already configured")
			return
		}
		if _, err = tx.Exec(ctx, `INSERT INTO installation(singleton,owner_user) VALUES(1,?1)`, uid.String); err != nil {
			problem(w, 409, "installation already configured")
			return
		}
	} else {
		var matches bool
		if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM installation i JOIN users u ON u.id=i.owner_user WHERE u.id=?1 AND u.password_hash=?2 AND u.password_salt=?3)`, uid.String, hash, salt).Scan(&matches) != nil {
			problem(w, 503, "setup unavailable")
			return
		}
		if !matches {
			problem(w, 401, "invalid enrollment or code")
			return
		}
	}
	encrypted, err = a.sealTOTP(ctx, secret, "owner-totp:"+uid.String)
	if err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	var hashes [][]byte
	if json.Unmarshal(encodedHashes, &hashes) != nil || len(hashes) != 10 {
		problem(w, 503, "setup unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO owner_mfa(user_id,secret,last_step) VALUES(?1,?2,?3) ON CONFLICT(user_id) DO UPDATE SET secret=excluded.secret,last_step=excluded.last_step`, uid.String, encrypted, step); err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM owner_recovery WHERE user_id=?1`, uid.String); err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	for _, h := range hashes {
		if _, err = tx.Exec(ctx, `INSERT INTO owner_recovery(user_id,code_hash) VALUES(?1,?2)`, uid.String, h); err != nil {
			problem(w, 503, "setup unavailable")
			return
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=?1`, uid.String); err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE update_authorizations SET state='CANCELLED' WHERE user_id=?1 AND state='PENDING'`, uid.String); err != nil {
		problem(w, 503, "authentication unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_enrollment WHERE singleton=1`); err != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	raw, err := opaqueToken()
	if err != nil || insertSession(ctx, tx, uid.String, raw) != nil || tx.Commit(ctx) != nil {
		problem(w, 503, "setup unavailable")
		return
	}
	a.sessionCookie(w, raw, 7*24*3600)
	writeJSON(w, 200, map[string]bool{"authenticated": true})
}

// RecoverOwner is an offline host-admin action under the exclusive SQLite lock.
// It never changes media configuration, ingest keys or encryption-key material.
func RecoverOwner(ctx context.Context, store *persistence.Store, password string) error {
	if len(password) < 12 || len(password) > 256 {
		return errors.New("password must contain 12–256 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return errors.New("password generation unavailable")
	}
	hash := passwordHash(password, salt)
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var uid string
	if err = tx.QueryRow(ctx, `SELECT owner_user FROM installation WHERE singleton=1`).Scan(&uid); err != nil {
		return errors.New("owner unavailable")
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=?2,password_salt=?3 WHERE id=?1`, uid, hash, salt); err != nil {
		return err
	}
	for _, statement := range []string{`UPDATE update_authorizations SET state='CANCELLED' WHERE state='PENDING'`, `DELETE FROM user_sessions`, `DELETE FROM auth_enrollment`, `DELETE FROM owner_recovery`, `DELETE FROM owner_mfa`} {
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	// Recovery does not provide an HTTP bypass or reuse the installation token as
	// a login credential. Password + installation token must enroll a fresh TOTP.
	return tx.Commit(ctx)
}
