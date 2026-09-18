package workerio

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archive(t *testing.T, headers []*tar.Header, contents []string) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	for i, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, contents[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func fileHeader(name, content string) *tar.Header {
	return &tar.Header{Name: name, Size: int64(len(content)), Mode: 0777, Typeflag: tar.TypeReg}
}

func TestHandoffActivationAndAtomicResourceUpdate(t *testing.T) {
	root := t.TempDir()
	snapshot := `{"slots":1,"cpu_millis":1000}`
	data := archive(t, []*tar.Header{fileHeader("resources.json", snapshot), fileHeader("control_token", "private-token")}, []string{snapshot, "private-token"})
	if err := executeAt(root, "receive", bytes.NewReader(data), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"control_token", "resources.json"} {
		stat, err := os.Stat(filepath.Join(root, name))
		if err != nil || stat.Mode().Perm() != 0600 {
			t.Fatalf("private mode: %v %v", stat, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "ready")); !os.IsNotExist(err) {
		t.Fatal("receive activated incomplete worker")
	}
	if err := executeAt(root, "activate", nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	newSnapshot := `{"slots":1,"cpu_millis":1500}`
	data = archive(t, []*tar.Header{fileHeader("resources.next", newSnapshot)}, []string{newSnapshot})
	if err := executeAt(root, "receive", bytes.NewReader(data), io.Discard); err != nil {
		t.Fatal(err)
	}
	if old, _ := os.ReadFile(filepath.Join(root, "resources.json")); string(old) != snapshot {
		t.Fatal("snapshot changed before commit")
	}
	if err := executeAt(root, "commit-resources", nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := executeAt(root, "read-resources", nil, &output); err != nil || output.String() != newSnapshot {
		t.Fatalf("snapshot: %s %v", output.String(), err)
	}
}

func TestRejectedArchiveCannotEscapeOrActivate(t *testing.T) {
	for _, name := range []string{"../escape", "/escape", "folder/file", "..", "ready"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			data := archive(t, []*tar.Header{fileHeader(name, "secret")}, []string{"secret"})
			if err := executeAt(root, "receive", bytes.NewReader(data), io.Discard); err == nil {
				t.Fatal("unsafe entry accepted")
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 0 {
				t.Fatal("rejected handoff left files")
			}
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeDir} {
		root := t.TempDir()
		header := &tar.Header{Name: "control_token", Typeflag: kind, Linkname: "../outside"}
		data := archive(t, []*tar.Header{header}, []string{""})
		if err := executeAt(root, "receive", bytes.NewReader(data), io.Discard); err == nil {
			t.Fatal("link/directory accepted")
		}
	}
}

func TestDuplicateAndOversizedEntriesRejected(t *testing.T) {
	data := archive(t, []*tar.Header{fileHeader("source_token", "a"), fileHeader("source_token", "b")}, []string{"a", "b"})
	if err := executeAt(t.TempDir(), "receive", bytes.NewReader(data), io.Discard); err == nil {
		t.Fatal("duplicate accepted")
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "control_token", Size: 1<<20 + 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if err := executeAt(t.TempDir(), "receive", bytes.NewReader(buffer.Bytes()), io.Discard); err == nil {
		t.Fatal("oversized entry accepted")
	}
}

func TestTruncatedArchivePreservesExistingSecrets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "control_token")
	if err := os.WriteFile(path, []byte("old-token"), 0600); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(fileHeader("control_token", "new-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("new-token")); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: "source_token", Size: 10, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("short")); err != nil {
		t.Fatal(err)
	}
	if err := executeAt(root, "receive", bytes.NewReader(buffer.Bytes()), io.Discard); err == nil {
		t.Fatal("truncation accepted")
	}
	old, _ := os.ReadFile(path)
	if string(old) != "old-token" {
		t.Fatal("failed handoff replaced existing secret")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal("staging left behind")
	}
}

func TestInvalidSnapshotAndSymlinkTokenFailClosed(t *testing.T) {
	root := t.TempDir()
	old := `{"slots":1}`
	if err := os.WriteFile(filepath.Join(root, "resources.json"), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"slots":0}`, `{"slots":1,"cpu_millis":-1}`, `{"slots":1,"unknown":true}`, `{"slots":1} {}`} {
		if err := os.WriteFile(filepath.Join(root, "resources.next"), []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if err := executeAt(root, "commit-resources", nil, io.Discard); err == nil {
			t.Fatal("invalid snapshot committed")
		}
		actual, _ := os.ReadFile(filepath.Join(root, "resources.json"))
		if string(actual) != old {
			t.Fatal("invalid commit changed snapshot")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "control_token")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := executeAt(root, "read-token", nil, &output); err == nil || output.Len() != 0 {
		t.Fatal("symlink token disclosed")
	}
	if err := executeAt(root, "activate", nil, io.Discard); err == nil {
		t.Fatal("symlink token activated worker")
	}
	if err := executeAt(root, "../control_token", nil, &output); err == nil || strings.Contains(err.Error(), "outside-secret") {
		t.Fatal("unknown command accepted or disclosed secret")
	}
}
