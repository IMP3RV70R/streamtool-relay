// Package release verifies distribution provenance without executing bundle code.
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"time"
)

const Protocol = 1
const MaxManifest = 64 * 1024
const MaxArchive int64 = 8 << 30

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Artifact struct {
	Architecture string `json:"architecture"`
	URL          string `json:"url"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
}
type Manifest struct {
	TargetSchema    int        `json:"target_schema"`
	Sequence        uint64     `json:"sequence"`
	Version         string     `json:"version"`
	Channel         string     `json:"channel"`
	Created         time.Time  `json:"created"`
	Expires         time.Time  `json:"expires"`
	Protocol        int        `json:"protocol"`
	MinimumSequence uint64     `json:"minimum_sequence"`
	MinimumSchema   int        `json:"minimum_schema"`
	MaximumSchema   int        `json:"maximum_schema"`
	Notes           string     `json:"notes"`
	Artifacts       []Artifact `json:"artifacts"`
}
type Policy struct {
	PublicKey         ed25519.PublicKey
	Channel           string
	ArtifactOrigin    string
	Architecture      string
	InstalledSequence uint64
	InstalledSchema   int
	Initial           bool
}
type Watermark struct {
	Sequence uint64 `json:"sequence"`
	Digest   string `json:"digest"`
}

func Decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON")
	}
	// Reject duplicate object keys rather than accepting an ambiguous signed contract.
	decoder = json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name := key.(string)
				if keys[name] {
					return errors.New("duplicate JSON key")
				}
				keys[name] = true
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	return walk()
}
func Verify(data, signature []byte, policy Policy, previous Watermark, now time.Time) (Manifest, Artifact, Watermark, error) {
	var manifest Manifest
	fail := func(reason string) (Manifest, Artifact, Watermark, error) {
		return Manifest{}, Artifact{}, Watermark{}, errors.New(reason)
	}
	if len(data) == 0 || len(data) > MaxManifest || len(policy.PublicKey) != ed25519.PublicKeySize || !ed25519.Verify(policy.PublicKey, data, signature) {
		return fail("invalid release signature")
	}
	if err := Decode(data, &manifest); err != nil {
		return fail("invalid release manifest")
	}
	if manifest.TargetSchema < 1 || manifest.Sequence == 0 || !versionPattern.MatchString(manifest.Version) || manifest.Channel != policy.Channel || manifest.Channel == "" || manifest.Protocol != Protocol || len(manifest.Notes) > 8192 {
		return fail("incompatible release metadata")
	}
	if manifest.Created.IsZero() || manifest.Created.After(now.Add(5*time.Minute)) || !manifest.Expires.After(now) || !manifest.Expires.After(manifest.Created) || manifest.Expires.Sub(manifest.Created) > 31*24*time.Hour {
		return fail("expired or invalid release time")
	}
	if manifest.MinimumSchema < 0 || manifest.MaximumSchema < manifest.MinimumSchema || manifest.MinimumSequence >= manifest.Sequence {
		return fail("invalid upgrade range")
	}
	if !policy.Initial && (manifest.TargetSchema < policy.InstalledSchema || policy.InstalledSequence < manifest.MinimumSequence || policy.InstalledSchema < manifest.MinimumSchema || policy.InstalledSchema > manifest.MaximumSchema) {
		return fail("unsupported installed release or schema")
	}
	sum := sha256.Sum256(data)
	stamp := Watermark{manifest.Sequence, hex.EncodeToString(sum[:])}
	if previous.Sequence > stamp.Sequence || (previous.Sequence == stamp.Sequence && previous.Digest != stamp.Digest) || (!policy.Initial && manifest.Sequence <= policy.InstalledSequence) {
		return fail("release rollback or equivocation")
	}
	origin, err := url.Parse(policy.ArtifactOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return fail("invalid trusted artifact origin")
	}
	seen := map[string]bool{}
	var selected Artifact
	if len(manifest.Artifacts) < 1 || len(manifest.Artifacts) > 2 {
		return fail("invalid artifact count")
	}
	for _, artifact := range manifest.Artifacts {
		if (artifact.Architecture != "amd64" && artifact.Architecture != "arm64") || seen[artifact.Architecture] || artifact.Size <= 0 || artifact.Size > MaxArchive || !digestPattern.MatchString(artifact.SHA256) {
			return fail("invalid artifact descriptor")
		}
		address, err := url.Parse(artifact.URL)
		if err != nil || address.Scheme != "https" || address.Host != origin.Host || address.User != nil || address.Fragment != "" || address.RawQuery != "" || address.Path == "" || address.Opaque != "" {
			return fail("untrusted artifact address")
		}
		seen[artifact.Architecture] = true
		if artifact.Architecture == policy.Architecture {
			selected = artifact
		}
	}
	if selected.Architecture == "" {
		return fail(fmt.Sprintf("no artifact for %s", policy.Architecture))
	}
	return manifest, selected, stamp, nil
}
