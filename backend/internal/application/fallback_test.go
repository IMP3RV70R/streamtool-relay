package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/png"
	"net/http/httptest"
	"path/filepath"
	"streamtool-relay/internal/persistence"
	"testing"
)

func TestFallbackValidation(t *testing.T) {
	var imageBytes bytes.Buffer
	png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 16, 16)))
	kind, canonical, err := validateFallback(imageBytes.Bytes(), "image/png")
	if err != nil || kind != "image" || len(canonical) == 0 {
		t.Fatal(err)
	}
	if _, _, err = validateFallback([]byte("not a movie"), "video/mp4"); err == nil {
		t.Fatal("accepted invalid MP4")
	}
	if _, _, err = validateFallback(imageBytes.Bytes(), "application/octet-stream"); err == nil {
		t.Fatal("accepted unknown media")
	}
	box := func(tag string, p []byte) []byte {
		out := make([]byte, len(p)+8)
		binary.BigEndian.PutUint32(out, uint32(len(out)))
		copy(out[4:8], tag)
		copy(out[8:], p)
		return out
	}
	movie := func(seconds uint32, width uint16, codec string) []byte {
		header := make([]byte, 20)
		binary.BigEndian.PutUint32(header[12:], 1000)
		binary.BigEndian.PutUint32(header[16:], seconds*1000)
		entry := make([]byte, 86)
		entry = append(entry, box("avcC", []byte{1, 100, 0, 40, 255, 225, 0, 4, 103, 100, 0, 40, 1, 0, 1, 104})...)
		binary.BigEndian.PutUint32(entry, uint32(len(entry)))
		copy(entry[4:8], codec)
		binary.BigEndian.PutUint16(entry[32:], width)
		binary.BigEndian.PutUint16(entry[34:], 1080)
		stsd := make([]byte, 8)
		binary.BigEndian.PutUint32(stsd[4:], 1)
		stsd = append(stsd, entry...)
		handler := make([]byte, 24)
		copy(handler[8:12], "vide")
		media := append(box("hdlr", handler), box("minf", box("stbl", box("stsd", stsd)))...)
		return box("moov", append(box("mvhd", header), box("trak", box("mdia", media))...))
	}
	if err = validateFallbackMP4(movie(30, 1920, "avc1")); err != nil {
		t.Fatal(err)
	}
	for _, handler := range []string{"clcp", "text", "meta", "subt"} {
		data := bytes.Replace(movie(10, 1920, "avc1"), []byte("vide"), []byte(handler), 1)
		if validateFallbackMP4(data) == nil {
			t.Fatalf("accepted unsupported %s handler claiming AVC", handler)
		}
	}
	for _, data := range [][]byte{movie(31, 1920, "avc1"), movie(0, 1920, "avc1"), movie(10, 3840, "avc1"), movie(10, 1920, "hvc1"), movie(10, 1920, "avc1")[:50], {0, 0, 0, 1, 'f', 'r', 'e', 'e', 255, 255, 255, 255, 255, 255, 255, 255}} {
		if validateFallbackMP4(data) == nil {
			t.Fatal("accepted unsupported or malformed MP4")
		}
	}
}
func TestFallbackGenerationAndActiveIsolation(t *testing.T) {
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
	account, err := s.CreateAccount(ctx, "fallback owner")
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.CreateStream(ctx, account, "fallback source", "fallback-test", []byte("hash"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO account_sources(account_id,stream_id) VALUES(?1,?2)`, account, source.ID); err != nil {
		t.Fatal(err)
	}
	cookieToken := func(owner string) string {
		digest := sha256.Sum256([]byte(owner))
		return base64.RawURLEncoding.EncodeToString(digest[:])
	}
	seedCookie := func(owner string) {
		t.Helper()
		var uid string
		if err = s.Pool.QueryRow(ctx, `INSERT INTO users(account_id,email,password_hash,password_salt) VALUES(?1,?2,?3,?4) RETURNING id`, owner, owner+"@example.invalid", []byte{1}, []byte{1}).Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Pool.Exec(ctx, `INSERT INTO user_sessions(user_id,token_hash,expires_at) VALUES(?1,?2,strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'))`, uid, sessionHash(cookieToken(owner))); err != nil {
			t.Fatal(err)
		}
		if _, e := s.Pool.Exec(ctx, `INSERT INTO owner_mfa(user_id,secret) VALUES(?1,'fixture')`, uid); e != nil {
			t.Fatal(e)
		}
		if _, e := s.Pool.Exec(ctx, `INSERT OR IGNORE INTO installation(singleton,owner_user) VALUES(1,?1)`, uid); e != nil {
			t.Fatal(e)
		}

	}
	seedCookie(account)
	a := &API{Store: s}
	handler := a.Handler()
	var asset bytes.Buffer
	png.Encode(&asset, image.NewRGBA(image.Rect(0, 0, 16, 16)))
	call := func(method, query string, owner string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, "/v1/me/source/fallback"+query, bytes.NewReader(asset.Bytes()))
		r.Header.Set("Content-Type", "image/png")
		if method != "PUT" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("X-Streamtool", "1")
		r.Header.Set("Cookie", "streamtool_session="+cookieToken(owner))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, query, w.Code, w.Body.String())
		}
	}
	call("PUT", "?generation=0", account, 204)
	call("PUT", "?generation=0", account, 409)
	call("DELETE", "?generation=1", account, 204)
	call("PUT", "?generation=0", account, 409)
	call("PUT", "?generation=2", account, 204)
	call("DELETE", "?generation=1", account, 409)
	other, err := s.CreateAccount(ctx, "other fallback owner")
	if err != nil {
		t.Fatal(err)
	}
	seedCookie(other)
	call("PUT", "?generation=3", other, 401)
	if _, err = s.Pool.Exec(ctx, `INSERT INTO ingest_connections(edge_id,connection_id,stream_id,protocol,status,connected_at,last_seen_at) VALUES('fallback-edge','fallback-source',?1,'srt','CONNECTED',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, source.ID); err != nil {
		t.Fatal(err)
	}
	call("PUT", "?generation=3", account, 409)
	call("DELETE", "?generation=3", account, 409)
	var generation int64
	if err = s.Pool.QueryRow(ctx, `SELECT generation FROM source_fallback_assets WHERE source_id=?1`, source.ID).Scan(&generation); err != nil || generation != 3 {
		t.Fatal("active edit changed generation", generation, err)
	}
}

func TestFallbackAVCProfiles(t *testing.T) {
	child := []byte{0, 0, 0, 24, 'a', 'v', 'c', 'C', 1, 100, 0, 40, 255, 225, 0, 4, 103, 100, 0, 40, 1, 0, 1, 104}
	for _, profile := range []byte{66, 77, 100, 110, 122, 244} {
		child[9], child[17] = profile, profile
		if supportedAVCConfiguration(child) != (profile == 66 || profile == 77 || profile == 100) {
			t.Fatalf("profile %d", profile)
		}
	}
	child[9], child[17] = 100, 110
	if supportedAVCConfiguration(child) || supportedAVCConfiguration(child[:12]) || supportedAVCConfiguration(nil) {
		t.Fatal("accepted invalid decoder configuration")
	}
}
