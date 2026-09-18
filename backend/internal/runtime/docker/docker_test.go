package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"streamtool-relay/internal/domain"
	workerruntime "streamtool-relay/internal/runtime"
	"strings"
	"testing"
)

type inspectCLI struct {
	out string
	err error
}

type policyFailureCLI struct{ activated, stopped, running bool }

func (c *policyFailureCLI) Run(_ context.Context, args ...string) ([]byte, error) {
	if args[0] == "inspect" {
		if c.running {
			return []byte("container-id"), nil
		}
		return []byte("no such object"), errors.New("missing")
	}
	if args[0] == "run" {
		c.running = true
	}
	if args[0] == "stop" {
		c.stopped = true
	}
	if args[0] == "exec" {
		c.activated = true
	}
	return nil, nil
}

func TestPolicyFailureNeverHandsOffSecretsOrActivates(t *testing.T) {
	cli := &policyFailureCLI{}
	r := Runtime{CLI: cli, Policy: inspectCLI{err: errors.New("policy denied")}}
	spec := workerruntime.WorkerSpec{AllocationID: "test", SessionID: "session", NodeID: "node", Image: "worker", Generation: 1, FencingToken: 1, Resources: domain.ResourceVector{Slots: 1}, Files: map[string][]byte{"control_token": []byte("test-token")}}
	if _, err := r.Start(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "network policy") {
		t.Fatalf("expected policy failure: %v", err)
	}
	if cli.activated {
		t.Fatal("policy failure handed off secrets or activated worker")
	}
	if !cli.stopped {
		t.Fatal("failed worker was not stopped")
	}
	if _, mutated := spec.Files["resources.json"]; mutated {
		t.Fatal("start mutated caller's snapshot")
	}
}

func TestMissingDestinationSnapshotRejectedBeforeMutation(t *testing.T) {
	if err := (Runtime{}).ApplyDestinations(context.Background(), "test", nil, domain.ResourceVector{}); err == nil {
		t.Fatal("missing snapshot accepted")
	}
}

func (c inspectCLI) Run(context.Context, ...string) ([]byte, error) { return []byte(c.out), c.err }
func TestInspectStoppedAndUnavailable(t *testing.T) {
	r := Runtime{CLI: inspectCLI{out: `[{"Config":{"Labels":{"streamtool.allocation":"a","streamtool.slots":"1"}},"State":{"Running":false,"ExitCode":42}}]`}}
	w, err := r.Inspect(context.Background(), "a")
	if err != nil || w.State != "STOPPED" || w.ExitCode != 42 {
		t.Fatalf("bad stopped status: %+v %v", w, err)
	}
	r.CLI = inspectCLI{out: "daemon unavailable", err: errors.New("exit")}
	_, err = r.Inspect(context.Background(), "a")
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal("daemon failure hidden")
	}
}

func TestMissingContainer(t *testing.T) {
	r := Runtime{CLI: inspectCLI{out: "[]\nerror: no such object: stream-worker-a", err: errors.New("exit")}}
	_, err := r.Inspect(context.Background(), "a")
	if !errors.Is(err, workerruntime.ErrNotFound) {
		t.Fatalf("missing container: %v", err)
	}
}

func TestCLIStderrWarningDoesNotBecomeWorkerID(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'WARNING: config inaccessible' >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{CLI: CLI{Binary: binary}}
	workers, err := runtime.List(context.Background())
	if err != nil || len(workers) != 0 {
		t.Fatalf("warning corrupted inventory: %v, %v", workers, err)
	}
}

func TestCLIFailedInspectRetainsMissingClassification(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'Error: No such object' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{CLI: CLI{Binary: binary}}
	_, err := runtime.Inspect(context.Background(), "11111111-1111-4111-8111-111111111111")
	if !errors.Is(err, workerruntime.ErrNotFound) {
		t.Fatalf("missing classification lost: %v", err)
	}
}
