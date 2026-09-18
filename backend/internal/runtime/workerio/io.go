// Package workerio implements the Agent's private Docker-exec file transport.
// It accepts no paths from command arguments and never logs secret contents.
package workerio

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"streamtool-relay/internal/domain"
	workerruntime "streamtool-relay/internal/runtime"
)

func Execute(mode string, input io.Reader, output io.Writer) error {
	return executeAt("/run/secrets", mode, input, output)
}

func executeAt(root, mode string, input io.Reader, output io.Writer) error {
	switch mode {
	case "receive":
		return receive(root, input)
	case "activate":
		if _, err := resources(root, "resources.json"); err != nil {
			return err
		}
		if _, err := readRegular(filepath.Join(root, "control_token"), 1<<20); err != nil {
			return err
		}
		file, err := os.OpenFile(filepath.Join(root, "ready"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("activation marker unavailable")
		}
		return file.Close()
	case "read-resources":
		data, err := resources(root, "resources.json")
		if err != nil {
			return err
		}
		_, err = output.Write(data)
		return err
	case "read-token":
		data, err := readRegular(filepath.Join(root, "control_token"), 1<<20)
		if err != nil {
			return err
		}
		_, err = output.Write(data)
		return err
	case "commit-resources":
		if _, err := resources(root, "resources.next"); err != nil {
			return err
		}
		return os.Rename(filepath.Join(root, "resources.next"), filepath.Join(root, "resources.json"))
	case "read-stat", "read-memory":
		path := "/proc/1/stat"
		if mode == "read-memory" {
			path = "/sys/fs/cgroup/memory.current"
		}
		data, err := readRegular(path, 8192)
		if err != nil {
			return err
		}
		_, err = output.Write(data)
		return err
	default:
		return errors.New("unknown private transport command")
	}
}

func readRegular(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("private file unavailable")
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil, errors.New("private file is not regular")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) == 0 || int64(len(data)) > limit {
		return nil, errors.New("private file size invalid")
	}
	return data, nil
}

func resources(root, name string) ([]byte, error) {
	data, err := readRegular(filepath.Join(root, name), 4096)
	if err != nil {
		return nil, err
	}
	var resource domain.ResourceVector
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&resource) != nil || decoder.Decode(new(any)) != io.EOF || resource.Slots <= 0 || resource.CPUMillis < 0 || resource.MemoryBytes < 0 || resource.IngressBPS < 0 || resource.EgressBPS < 0 {
		return nil, errors.New("private resource snapshot invalid")
	}
	return data, nil
}

func receive(root string, input io.Reader) error {
	stat, err := os.Lstat(root)
	if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return errors.New("private directory unavailable")
	}
	stage, err := os.MkdirTemp(root, ".handoff-")
	if err != nil {
		return errors.New("private staging unavailable")
	}
	defer os.RemoveAll(stage)
	reader := tar.NewReader(io.LimitReader(input, 56<<20))
	seen := make(map[string]bool)
	var names []string
	var total int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("private archive invalid")
		}
		name := header.Name
		limit := int64(1 << 20)
		if name == "fallback_asset" {
			limit = 50 << 20
		}
		total += header.Size
		// Start permits 54 MiB of caller files and adds its resource snapshot.
		if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || len(header.PAXRecords) != 0 || header.Linkname != "" || name == "ready" || (!workerruntime.ValidID(name) && name != "resources.json" && name != "resources.next" && name != "destinations.json") || seen[name] || len(names) >= 32 || header.Size < 0 || header.Size > limit || total > (54<<20)+4096 {
			return errors.New("private archive entry rejected")
		}
		file, err := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("private staging failed")
		}
		_, copyErr := io.CopyN(file, reader, header.Size)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return errors.New("private archive truncated")
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		return errors.New("private archive empty")
	}
	for _, name := range names {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join(root, name)); err != nil {
			return errors.New("private handoff failed")
		}
	}
	return nil
}
