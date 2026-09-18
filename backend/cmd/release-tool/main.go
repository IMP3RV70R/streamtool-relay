// release-tool is an offline host/release-infrastructure tool, not a public API.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"streamtool-relay/internal/release"
)

type trust struct {
	PublicKey      string `json:"public_key"`
	Channel        string `json:"channel"`
	ArtifactOrigin string `json:"artifact_origin"`
}

func bounded(name string, limit int64) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("input exceeds size limit")
	}
	return data, nil
}
func atomic(name string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(name), ".release-state-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), name); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func run(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("use verify or sign")
	}
	flags := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
	manifestPath := flags.String("manifest", "", "exact manifest JSON bytes")
	signaturePath := flags.String("signature", "", "detached base64 Ed25519 signature")
	if arguments[0] == "sign" {
		keyPath := flags.String("key", "", "private 0600 signing seed file (base64), kept outside repository/servers")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		info, err := os.Lstat(*keyPath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("private signing key must be a private regular file")
		}
		encoded, err := bounded(*keyPath, 128)
		if err != nil {
			return err
		}
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return errors.New("invalid signing seed")
		}
		data, err := bounded(*manifestPath, release.MaxManifest)
		if err != nil {
			return err
		}
		var manifest release.Manifest
		if err := release.Decode(data, &manifest); err != nil {
			return err
		}
		key := ed25519.NewKeyFromSeed(seed)
		signature := ed25519.Sign(key, data)
		file, err := os.OpenFile(*signaturePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err = file.WriteString(base64.StdEncoding.EncodeToString(signature) + "\n"); err != nil {
			return err
		}
		return file.Sync()
	}
	if arguments[0] != "verify" {
		return errors.New("unknown command")
	}
	trustPath := flags.String("trust", "", "independently provisioned trust JSON")
	statePath := flags.String("state", "", "durable anti-rollback watermark outside application backups")
	metadataOnly := flags.Bool("metadata-only", false, "verify signed metadata without staging")
	bundlePath := flags.String("bundle", "", "local downloaded archive")
	destination := flags.String("destination", "", "new directory in a private staging root")
	architecture := flags.String("architecture", "", "amd64 or arm64")
	sequence := flags.Uint64("installed-sequence", 0, "currently installed signed release sequence")
	schema := flags.Int("installed-schema", 0, "currently installed SQLite schema")
	initial := flags.Bool("initial", false, "verify first installation")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if *trustPath == "" || *statePath == "" || (!*metadataOnly && (*bundlePath == "" || *destination == "")) || *manifestPath == "" || *signaturePath == "" || (*architecture != "amd64" && *architecture != "arm64") {
		return errors.New("all verification paths and architecture are required")
	}
	// Root-owned private parent directories are part of the host trust boundary.
	directories := []string{filepath.Dir(*statePath)}
	if !*metadataOnly {
		directories = append(directories, filepath.Dir(*destination))
	}
	for _, directory := range directories {
		info, err := os.Lstat(directory)
		if err != nil {
			return err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ok || int(owner.Uid) != os.Geteuid() {
			return errors.New("state/staging parent must be a private directory")
		}
	}
	trustInfo, err := os.Lstat(*trustPath)
	if err != nil {
		return err
	}
	trustOwner, ok := trustInfo.Sys().(*syscall.Stat_t)
	if !trustInfo.Mode().IsRegular() || trustInfo.Mode().Perm()&0022 != 0 || !ok || int(trustOwner.Uid) != os.Geteuid() {
		return errors.New("trust configuration must be owned by the verifier and not writable by others")
	}
	// Concurrent retries cannot race watermark acceptance. Never replace this lock.
	fd, err := syscall.Open(*statePath+".lock", syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("release verification already running")
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	rawTrust, err := bounded(*trustPath, 4096)
	if err != nil {
		return err
	}
	var configuration trust
	if err := release.Decode(rawTrust, &configuration); err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(configuration.PublicKey)
	if err != nil {
		return errors.New("invalid pinned public key")
	}
	data, err := bounded(*manifestPath, release.MaxManifest)
	if err != nil {
		return err
	}
	encoded, err := bounded(*signaturePath, 256)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return errors.New("invalid signature encoding")
	}
	var previous release.Watermark
	old, err := bounded(*statePath, 4096)
	if err == nil {
		if err := release.Decode(old, &previous); err != nil {
			return errors.New("corrupt release watermark")
		}
		if previous.Sequence == 0 || len(previous.Digest) != 64 {
			return errors.New("corrupt release watermark")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	manifest, artifact, watermark, err := release.Verify(data, signature, release.Policy{PublicKey: key, Channel: configuration.Channel, ArtifactOrigin: configuration.ArtifactOrigin, Architecture: *architecture, InstalledSequence: *sequence, InstalledSchema: *schema, Initial: *initial}, previous, time.Now().UTC())
	if err != nil {
		return err
	}
	// Accept signed metadata before staging. An interrupted download must not permit
	// falling back to an older feed; retrying identical accepted metadata is allowed.
	stamp, err := json.Marshal(watermark)
	if err != nil {
		return err
	}
	if err := atomic(*statePath, stamp); err != nil {
		return err
	}
	if *metadataOnly {
		fmt.Println("Verified signed release metadata. No installation performed.")
		return nil
	}
	if err := release.Stage(*bundlePath, *destination, manifest, artifact); err != nil {
		return err
	}
	fmt.Printf("Verified release %s (%s), sequence %d. No installation performed.\n", manifest.Version, artifact.Architecture, manifest.Sequence)
	return nil
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Release rejected:", err)
		os.Exit(1)
	}
}
