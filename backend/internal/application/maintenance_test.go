package application

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestMaintenanceBlocksOwnerAndEdgeMutationsWithoutTouchingStorage(t *testing.T) {
	directory := t.TempDir()
	_ = os.WriteFile(filepath.Join(directory, "admission.lock"), nil, 0644)
	_ = os.WriteFile(filepath.Join(directory, "active.json"), []byte(`{"job":"owned"}`), 0644)
	api := &API{MaintenanceDirectory: directory}
	for _, path := range []string{"/v1/auth/login", "/v1/auth/setup", "/v1/auth/setup/confirm", "/v1/auth/totp/replace", "/v1/auth/logout", "/v1/me/source", "/v1/edge/observations"} {
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if response.Code != 503 {
			t.Fatalf("%s admitted during maintenance: %d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/healthz", nil))
	if response.Code != 200 {
		t.Fatal("nonmutating health blocked")
	}
	// Edge peer trust still precedes maintenance; configure a trusted local peer.
	api.EdgePeers, _ = ParsePrefixes("192.0.2.0/24")
	request := httptest.NewRequest("POST", "/v1/edge/auth", strings.NewReader(`{}`))
	request.RemoteAddr = "192.0.2.10:1234"
	response = httptest.NewRecorder()
	api.EdgeHandler().ServeHTTP(response, request)
	if response.Code != 503 {
		t.Fatal("publisher auth admitted during maintenance")
	}
}

func TestMaintenanceAllowsOnlyAuthenticatedControllerCapability(t *testing.T) {
	directory := t.TempDir()
	lock, err := os.OpenFile(filepath.Join(directory, "admission.lock"), os.O_CREATE|os.O_RDONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	api := &API{MaintenanceDirectory: directory, EdgeToken: "fixture-controller-secret"}
	api.EdgePeers, _ = ParsePrefixes("192.0.2.10/32")
	for _, test := range []struct {
		action, password, peer string
		status                 int
	}{
		{"api", "fixture-controller-secret", "192.0.2.10:1234", 204},
		{"api", "wrong", "192.0.2.10:1234", 503},
		{"publish", "fixture-controller-secret", "192.0.2.10:1234", 503},
		{"read", "fixture-controller-secret", "192.0.2.10:1234", 503},
		{"api", "fixture-controller-secret", "192.0.2.11:1234", 403},
	} {
		request := httptest.NewRequest("POST", "/v1/edge/auth", strings.NewReader(fmt.Sprintf(`{"user":"controller","action":%q,"password":%q}`, test.action, test.password)))
		request.RemoteAddr = test.peer
		response := httptest.NewRecorder()
		api.EdgeHandler().ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s %s: got %d want %d", test.action, test.peer, response.Code, test.status)
		}
	}
}
