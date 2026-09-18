package application

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"streamtool-relay/internal/auth"
	appcrypto "streamtool-relay/internal/crypto"
	"streamtool-relay/internal/persistence"
	"strings"
	"testing"
	"time"
)

func TestSourceProvisioning(t *testing.T) {
	database := filepath.Join(t.TempDir(), "control.sqlite")
	ctx := context.Background()
	s, err := persistence.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, "../../sqlite-migrations"); err != nil {
		t.Fatal(err)
	}
	account, err := s.CreateAccount(ctx, "source provision test")
	if err != nil {
		t.Fatal(err)
	}
	operatorStream, err := s.CreateStream(ctx, account, "operator stream", "local", []byte("operator-key-hash"))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{Store: s, Keys: appcrypto.LocalProvider{ID: "test", Keys: map[string][]byte{"test": []byte("01234567890123456789012345678901")}}}
	out := make(chan *httptest.ResponseRecorder, 4)
	for range 4 {
		go func() {
			w := httptest.NewRecorder()
			a.source(w, httptest.NewRequest("POST", "/v1/me/source", nil), account)
			out <- w
		}()
	}
	var id, key string
	issued := 0
	for range 4 {
		w := <-out
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var c map[string]any
		if err = json.Unmarshal(w.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		if id == "" {
			id = c["source_id"].(string)
		}
		if k := c["ingest_key"].(string); k != "" {
			key = k
			issued++
		}
		if c["source_id"] != id {
			t.Fatal("source or key changed")
		}
	}
	var n int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM streams WHERE account_id=?1`, account).Scan(&n); err != nil || n != 2 {
		t.Fatal("duplicate source", n, err)
	}
	if id == operatorStream.ID {
		t.Fatal("operator stream was adopted as the website source")
	}
	var onSourceLoss, forced bool
	if err = s.Pool.QueryRow(ctx, `SELECT on_source_loss,forced FROM source_slates WHERE source_id=?1`, id).Scan(&onSourceLoss, &forced); err != nil || !onSourceLoss || forced {
		t.Fatal("default slate settings missing", onSourceLoss, forced, err)
	}

	wMedia := httptest.NewRecorder()
	a.userMedia(wMedia, httptest.NewRequest("GET", "/v1/me/source/media", nil), id)
	if wMedia.Code != 200 {
		t.Fatal("profile not provisioned", wMedia.Body.String())
	}
	profileBody := `{"width":1920,"height":1080,"fps_num":60000,"fps_den":1001,"video_kbps":6000,"audio_kbps":160,"generation":1}`
	for _, want := range []int{200, 409} {
		wMedia = httptest.NewRecorder()
		a.userMedia(wMedia, httptest.NewRequest("PUT", "/v1/me/source/media", strings.NewReader(profileBody)), id)
		if wMedia.Code != want {
			t.Fatal("profile generation", wMedia.Code, wMedia.Body.String())
		}
	}

	if issued != 1 {
		t.Fatal("key must be issued once", issued)
	}
	_, oldHash, err := s.GetStream(ctx, id)
	if err != nil || !auth.VerifyKey(oldHash, key) {
		t.Fatal("invalid issued key", err)
	}
	salt := []byte("test-password-salt")
	if _, err = s.Pool.Exec(ctx, `INSERT INTO users(account_id,email,password_hash,password_salt) VALUES(?1,?2,?3,?4)`, account, account+"@example.com", passwordHash("source-password-123", salt), salt); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.source(w, httptest.NewRequest("POST", "/v1/me/source/credential", strings.NewReader(`{"password":"wrong-password-123"}`)), account)
	if w.Code != 403 {
		t.Fatal("wrong rotation password accepted")
	}
	w = httptest.NewRecorder()
	a.source(w, httptest.NewRequest("POST", "/v1/me/source/credential", strings.NewReader(`{"password":"source-password-123"}`)), account)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, newHash, err := s.GetStream(ctx, id)
	if err != nil || auth.VerifyKey(newHash, key) {
		t.Fatal("old key still accepted", err)
	}
	if _, err = s.ObserveIngest(ctx, "test-edge", "current", id, "srt", true, time.Now(), time.Second, newHash); err != nil {
		t.Fatal(err)
	}
	wMedia = httptest.NewRecorder()
	a.userMedia(wMedia, httptest.NewRequest("PUT", "/v1/me/source/media", strings.NewReader(strings.Replace(profileBody, `"generation":1`, `"generation":2`, 1))), id)
	if wMedia.Code != 409 {
		t.Fatal("active media profile changed", wMedia.Code)
	}

	w = httptest.NewRecorder()
	a.source(w, httptest.NewRequest("POST", "/v1/me/source/credential", strings.NewReader(`{"password":"source-password-123"}`)), account)
	if w.Code != 409 {
		t.Fatal("active source key rotation must be rejected", w.Code)
	}
	if _, err = s.ObserveIngest(ctx, "test-edge", "stale", id, "srt", true, time.Now(), time.Second, oldHash); err == nil {
		t.Fatal("stale in-flight authorization admitted after rotation")
	}
}
