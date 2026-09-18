package maintenance

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAdmissionAndExclusiveHostLock(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "admission.lock")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	leave, err := Enter(directory)
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if err := syscall.Flock(int(host.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("host maintenance raced active mutation")
	}
	leave()
	if err := syscall.Flock(int(host.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := Enter(directory); err == nil {
		t.Fatal("new mutation admitted during final idle check")
	}
	_ = os.WriteFile(filepath.Join(directory, "active.json"), []byte(`{"job":"owned"}`), 0644)
	_ = syscall.Flock(int(host.Fd()), syscall.LOCK_UN)
	if _, err := Enter(directory); err == nil {
		t.Fatal("persistent maintenance disappeared with host process lock")
	}
	if err := os.Remove(filepath.Join(directory, "active.json")); err != nil {
		t.Fatal(err)
	}
	leave, err = Enter(directory)
	if err != nil {
		t.Fatal(err)
	}
	leave()
}
func TestUnavailableGateFailsClosed(t *testing.T) {
	if _, err := Enter(t.TempDir()); err == nil {
		t.Fatal("missing host lock admitted mutation")
	}
}

// Optional cross-process Linux check driven by the real Python host fence.
func TestHostFenceInterop(t *testing.T) {
	directory := os.Getenv("STREAMTOOL_GATE_INTEROP_DIRECTORY")
	if directory == "" {
		t.Skip("optional host fence integration not supplied")
	}
	leave, err := Enter(directory)
	if os.Getenv("STREAMTOOL_GATE_EXPECT_ACTIVE") == "1" {
		if err == nil {
			leave()
			t.Fatal("Python host fence admitted Go mutation")
		}
		return
	}
	if err != nil {
		t.Fatal("released Python fence still blocked Go admission", err)
	}
	leave()
}
