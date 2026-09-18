package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"streamtool-relay/internal/domain"
	"streamtool-relay/internal/pipeline"
	workerruntime "streamtool-relay/internal/runtime"
	"streamtool-relay/internal/worker"
	"strings"
	"time"
)

type Commander interface {
	Run(context.Context, ...string) ([]byte, error)
}
type InputCommander interface {
	RunInput(context.Context, []byte, ...string) ([]byte, error)
}
type CLI struct {
	Binary     string
	PrefixArgs []string
}

func (c CLI) Run(ctx context.Context, args ...string) ([]byte, error) {
	return c.RunInput(ctx, nil, args...)
}
func (c CLI) RunInput(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	binary := c.Binary
	if binary == "" {
		binary = "docker"
	}
	cmd := exec.CommandContext(ctx, binary, append(append([]string{}, c.PrefixArgs...), args...)...)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.Output()
	if failure, ok := err.(*exec.ExitError); ok {
		return append(out, failure.Stderr...), err
	}
	return out, err
}

type Runtime struct {
	CLI     Commander
	Network string
	Policy  Commander
}

func (r Runtime) Start(ctx context.Context, s workerruntime.WorkerSpec) (workerruntime.Worker, error) {
	if err := workerruntime.ValidateStart(s); err != nil {
		return workerruntime.Worker{}, err
	}
	current, err := r.inspect(ctx, s.AllocationID, false)
	if err == nil {
		if current.Spec.FencingToken > s.FencingToken {
			return workerruntime.Worker{}, workerruntime.ErrFenced
		}
		if current.Spec.Generation == s.Generation && current.State == "RUNNING" {
			return current, nil
		}
		if err = r.Stop(ctx, s.AllocationID); err != nil {
			return workerruntime.Worker{}, err
		}
	} else if !errors.Is(err, workerruntime.ErrNotFound) {
		return workerruntime.Worker{}, err
	}
	resources, _ := json.Marshal(s.Resources)
	files := make(map[string][]byte, len(s.Files)+1)
	for name, contents := range s.Files {
		files[name] = contents
	}
	s.Files = files
	s.Files["resources.json"] = resources
	args := []string{"run", "-d", "--name", "stream-worker-" + s.AllocationID, "--label", "streamtool.allocation=" + s.AllocationID, "--label", "streamtool.session=" + s.SessionID, "--label", "streamtool.node=" + s.NodeID, "--label", "streamtool.generation=" + strconv.FormatUint(s.Generation, 10), "--label", "streamtool.fencing=" + strconv.FormatUint(s.FencingToken, 10), "--label", "streamtool.resources=" + string(resources), "--label", "streamtool.slots=" + strconv.Itoa(s.Resources.Slots), "--restart", "no", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m", "--tmpfs", "/run/secrets:rw,noexec,nosuid,nodev,size=64m,mode=0700,uid=65532,gid=65532"}
	if s.Resources.MemoryBytes > 0 {
		args = append(args, "--memory", strconv.FormatInt(s.Resources.MemoryBytes, 10))
	}
	if s.Resources.CPUMillis > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%.3f", float64(s.Resources.CPUMillis)/1000))
	}
	if r.Network != "" {
		args = append(args, "--network", r.Network)
	}
	args = append(args, "--pids-limit", "512")
	keys := make([]string, 0, len(s.Environment))
	for k := range s.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+s.Environment[k])
	}
	args = append(args, "--entrypoint", "/usr/local/bin/managed-worker", s.Image)
	if _, err = r.CLI.Run(ctx, args...); err != nil {
		return workerruntime.Worker{}, errors.New("docker start failed")
	}
	if r.Policy != nil {
		if _, err = r.Policy.Run(ctx, s.AllocationID, strconv.FormatInt(s.Resources.IngressBPS, 10), strconv.FormatInt(s.Resources.EgressBPS, 10)); err != nil {
			_ = r.Stop(ctx, s.AllocationID)
			return workerruntime.Worker{}, errors.New("worker network policy failed")
		}
	}
	uploader, ok := r.CLI.(InputCommander)
	if !ok {
		return workerruntime.Worker{}, errors.New("secret transport unavailable")
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	names := make([]string, 0, len(s.Files))
	for name := range s.Files {
		if name != "ready" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		b := s.Files[name]
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(b))}); err != nil {
			return workerruntime.Worker{}, err
		}
		if _, err = tw.Write(b); err != nil {
			return workerruntime.Worker{}, err
		}
	}
	if err = tw.Close(); err != nil {
		return workerruntime.Worker{}, err
	}
	if _, err = uploader.RunInput(ctx, archive.Bytes(), "exec", "-i", "--user", "65532", "stream-worker-"+s.AllocationID, "/usr/local/bin/stream-worker", "runtime-io", "receive"); err != nil {
		_ = r.Stop(ctx, s.AllocationID)
		return workerruntime.Worker{}, errors.New("secret handoff failed")
	}
	if _, err = r.CLI.Run(ctx, "exec", "--user", "65532", "stream-worker-"+s.AllocationID, "/usr/local/bin/stream-worker", "runtime-io", "activate"); err != nil {
		_ = r.Stop(ctx, s.AllocationID)
		return workerruntime.Worker{}, errors.New("worker activation failed")
	}
	return r.inspect(ctx, s.AllocationID, false)
}
func (r Runtime) Stop(ctx context.Context, id string) error {
	if !workerruntime.ValidID(id) {
		return errors.New("invalid allocation id")
	}
	// A failed pre-activation handoff has no resources/token files yet. Cleanup
	// must not depend on a readable worker snapshot or authenticated status.
	if out, err := r.CLI.Run(ctx, "inspect", "stream-worker-"+id, "--format", "{{.Id}}"); err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no such") {
			return nil
		}
		return errors.New("docker inspect unavailable")
	}
	// Allow GStreamer to close its outputs before forcibly removing the container.
	if _, err := r.CLI.Run(ctx, "stop", "-t", "10", "stream-worker-"+id); err != nil {
		return errors.New("docker stop failed")
	}
	if _, err := r.CLI.Run(ctx, "rm", "-f", "stream-worker-"+id); err != nil {
		return errors.New("docker remove failed")
	}
	return nil
}
func (r Runtime) Inspect(ctx context.Context, id string) (workerruntime.Worker, error) {
	return r.inspect(ctx, id, true)
}
func (r Runtime) inspect(ctx context.Context, id string, report bool) (workerruntime.Worker, error) {
	if !workerruntime.ValidID(id) {
		return workerruntime.Worker{}, errors.New("invalid allocation id")
	}
	out, err := r.CLI.Run(ctx, "inspect", "stream-worker-"+id)
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no such") {
			return workerruntime.Worker{}, workerruntime.ErrNotFound
		}
		return workerruntime.Worker{}, errors.New("docker inspect unavailable")
	}
	var data []struct {
		Config struct{ Labels map[string]string }
		State  struct {
			Running   bool
			ExitCode  int
			StartedAt time.Time
		}
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
		}
	}
	if json.Unmarshal(out, &data) != nil || len(data) != 1 {
		return workerruntime.Worker{}, errors.New("invalid docker inspect")
	}
	d := data[0]
	labels := d.Config.Labels
	generation, _ := strconv.ParseUint(labels["streamtool.generation"], 10, 64)
	fence, _ := strconv.ParseUint(labels["streamtool.fencing"], 10, 64)
	slots, _ := strconv.Atoi(labels["streamtool.slots"])
	w := workerruntime.Worker{ID: id, State: "STOPPED", ExitCode: d.State.ExitCode, StartedAt: d.State.StartedAt}
	w.Spec.AllocationID = labels["streamtool.allocation"]
	w.Spec.SessionID = labels["streamtool.session"]
	w.Spec.NodeID = labels["streamtool.node"]
	w.Spec.Generation = generation
	w.Spec.FencingToken = fence
	w.Spec.Resources.Slots = slots
	if raw := labels["streamtool.resources"]; raw != "" {
		if json.Unmarshal([]byte(raw), &w.Spec.Resources) != nil {
			return workerruntime.Worker{}, errors.New("invalid resource labels")
		}
	}
	if d.State.Running {
		w.State = "RUNNING"
		raw, e := r.CLI.Run(ctx, "exec", "--user", "65532", "stream-worker-"+id, "/usr/local/bin/stream-worker", "runtime-io", "read-resources")
		if e != nil || json.Unmarshal(raw, &w.Spec.Resources) != nil {
			return workerruntime.Worker{}, errors.New("worker resource snapshot unavailable")
		}
	}
	if report && d.State.Running {
		token, e := r.CLI.Run(ctx, "exec", "--user", "65532", "stream-worker-"+id, "/usr/local/bin/stream-worker", "runtime-io", "read-token")
		address := d.NetworkSettings.Networks[r.Network].IPAddress
		if e == nil && address != "" {
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+address+":9090/v1/status", nil)
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
			client := &http.Client{Timeout: 2 * time.Second}
			res, e := client.Do(req)
			if e == nil {
				defer res.Body.Close()
				var snap worker.Snapshot
				if res.StatusCode == 200 && json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&snap) == nil {
					w.Report = &snap
				}
			}
		}
	}
	return w, nil
}
func (r Runtime) List(ctx context.Context) ([]workerruntime.Worker, error) {
	out, err := r.CLI.Run(ctx, "ps", "-a", "--filter", "label=streamtool.allocation", "--format", "{{.Label \"streamtool.allocation\"}}")
	if err != nil {
		return nil, errors.New("docker list unavailable")
	}
	workers := []workerruntime.Worker{}
	for _, id := range strings.Fields(string(out)) {
		w, err := r.inspect(ctx, id, false)
		if errors.Is(err, workerruntime.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		workers = append(workers, w)
	}
	return workers, nil
}

// ApplyDestinations replaces secret files atomically inside the worker's tmpfs,
// then asks the running pipeline to reconcile its branches.
func (r Runtime) ApplyDestinations(ctx context.Context, id string, files map[string][]byte, resources domain.ResourceVector) error {
	if !workerruntime.ValidID(id) {
		return errors.New("invalid allocation")
	}
	if len(files["destinations.json"]) == 0 {
		return errors.New("missing destination snapshot")
	}
	if resources.Slots != 1 || resources.CPUMillis <= 0 || resources.MemoryBytes <= 0 || resources.IngressBPS <= 0 || resources.EgressBPS <= 0 {
		return errors.New("invalid resources")
	}
	if r.Policy != nil {
		if _, err := r.Policy.Run(ctx, id, strconv.FormatInt(resources.IngressBPS, 10), strconv.FormatInt(resources.EgressBPS, 10)); err != nil {
			return errors.New("network policy update failed")
		}
	}
	if _, err := r.CLI.Run(ctx, "update", "--cpus", fmt.Sprintf("%.3f", float64(resources.CPUMillis)/1000), "--memory", strconv.FormatInt(resources.MemoryBytes, 10), "stream-worker-"+id); err != nil {
		return errors.New("resource update failed")
	}
	files["resources.json"], _ = json.Marshal(resources)
	uploader, ok := r.CLI.(InputCommander)
	if !ok {
		return errors.New("secret transport unavailable")
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for name, b := range files {
		if name != "destinations.json" && name != "resources.json" && (!strings.HasPrefix(name, "destination-") || !workerruntime.ValidID(name)) {
			return errors.New("invalid destination file")
		}
		archiveName := name
		if name == "resources.json" {
			archiveName = "resources.next"
		}
		if err := tw.WriteHeader(&tar.Header{Name: archiveName, Mode: 0600, Size: int64(len(b))}); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if _, err := uploader.RunInput(ctx, archive.Bytes(), "exec", "-i", "--user", "65532", "stream-worker-"+id, "/usr/local/bin/stream-worker", "runtime-io", "receive"); err != nil {
		return errors.New("destination secret handoff failed")
	}
	if _, err := r.CLI.Run(ctx, "exec", "--user", "65532", "stream-worker-"+id, "/usr/local/bin/stream-worker", "runtime-io", "commit-resources"); err != nil {
		return errors.New("resource snapshot update failed")
	}
	// Inspect returns the current network address without exposing the credentials.
	out, err := r.CLI.Run(ctx, "inspect", "stream-worker-"+id, "--format", "{{json .NetworkSettings.Networks}}")
	if err != nil {
		return errors.New("worker network unavailable")
	}
	var networks map[string]struct{ IPAddress string }
	if json.Unmarshal(out, &networks) != nil {
		return errors.New("invalid worker network")
	}
	address := networks[r.Network].IPAddress
	if address == "" {
		return errors.New("worker network unavailable")
	}
	token, err := r.CLI.Run(ctx, "exec", "--user", "65532", "stream-worker-"+id, "/usr/local/bin/stream-worker", "runtime-io", "read-token")
	if err != nil {
		return errors.New("worker token unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", "http://"+address+":9090/v1/destinations", bytes.NewReader(files["destinations.json"]))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return errors.New("worker update unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 204 {
		return fmt.Errorf("worker update HTTP %d", res.StatusCode)
	}
	return nil
}

func (r Runtime) ApplySlatePolicy(ctx context.Context, id string, policy pipeline.SlatePolicy) error {
	if !workerruntime.ValidID(id) {
		return errors.New("invalid allocation")
	}
	out, err := r.CLI.Run(ctx, "inspect", "stream-worker-"+id, "--format", "{{json .NetworkSettings.Networks}}")
	if err != nil {
		return errors.New("worker network unavailable")
	}
	var networks map[string]struct{ IPAddress string }
	if json.Unmarshal(out, &networks) != nil || networks[r.Network].IPAddress == "" {
		return errors.New("invalid worker network")
	}
	token, err := r.CLI.Run(ctx, "exec", "--user", "65532", "stream-worker-"+id, "/usr/local/bin/stream-worker", "runtime-io", "read-token")
	if err != nil {
		return errors.New("worker token unavailable")
	}
	body, _ := json.Marshal(policy)
	req, err := http.NewRequestWithContext(ctx, "PUT", "http://"+networks[r.Network].IPAddress+":9090/v1/slate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return errors.New("worker update unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("worker update HTTP %d", res.StatusCode)
	}
	return nil
}
