package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (Manifest, Policy, ed25519.PrivateKey, time.Time) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	manifest := Manifest{TargetSchema: 6, Sequence: 9, Version: "0.1.0-rc9", Channel: "stable", Created: now.Add(-time.Hour), Expires: now.Add(time.Hour), Protocol: Protocol, MinimumSequence: 1, MinimumSchema: 5, MaximumSchema: 6, Artifacts: []Artifact{{"arm64", "https://releases.example.invalid/relay.tar.gz", 1024, strings.Repeat("a", 64)}}}
	return manifest, Policy{PublicKey: public, Channel: "stable", ArtifactOrigin: "https://releases.example.invalid", Architecture: "arm64", InstalledSequence: 8, InstalledSchema: 6}, private, now
}
func TestSignedMetadataAndWatermark(t *testing.T) {
	manifest, policy, key, now := fixture(t)
	data, _ := json.Marshal(manifest)
	signature := ed25519.Sign(key, data)
	_, artifact, stamp, err := Verify(data, signature, policy, Watermark{}, now)
	if err != nil || artifact.Architecture != "arm64" || stamp.Sequence != 9 {
		t.Fatal("valid release rejected", err)
	}
	if _, _, _, err := Verify(data, signature, policy, stamp, now); err != nil {
		t.Fatal("identical retry rejected", err)
	}
	changed := append([]byte(nil), data...)
	changed[3] ^= 1
	if _, _, _, err := Verify(changed, signature, policy, Watermark{}, now); err == nil {
		t.Fatal("tampering accepted")
	}
	if _, _, _, err := Verify(data, signature, policy, Watermark{10, strings.Repeat("b", 64)}, now); err == nil {
		t.Fatal("rollback accepted")
	}
	if _, _, _, err := Verify(data, signature, policy, Watermark{9, strings.Repeat("b", 64)}, now); err == nil {
		t.Fatal("equivocation accepted")
	}
	_, different, _ := ed25519.GenerateKey(rand.Reader)
	if _, _, _, err := Verify(data, ed25519.Sign(different, data), policy, Watermark{}, now); err == nil {
		t.Fatal("foreign signature accepted")
	}
	duplicate := bytes.Replace(data, []byte(`"sequence":9`), []byte(`"sequence":8,"sequence":9`), 1)
	if _, _, _, err := Verify(duplicate, ed25519.Sign(key, duplicate), policy, Watermark{}, now); err == nil {
		t.Fatal("duplicate keys accepted")
	}
}
func TestRejectIncompatibleManifests(t *testing.T) {
	cases := map[string]func(*Manifest, *Policy){
		"expired":            func(m *Manifest, _ *Policy) { m.Expires = m.Created },
		"future":             func(m *Manifest, _ *Policy) { m.Created = m.Expires },
		"long validity":      func(m *Manifest, _ *Policy) { m.Expires = m.Created.Add(32 * 24 * time.Hour) },
		"schema downgrade":   func(m *Manifest, _ *Policy) { m.TargetSchema = 5 },
		"protocol":           func(m *Manifest, _ *Policy) { m.Protocol++ },
		"schema":             func(_ *Manifest, p *Policy) { p.InstalledSchema = 4 },
		"installed sequence": func(_ *Manifest, p *Policy) { p.InstalledSequence = 0 },
		"no new version":     func(_ *Manifest, p *Policy) { p.InstalledSequence = 9 },
		"wrong architecture": func(_ *Manifest, p *Policy) { p.Architecture = "amd64" },
		"foreign origin":     func(m *Manifest, _ *Policy) { m.Artifacts[0].URL = "https://evil.invalid/a" },
		"credentials":        func(m *Manifest, _ *Policy) { m.Artifacts[0].URL = "https://pw@releases.example.invalid/a" },
		"query":              func(m *Manifest, _ *Policy) { m.Artifacts[0].URL += "?token=secret" },
		"http":               func(m *Manifest, _ *Policy) { m.Artifacts[0].URL = "http://releases.example.invalid/a" },
		"oversize":           func(m *Manifest, _ *Policy) { m.Artifacts[0].Size = MaxArchive + 1 },
		"duplicate arch":     func(m *Manifest, _ *Policy) { m.Artifacts = append(m.Artifacts, m.Artifacts[0]) },
		"wrong channel":      func(m *Manifest, _ *Policy) { m.Channel = "beta" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m, p, key, now := fixture(t)
			mutate(&m, &p)
			data, _ := json.Marshal(m)
			if _, _, _, err := Verify(data, ed25519.Sign(key, data), p, Watermark{}, now); err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
}

type entry struct {
	name string
	body []byte
	kind byte
}

func archive(t *testing.T, entries []entry) []byte {
	t.Helper()
	var result bytes.Buffer
	zipped := gzip.NewWriter(&result)
	writer := tar.NewWriter(zipped)
	for _, item := range entries {
		kind := item.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		size := int64(len(item.body))
		if kind != tar.TypeReg {
			size = 0
		}
		if err := writer.WriteHeader(&tar.Header{Name: item.name, Mode: 0777, Size: size, Typeflag: kind, Linkname: "../../outside"}); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := writer.Write(item.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}
func completeEntries() []entry {
	names := []string{"VERSION", "ARCHITECTURE", "api-image", "worker-image", "proxy-image", "edge-image", "images.tar", "node-agent", "updater.py", "updater_bridge.py", "updater_engine.py", "updater_host.py", "maintenance.py", "release-tool", "streamtool-updater.service", "streamtool-updater-space.service", "streamtool-updater-bridge.service", "bootstrap.py", "deploy.py", "backup.py", "renew.py", "install.sh", "update.sh", "compose.yml", "Caddyfile", "mediamtx.yml", "streamtool-agent.service", "streamtool-agent.sudoers", "streamtool-worker-policy", "streamtool-certificates.service", "streamtool-certificates.timer", "web/index.html", "web/app.js", "web/style.css"}
	var entries []entry
	var sums strings.Builder
	for _, name := range names {
		body := []byte("test fixture: no execution")
		if name == "VERSION" {
			body = []byte("0.1.0-rc9\n")
		}
		if name == "ARCHITECTURE" {
			body = []byte("arm64\n")
		}
		if strings.HasSuffix(name, "-image") {
			body = []byte("sha256:" + strings.Repeat("a", 64) + "\n")
		}
		digest := sha256.Sum256(body)
		sums.WriteString(hex.EncodeToString(digest[:]) + "  " + name + "\n")
		entries = append(entries, entry{name, body, 0})
	}
	return append(entries, entry{"SHA256SUMS", []byte(sums.String()), 0})
}
func stageFixture(t *testing.T, data []byte) (string, error) {
	t.Helper()
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle.tar.gz")
	if err := os.WriteFile(bundle, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	manifest, _, _, _ := fixture(t)
	artifact := manifest.Artifacts[0]
	artifact.Size = int64(len(data))
	artifact.SHA256 = hex.EncodeToString(sum[:])
	destination := filepath.Join(root, "staged")
	return destination, Stage(bundle, destination, manifest, artifact)
}
func TestStageVerifiedBundle(t *testing.T) {
	destination, err := stageFixture(t, archive(t, completeEntries()))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("stage not private")
	}
	info, err = os.Stat(filepath.Join(destination, "install.sh"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("untrusted executable permissions preserved")
	}
}
func TestRejectUnsafeBundlesAndCleanPartialStage(t *testing.T) {
	cases := map[string][]entry{
		"traversal":     {{"../escape", []byte("bad"), 0}},
		"absolute":      {{"/escape", nil, 0}},
		"symlink":       {{"web", nil, tar.TypeSymlink}},
		"hardlink":      {{"web", nil, tar.TypeLink}},
		"device":        {{"device", nil, tar.TypeChar}},
		"duplicate":     {{"VERSION", []byte("a"), 0}, {"./VERSION", []byte("b"), 0}},
		"missing sums":  {{"VERSION", []byte("a"), 0}},
		"unlisted file": append(completeEntries(), entry{"extra", []byte("unlisted"), 0}),
	}
	wrong := completeEntries()
	wrong[0].body = []byte("wrong version")
	cases["inner checksum"] = wrong
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			destination, err := stageFixture(t, archive(t, entries))
			if err == nil {
				t.Fatal("unsafe bundle accepted")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("partial stage retained")
			}
		})
	}
	data := archive(t, completeEntries())
	data[len(data)-1] ^= 1
	destination, err := stageFixture(t, data)
	if err == nil {
		t.Fatal("invalid gzip checksum accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("partial corrupt archive retained")
	}
}
func TestOuterDigestRejectsWithoutTouchingDestination(t *testing.T) {
	m, _, _, _ := fixture(t)
	root := t.TempDir()
	bundle := filepath.Join(root, "a")
	data := []byte("untrusted")
	_ = os.WriteFile(bundle, data, 0600)
	artifact := m.Artifacts[0]
	artifact.Size = int64(len(data))
	destination := filepath.Join(root, "stage")
	if err := Stage(bundle, destination, m, artifact); err == nil {
		t.Fatal("incorrect outer digest accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("destination created before provenance")
	}
}

func TestTarRecordPaddingIsBoundedAndZeroOnly(t *testing.T) {
	reader, err := gzip.NewReader(bytes.NewReader(archive(t, completeEntries())))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		padding []byte
		valid   bool
	}{
		{"python record padding", make([]byte, 8192), true},
		{"oversized trailer", make([]byte, 20*512), false},
		{"nonzero trailer", []byte{1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writer := gzip.NewWriter(&output)
			if _, err := writer.Write(append(append([]byte{}, raw...), test.padding...)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			_, err := stageFixture(t, output.Bytes())
			if (err == nil) != test.valid {
				t.Fatalf("unexpected padding result: %v", err)
			}
		})
	}
}
