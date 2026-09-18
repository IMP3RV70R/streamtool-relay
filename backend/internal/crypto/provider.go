package crypto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Manifest contains version references, not production key material. Replacing
// it atomically switches writes; old references remain available for decryption.
type KeyReference struct {
	File      string `json:"file,omitempty"`
	SecretID  string `json:"secret_id,omitempty"`
	VersionID string `json:"version_id,omitempty"`
	Entry     string `json:"entry,omitempty"`
}
type KeyManifest struct {
	Current string                  `json:"current"`
	Keys    map[string]KeyReference `json:"keys"`
}
type VersionedProvider struct {
	Path, Mode              string
	mu                      sync.Mutex
	cache                   map[KeyReference][]byte
	bindings                map[string]KeyReference
	client                  *http.Client
	metadataURL, payloadURL string
}

func NewVersionedProvider(path, mode string) (*VersionedProvider, error) {
	if path == "" || (mode != "file" && mode != "lockbox") {
		return nil, errors.New("key manifest and valid provider required")
	}
	p := &VersionedProvider{Path: path, Mode: mode, cache: map[KeyReference][]byte{}, bindings: map[string]KeyReference{}, client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, metadataURL: "http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token", payloadURL: "https://payload.lockbox.api.cloud.yandex.net/lockbox/v1/secrets/"}
	_, err := p.manifest()
	return p, err
}
func (p *VersionedProvider) manifest() (KeyManifest, error) {
	f, err := os.Open(p.Path)
	if err != nil {
		return KeyManifest{}, errors.New("key manifest unavailable")
	}
	defer f.Close()
	var m KeyManifest
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || len(m.Keys) < 1 || len(m.Keys) > 32 || m.Current == "" {
		return m, errors.New("invalid key manifest")
	}
	if _, ok := m.Keys[m.Current]; !ok {
		return m, errors.New("current key not in manifest")
	}
	for id, ref := range m.Keys {
		if id == "" || len(id) > 100 {
			return m, errors.New("invalid key ID")
		}
		if p.Mode == "file" {
			if ref.File == "" || ref.SecretID != "" || ref.VersionID != "" {
				return m, errors.New("invalid file reference")
			}
		} else if ref.File != "" || ref.SecretID == "" || ref.VersionID == "" || ref.Entry == "" {
			return m, errors.New("Lockbox references must pin a version and entry")
		}
	}
	return m, nil
}
func (p *VersionedProvider) CurrentKeyID(ctx context.Context) (string, error) {
	m, err := p.manifest()
	if err != nil {
		return "", err
	}
	if _, err = p.Key(ctx, m.Current); err != nil {
		return "", err
	}
	return m.Current, nil
}
func (p *VersionedProvider) Key(ctx context.Context, id string) ([]byte, error) {
	m, err := p.manifest()
	if err != nil {
		return nil, err
	}
	ref, ok := m.Keys[id]
	if !ok {
		return nil, errors.New("key version unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if prior, ok := p.bindings[id]; ok && prior != ref {
		return nil, errors.New("key ID cannot be rebound")
	}
	p.bindings[id] = ref
	// Bound the cache to the current manifest and preserve immutable ID bindings.
	for cached := range p.cache {
		found := false
		for _, v := range m.Keys {
			if v == cached {
				found = true
			}
		}
		if !found {
			delete(p.cache, cached)
		}
	}
	if key := p.cache[ref]; key != nil {
		return append([]byte(nil), key...), nil
	}
	var raw string
	if p.Mode == "file" {
		b, err := os.ReadFile(ref.File)
		if err != nil {
			return nil, errors.New("key file unavailable")
		}
		raw = string(b)
	} else {
		var token struct {
			Access string `json:"access_token"`
		}
		if err = p.get(ctx, p.metadataURL, "", &token); err != nil || token.Access == "" {
			return nil, errors.New("workload credentials unavailable")
		}
		var payload struct {
			Version string `json:"versionId"`
			Entries []struct {
				Key  string `json:"key"`
				Text string `json:"textValue"`
			} `json:"entries"`
		}
		endpoint := p.payloadURL + url.PathEscape(ref.SecretID) + "/payload?" + url.Values{"versionId": {ref.VersionID}}.Encode()
		if err = p.get(ctx, endpoint, token.Access, &payload); err != nil || payload.Version != ref.VersionID {
			return nil, errors.New("key payload unavailable or version mismatch")
		}
		for _, entry := range payload.Entries {
			if entry.Key == ref.Entry {
				raw = entry.Text
			}
		}
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid encryption key")
	}
	p.cache[ref] = append([]byte(nil), key...)
	return key, nil
}
func (p *VersionedProvider) get(ctx context.Context, endpoint, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return errors.New("key request failed")
	}
	if token == "" {
		req.Header.Set("Metadata-Flavor", "Google")
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return errors.New("key service unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out) != nil {
		return errors.New("invalid key service response")
	}
	return nil
}
