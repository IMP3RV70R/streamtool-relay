package release

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Stage creates a new private directory only after checking the signed outer hash.
// The caller owns the parent directory; never use a user-writable host staging root.
// No scripts, binaries, migrations or Docker operations are executed here.
func Stage(bundle string, destination string, manifest Manifest, artifact Artifact) (err error) {
	if artifact.Size <= 0 || artifact.Size > MaxArchive || !digestPattern.MatchString(artifact.SHA256) {
		return errors.New("invalid artifact bounds")
	}
	file, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != artifact.Size {
		return errors.New("artifact size or type mismatch")
	}
	// Snapshot untrusted input once: hashing then rereading the original would
	// permit another process to swap bytes between verification and extraction.
	private, err := os.CreateTemp(filepath.Dir(destination), ".release-input-")
	if err != nil {
		return err
	}
	defer os.Remove(private.Name())
	defer private.Close()
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(hash, private), io.LimitReader(file, artifact.Size+1))
	if err != nil {
		return err
	}
	if count != artifact.Size || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return errors.New("artifact digest mismatch")
	}
	if err = private.Sync(); err != nil {
		return err
	}
	if _, err = private.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destination)
		}
	}()
	compressed, err := gzip.NewReader(private)
	if err != nil {
		return err
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	entries := map[string]bool{}
	files := map[string]string{}
	var total int64
	rootSeen := false
	for {
		header, readErr := reader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
		name := strings.TrimPrefix(header.Name, "./")
		if (header.Name == "." || header.Name == "./") && header.Typeflag == tar.TypeDir && !rootSeen {
			rootSeen = true
			continue
		}
		name = strings.TrimSuffix(name, "/")
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.ContainsAny(name, "\x00\r\n\t") || name == ".." || strings.HasPrefix(name, "../") || entries[name] || len(entries) >= 4096 {
			return errors.New("unsafe or duplicate archive entry")
		}
		entries[name] = true
		target := filepath.Join(destination, filepath.FromSlash(name))
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > MaxArchive || total+header.Size > 16<<30 {
			return errors.New("unsupported archive entry or extraction limit")
		}
		total += header.Size
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		digest := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(output, digest), reader)
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written != header.Size {
			return io.ErrUnexpectedEOF
		}
		files[name] = hex.EncodeToString(digest.Sum(nil))
	}
	// Force gzip checksum/length validation; reject non-padding data after tar EOF.
	// Python tarfile pads records to 20 blocks (10 KiB).
	padding := make([]byte, 20*512)
	n, readErr := io.ReadFull(compressed, padding)
	if readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return errors.New("invalid archive trailer")
	}
	for _, b := range padding[:n] {
		if b != 0 {
			return errors.New("unexpected archive trailer")
		}
	}
	if len(files) == 0 {
		return errors.New("empty release bundle")
	}
	checksums, err := os.Open(filepath.Join(destination, "SHA256SUMS"))
	if err != nil {
		return errors.New("bundle checksum list missing")
	}
	defer checksums.Close()
	checksumInfo, err := checksums.Stat()
	if err != nil || checksumInfo.Size() > 1<<20 {
		return errors.New("oversized checksum list")
	}
	scanner := bufio.NewScanner(io.LimitReader(checksums, 1<<20))
	matched := map[string]bool{}
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 67 || line[64:66] != "  " {
			return errors.New("invalid bundle checksum record")
		}
		name := line[66:]
		if name == "SHA256SUMS" || matched[name] || files[name] == "" || files[name] != line[:64] {
			return errors.New("bundle checksum mismatch")
		}
		matched[name] = true
	}
	if scanner.Err() != nil || len(matched) != len(files)-1 {
		return errors.New("incomplete bundle checksums")
	}
	required := []string{"VERSION", "ARCHITECTURE", "api-image", "worker-image", "proxy-image", "edge-image", "images.tar", "node-agent", "updater.py", "updater_bridge.py", "updater_engine.py", "updater_host.py", "maintenance.py", "release-tool", "streamtool-updater.service", "streamtool-updater-space.service", "streamtool-updater-bridge.service", "bootstrap.py", "deploy.py", "backup.py", "renew.py", "install.sh", "update.sh", "compose.yml", "Caddyfile", "mediamtx.yml", "streamtool-agent.service", "streamtool-agent.sudoers", "streamtool-worker-policy", "streamtool-certificates.service", "streamtool-certificates.timer", "web/index.html", "web/app.js", "web/style.css"}
	for _, name := range required {
		if files[name] == "" {
			return fmt.Errorf("required bundle file missing: %s", name)
		}
	}
	readSmall := func(name string) (string, error) {
		file, err := os.Open(filepath.Join(destination, name))
		if err != nil {
			return "", err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 256))
		if err != nil || len(data) >= 256 {
			return "", errors.New("oversized bundle metadata")
		}
		return strings.TrimSpace(string(data)), nil
	}
	version, err := readSmall("VERSION")
	if err != nil || version != manifest.Version {
		return errors.New("bundle version mismatch")
	}
	arch, err := readSmall("ARCHITECTURE")
	if err != nil || arch != artifact.Architecture {
		return errors.New("bundle architecture mismatch")
	}
	for _, name := range []string{"api-image", "worker-image", "proxy-image", "edge-image"} {
		image, err := readSmall(name)
		if err != nil || !strings.HasPrefix(image, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(image, "sha256:")) {
			return errors.New("invalid immutable image ID")
		}
	}
	directory, err := os.Open(destination)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
