// Package control implements the SQLite-authoritative media control loop.
package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"streamtool-relay/internal/maintenance"
	"strings"
	"time"

	"database/sql"
	"streamtool-relay/internal/auth"
	"streamtool-relay/internal/config"
	appcrypto "streamtool-relay/internal/crypto"
	"streamtool-relay/internal/domain"
	"streamtool-relay/internal/node"
	"streamtool-relay/internal/persistence"
	"streamtool-relay/internal/pipeline"
	workerruntime "streamtool-relay/internal/runtime"
)

type Controller struct {
	MaintenanceDirectory                                                         string
	Environment, AllowedDestinationHosts                                         string
	Store                                                                        *persistence.Store
	Keys                                                                         appcrypto.KeyProvider
	Client                                                                       *http.Client
	EdgeClient                                                                   *http.Client
	NodeID, Region, AgentURL, WorkerImage, EdgeURL, SourceURL, EdgeID, EdgeToken string
	ReadKey                                                                      []byte
	Grace                                                                        time.Duration
	Slots                                                                        int
	WorkerResources                                                              domain.ResourceVector
	InstanceID                                                                   string
}

func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := c.Once(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("control reconciliation deferred", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (c *Controller) call(ctx context.Context, method, path string, input, output any) error {
	var b bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&b).Encode(input); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.AgentURL+path, &b)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Client.Do(req)
	if err != nil {
		return errors.New("agent unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return workerruntime.ErrNotFound
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("agent returned HTTP %d", res.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(output)
	}
	return nil
}
func command(id string, generation, fence uint64) domain.Command {
	return domain.Command{CommandID: fmt.Sprintf("%s-%d", id, fence), ResourceID: id, TraceID: "single-node", Generation: generation, FencingToken: fence, Deadline: time.Now().Add(45 * time.Second)}
}
func (c *Controller) Once(parent context.Context) error {
	leave, err := maintenance.Enter(c.MaintenanceDirectory)
	if err != nil {
		return err
	}
	defer leave()

	ctx, cancel := context.WithTimeout(parent, 40*time.Second)
	defer cancel()
	c.Store.ControlMu.Lock()
	defer c.Store.ControlMu.Unlock()
	fence, err := c.Store.NextFence(ctx)
	if err != nil {
		return err
	}
	return c.onceNode(ctx, fence)
}

func (c *Controller) onceNode(ctx context.Context, fence uint64) error {
	var err error
	// Failed edge polling never fabricates disconnects. A successful full snapshot
	// repairs missed observations, including edge restarts.
	if err = c.pollEdge(ctx); err != nil {
		slog.Warn("edge snapshot unavailable")
	}
	var heartbeat struct {
		NodeID       string                `json:"node_id"`
		FencingToken uint64                `json:"fencing_token"`
		Capacity     domain.ResourceVector `json:"capacity"`
	}
	if err = c.call(ctx, "GET", "/v1/heartbeat", nil, &heartbeat); err != nil {
		return err
	}
	if heartbeat.NodeID != c.NodeID {
		return errors.New("agent identity mismatch")
	}
	if heartbeat.FencingToken >= fence {
		fence, err = c.Store.AdvanceFence(ctx, heartbeat.FencingToken)
		if err != nil {
			return err
		}
	}
	c.Slots = min(c.Slots, heartbeat.Capacity.Slots)
	_, err = c.Store.Pool.Exec(ctx, `INSERT INTO media_nodes(id,region,endpoint,state,slots,cpu_millis,memory_bytes,ingress_bps,egress_bps,last_seen_at,instance_id) VALUES(?1,?2,?3,'ACTIVE',?4,0,0,0,0,strftime('%Y-%m-%dT%H:%M:%fZ','now'),?5) ON CONFLICT(id) DO UPDATE SET last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),slots=excluded.slots,endpoint=excluded.endpoint`, c.NodeID, c.Region, c.AgentURL, heartbeat.Capacity.Slots, c.InstanceID)
	if err != nil {
		return err
	}
	if _, err = c.Store.Pool.Exec(ctx, `UPDATE media_nodes SET unreachable_since=NULL WHERE id=?1`, c.NodeID); err != nil {
		return err
	}
	var draining bool
	if _, err = c.Store.Pool.Exec(ctx, `UPDATE media_nodes SET cpu_millis=?2,memory_bytes=?3,ingress_bps=?4,egress_bps=?5 WHERE id=?1`, c.NodeID, heartbeat.Capacity.CPUMillis, heartbeat.Capacity.MemoryBytes, heartbeat.Capacity.IngressBPS, heartbeat.Capacity.EgressBPS); err != nil {
		return err
	}
	if err = c.Store.Pool.QueryRow(ctx, "SELECT state='DRAINING' FROM media_nodes WHERE id=?1", c.NodeID).Scan(&draining); err != nil {
		return err
	}
	if err = c.call(ctx, "PUT", "/v1/draining", struct {
		Command  domain.Command
		Draining bool
	}{command(c.NodeID, 1, fence), draining}, nil); err != nil {
		return err
	}
	_, err = c.Store.Pool.Exec(ctx, `UPDATE stream_sessions AS ss SET desired='STOPPED',phase='STOPPING',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM streams s WHERE ss.stream_id=s.id AND ss.phase NOT IN ('ENDED','FAILED') AND NOT s.enabled`)
	if err != nil {
		return err
	}
	rows, err := c.Store.Pool.Query(ctx, `SELECT ss.id,ss.stream_id,ss.desired FROM stream_sessions ss JOIN streams s ON s.id=ss.stream_id WHERE ss.phase NOT IN ('ENDED','FAILED') AND s.region=?1 ORDER BY ss.started_at`, c.Region)
	if err != nil {
		return err
	}
	type session struct{ ID, Stream, Desired string }
	var sessions []session
	for rows.Next() {
		var s session
		if err = rows.Scan(&s.ID, &s.Stream, &s.Desired); err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if err = c.reconcile(ctx, s.ID, s.Stream, s.Desired, fence, draining); err != nil {
			slog.Warn("session reconciliation deferred", "session_id", s.ID, "error", err)
		}
	}
	// Remove orphan workers only after an authoritative database lookup. A worker
	// whose start response was lost is kept because its allocation was committed first.
	var workers []workerruntime.Worker
	if err = c.call(ctx, "GET", "/v1/workers", nil, &workers); err != nil {
		return err
	}
	for _, w := range workers {
		if w.Spec.NodeID != c.NodeID {
			continue
		}
		var keep bool
		if err = c.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_allocations wa JOIN stream_sessions ss ON ss.id=wa.session_id WHERE wa.id=?1 AND wa.desired='RUNNING' AND ss.desired='RUNNING' AND ss.phase NOT IN ('ENDED','FAILED'))`, w.Spec.AllocationID).Scan(&keep); err != nil {
			return err
		}
		if !keep {
			if err = c.call(ctx, "DELETE", "/v1/workers/"+w.Spec.AllocationID, command(w.Spec.AllocationID, w.Spec.Generation, fence), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

type allocation struct {
	ID         string
	Generation uint64
	Hash       string
	Restarts   int
	Retry      time.Time
	Healthy    *time.Time
}

func (c *Controller) reconcile(ctx context.Context, sessionID, streamID, desired string, fence uint64, draining bool) error {
	var a allocation
	var allocatedNode string
	err := c.Store.Pool.QueryRow(ctx, `SELECT id,desired_generation,config_hash,restart_count,retry_after,last_healthy_at,node_id FROM worker_allocations WHERE session_id=?1 AND state NOT IN ('STOPPED','FAILED') LIMIT 1`, sessionID).Scan(&a.ID, &a.Generation, &a.Hash, &a.Restarts, &a.Retry, &a.Healthy, &allocatedNode)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if a.ID != "" && allocatedNode != c.NodeID {
		return nil
	}
	if desired == "STOPPED" {
		if a.ID != "" {
			if err = c.call(ctx, "DELETE", "/v1/workers/"+a.ID, command(a.ID, a.Generation, fence), nil); err != nil {
				return err
			}
		}
		tx, err := c.Store.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `UPDATE worker_allocations SET desired='STOPPED',state='STOPPED',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE session_id=?1`, sessionID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE destination_runtimes SET state='STOPPED',last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE allocation_id=?1`, nullID(a.ID)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE stream_sessions SET phase='ENDED',ended_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, sessionID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	dests, err := c.destinations(ctx, streamID)
	if err != nil {
		return err
	}
	if len(dests) == 0 {
		// A revoked/disconnected integration must also stop the last output.
		// Keep the ingest session so reconnecting the destination can resume it.
		if a.ID == "" {
			return nil
		}
		if err = c.call(ctx, "DELETE", "/v1/workers/"+a.ID, command(a.ID, a.Generation, fence), nil); err != nil {
			return err
		}
		tx, err := c.Store.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `UPDATE worker_allocations SET desired='STOPPED',state='STOPPED',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, a.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE destination_runtimes SET state='STOPPED',last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE allocation_id=?1`, a.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE stream_sessions SET phase='SCHEDULING',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1 AND desired='RUNNING'`, sessionID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	policy, err := c.slatePolicy(ctx, streamID)
	if err != nil {
		return err
	}
	media := config.DefaultMediaProfile()
	err = c.Store.Pool.QueryRow(ctx, `SELECT COALESCE(p.width,1280),COALESCE(p.height,720),COALESCE(p.fps_num,30),COALESCE(p.fps_den,1),COALESCE(p.video_kbps,3000),COALESCE(p.audio_kbps,160) FROM streams s LEFT JOIN source_media_profiles p ON p.source_id=s.id WHERE s.id=?1`, streamID).Scan(&media.Width, &media.Height, &media.FPSNum, &media.FPSDen, &media.VideoKbps, &media.AudioKbps)
	if err != nil {
		return err
	}
	if err = media.Validate(); err != nil {
		return err
	}
	resources := c.resources(media, len(dests))
	hashBytes, _ := json.Marshal(struct {
		Destinations []persistence.Destination `json:"destinations"`
		Slate        pipeline.SlatePolicy      `json:"slate"`
		Media        config.MediaProfile       `json:"media"`
	}{dests, policy, media})
	sum := sha256.Sum256(hashBytes)
	hash := hex.EncodeToString(sum[:])
	if a.ID == "" {
		if draining {
			return nil
		}
		// The database owner and control mutex serialize local placement.
		var count int
		if err = c.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM worker_allocations WHERE node_id=?1 AND state NOT IN ('STOPPED','FAILED')`, c.NodeID).Scan(&count); err != nil {
			return err
		}
		if count >= c.Slots {
			return nil
		}
		r := resources
		var fits bool
		if err = c.Store.Pool.QueryRow(ctx, `SELECT cpu_millis-COALESCE((SELECT sum(cpu_millis) FROM worker_allocations WHERE node_id=?1 AND state NOT IN ('STOPPED','FAILED')),0)>=?2 AND memory_bytes-COALESCE((SELECT sum(memory_bytes) FROM worker_allocations WHERE node_id=?1 AND state NOT IN ('STOPPED','FAILED')),0)>=?3 AND ingress_bps-COALESCE((SELECT sum(ingress_bps) FROM worker_allocations WHERE node_id=?1 AND state NOT IN ('STOPPED','FAILED')),0)>=?4 AND egress_bps-COALESCE((SELECT sum(egress_bps) FROM worker_allocations WHERE node_id=?1 AND state NOT IN ('STOPPED','FAILED')),0)>=?5 FROM media_nodes WHERE id=?1`, c.NodeID, r.CPUMillis, r.MemoryBytes, r.IngressBPS, r.EgressBPS).Scan(&fits); err != nil {
			return err
		}
		if !fits {
			return nil
		}
		err = c.Store.Pool.QueryRow(ctx, `INSERT INTO worker_allocations(session_id,node_id,desired,state,desired_generation,fencing_token,config_hash,cpu_millis,memory_bytes,ingress_bps,egress_bps) VALUES(?1,?2,'RUNNING','PENDING',1,?3,?4,?5,?6,?7,?8) RETURNING id,desired_generation,config_hash,restart_count,retry_after,last_healthy_at`, sessionID, c.NodeID, fence, hash, r.CPUMillis, r.MemoryBytes, r.IngressBPS, r.EgressBPS).Scan(&a.ID, &a.Generation, &a.Hash, &a.Restarts, &a.Retry, &a.Healthy)
		if err != nil {
			return err
		}
	}
	if ok, err := c.reserveBudget(ctx, a.ID, resources); err != nil {
		return err
	} else if !ok {
		return nil
	}
	var current workerruntime.Worker
	err = c.call(ctx, "GET", "/v1/workers/"+a.ID, nil, &current)
	if err != nil && !errors.Is(err, workerruntime.ErrNotFound) {
		return err
	}
	healthy := err == nil && current.State == "RUNNING" && current.Report != nil
	configChanged := a.Hash != hash
	if healthy && current.Spec.Generation == a.Generation && !configChanged {
		return c.observe(ctx, sessionID, a, current)
	}
	if healthy && configChanged && current.Spec.Generation == a.Generation {
		files, err := c.destinationFiles(ctx, streamID, dests)
		if err != nil {
			return err
		}
		if err = c.call(ctx, "PUT", "/v1/workers/"+a.ID+"/destinations", struct {
			Command   domain.Command
			Secrets   map[string][]byte
			Resources domain.ResourceVector
		}{command(a.ID, a.Generation, fence), files, resources}, nil); err != nil {
			return err
		}
		if err = c.call(ctx, "PUT", "/v1/workers/"+a.ID+"/slate", struct {
			Command domain.Command
			Policy  pipeline.SlatePolicy
		}{command(a.ID, a.Generation, fence), policy}, nil); err != nil {
			return err
		}
		_, err = c.Store.Pool.Exec(ctx, `UPDATE worker_allocations SET config_hash=?2,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, a.ID, hash)
		return err
	}
	if time.Now().Before(a.Retry) {
		return nil
	}
	if err == nil && current.State == "RUNNING" && current.Spec.Generation == a.Generation && !configChanged {
		// Give a newly started process time to initialize, or a temporarily unavailable
		// status endpoint time to recover before restarting.
		since := current.StartedAt
		if a.Healthy != nil {
			since = *a.Healthy
		}
		grace := 30 * time.Second
		if a.Healthy == nil {
			grace = 60 * time.Second
		} // Includes the bounded 45-second fallback preparation.
		if time.Since(since) < grace {
			return nil
		}
	}
	if a.Restarts >= 5 && !configChanged {
		if err = c.call(ctx, "DELETE", "/v1/workers/"+a.ID, command(a.ID, a.Generation, fence), nil); err != nil {
			return err
		}
		tx, err := c.Store.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `UPDATE worker_allocations SET state='FAILED',desired='STOPPED',last_error='RESTARTS_EXHAUSTED',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, a.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE stream_sessions SET phase='FAILED',desired='STOPPED',end_reason='RESTARTS_EXHAUSTED',ended_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, sessionID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// A live process with the expected generation is adopted without changing its
	// credentials. A restart/config change receives a fresh generation and token.
	if current.ID != "" || configChanged {
		a.Generation++
	}
	files, err := c.destinationFiles(ctx, streamID, dests)
	if err != nil {
		return err
	}
	var assetKind string
	var asset []byte
	err = c.Store.Pool.QueryRow(ctx, `SELECT kind,content FROM source_fallback_assets WHERE source_id=?1`, streamID).Scan(&assetKind, &asset)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(asset) > 0 {
		files["fallback_asset"] = asset
	}
	files["source_token"] = []byte(auth.SignEdgeToken(auth.EdgeClaims{StreamID: streamID, Action: "read", AllocationID: a.ID, Generation: a.Generation}, c.ReadKey))
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	files["control_token"] = []byte(base64.RawURLEncoding.EncodeToString(random))
	source, err := url.Parse(c.SourceURL)
	if err != nil {
		return err
	}
	q := source.Query()
	q.Set("streamid", "read:"+streamID)
	source.RawQuery = q.Encode()
	spec := workerruntime.WorkerSpec{AllocationID: a.ID, SessionID: sessionID, NodeID: c.NodeID, Image: c.WorkerImage, Generation: a.Generation, FencingToken: fence, Resources: domain.ResourceVector{Slots: 1}, Environment: map[string]string{"WORKER_FALLBACK_IMAGE": "/usr/local/share/streamtool/offline.png", "WORKER_SLATE_ON_SOURCE_LOSS": strconv.FormatBool(policy.OnSourceLoss), "WORKER_SLATE_FORCED": strconv.FormatBool(policy.Forced), "WORKER_SESSION_ID": sessionID, "WORKER_SOURCE_URL": source.String(), "WORKER_SOURCE_AUTH": "token", "WORKER_SOURCE_TOKEN_FILE": "/run/secrets/source_token", "WORKER_DESTINATIONS_FILE": "/run/secrets/destinations.json", "WORKER_CONTROL_TOKEN_FILE": "/run/secrets/control_token"}}
	if len(asset) > 0 {
		key := "WORKER_FALLBACK_IMAGE"
		if assetKind == "video" {
			key = "WORKER_FALLBACK_VIDEO"
		}
		spec.Environment[key] = "/run/secrets/fallback_asset"
	}
	// Commit identity and backoff before the external side effect. Lost responses
	spec.Resources = resources
	mediaJSON, _ := json.Marshal(media)
	spec.Environment["WORKER_MEDIA_PROFILE"] = string(mediaJSON)
	spec.Environment["STREAMTOOL_ENV"] = c.Environment
	spec.Environment["ALLOWED_DESTINATION_HOSTS"] = c.AllowedDestinationHosts
	// are recovered by Inspect on the next pass, without launching a duplicate.
	delay := time.Duration(1<<min(a.Restarts, 5)) * time.Second
	_, err = c.Store.Pool.Exec(ctx, `UPDATE worker_allocations SET desired_generation=?2,config_hash=?3,fencing_token=?4,state='STARTING',restart_count=restart_count+1,retry_after=?5,last_healthy_at=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, a.ID, a.Generation, hash, fence, time.Now().Add(delay))
	if err != nil {
		return err
	}
	if _, err = c.Store.Pool.Exec(ctx, `UPDATE stream_sessions SET phase='STARTING',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, sessionID); err != nil {
		return err
	}
	var started workerruntime.Worker
	if err = c.call(ctx, "POST", "/v1/workers", node.StartRequest{Command: command(a.ID, a.Generation, fence), Spec: spec, Secrets: files}, &started); err != nil {
		return err
	}
	return nil
}

func (c *Controller) slatePolicy(ctx context.Context, streamID string) (pipeline.SlatePolicy, error) {
	policy := pipeline.SlatePolicy{OnSourceLoss: true}
	err := c.Store.Pool.QueryRow(ctx, `SELECT COALESCE(ss.on_source_loss,true),COALESCE(ss.forced,false) FROM streams s LEFT JOIN source_slates ss ON ss.source_id=s.id WHERE s.id=?1`, streamID).Scan(&policy.OnSourceLoss, &policy.Forced)
	return policy, err
}
func nullID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
func (c *Controller) destinations(ctx context.Context, streamID string) ([]persistence.Destination, error) {
	rows, err := c.Store.Pool.Query(ctx, `SELECT id,stream_id,name,endpoint,key_id,generation,secret_ciphertext,secret_nonce FROM destinations WHERE stream_id=?1 AND enabled AND NOT archived ORDER BY id`, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []persistence.Destination
	for rows.Next() {
		var d persistence.Destination
		if err = rows.Scan(&d.ID, &d.StreamID, &d.Name, &d.Endpoint, &d.KeyID, &d.Generation, &d.Ciphertext, &d.Nonce); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (c *Controller) observe(ctx context.Context, sessionID string, a allocation, w workerruntime.Worker) error {
	tx, err := c.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE worker_allocations SET fallback_active=?5,fallback_forced=?6,input_live=?7,input_unavailable=?8,input_error=?9,state='RUNNING',worker_id=?2,observed_generation=?3,last_healthy_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),last_error=NULL,restart_count=CASE WHEN ?4 THEN 0 ELSE restart_count END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, a.ID, w.ID, w.Spec.Generation, time.Since(w.StartedAt) > time.Minute, w.Report.FallbackActive, w.Report.FallbackForced, w.Report.InputLive, w.Report.InputUnavailable, w.Report.InputError)
	if err != nil {
		return err
	}
	phase := "STARTING"
	if w.Report.InputLive || w.Report.FallbackActive || w.Report.InputUnavailable {
		phase = "LIVE"
	}
	if _, err = tx.Exec(ctx, `UPDATE stream_sessions SET phase=?2,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1 AND desired='RUNNING'`, sessionID, phase); err != nil {
		return err
	}
	for id, d := range w.Report.Destinations {
		_, err = tx.Exec(ctx, `INSERT INTO destination_runtimes(allocation_id,destination_id,desired,state,generation,reconnect_count,last_error_code,last_seen_at) SELECT ?1,id,'RUNNING',?3,?4,?5,?6,strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM destinations WHERE id=?2 AND stream_id=(SELECT stream_id FROM stream_sessions WHERE id=?7) ON CONFLICT(allocation_id,destination_id) DO UPDATE SET state=excluded.state,generation=excluded.generation,reconnect_count=excluded.reconnect_count,last_error_code=excluded.last_error_code,last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, a.ID, id, d.State, d.Generation, d.Reconnects, string(d.ErrorCode), sessionID)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (c *Controller) pollEdge(ctx context.Context) error {
	type path struct {
		Name   string
		Ready  bool
		Source *struct{ Type, ID string }
	}
	active := map[string]path{}
	for page := 0; ; page++ {
		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/v3/paths/list?itemsPerPage=100&page=%d", c.EdgeURL, page), nil)
		if err != nil {
			return err
		}
		req.SetBasicAuth("controller", c.EdgeToken)
		res, err := c.EdgeClient.Do(req)
		if err != nil {
			return err
		}
		var result struct {
			Items     []path
			PageCount int
		}
		err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&result)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 {
			return errors.New("invalid edge snapshot")
		}
		for _, p := range result.Items {
			if p.Ready && p.Source != nil && (p.Source.Type == "srtConn" || p.Source.Type == "rtmpConn") {
				active[p.Name] = p
			}
		}
		if page+1 >= result.PageCount {
			break
		}
	}
	now := time.Now().UTC()
	// Disconnect missing/changed sources first, so a replacement source can be admitted.
	rows, err := c.Store.Pool.Query(ctx, `SELECT stream_id,connection_id,protocol,last_seen_at FROM ingest_connections WHERE edge_id=?1 AND status='CONNECTED'`, c.EdgeID)
	if err != nil {
		return err
	}
	type connection struct {
		stream, id, protocol string
		seen                 time.Time
	}
	var old []connection
	for rows.Next() {
		var v connection
		if err = rows.Scan(&v.stream, &v.id, &v.protocol, &v.seen); err != nil {
			rows.Close()
			return err
		}
		old = append(old, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range old {
		p, ok := active[v.stream]
		if (!ok || p.Source.ID != v.id) && now.Sub(v.seen) > c.Grace {
			if _, err = c.Store.ObserveIngest(ctx, c.EdgeID, v.id, v.stream, v.protocol, false, now, c.Grace); err != nil {
				return err
			}
		}
	}
	for streamID, p := range active {
		protocol := strings.TrimSuffix(p.Source.Type, "Conn")
		if _, err = c.Store.ObserveIngest(ctx, c.EdgeID, p.Source.ID, streamID, protocol, true, now, c.Grace); err != nil {
			return err
		}
	}
	_, err = c.Store.Pool.Exec(ctx, `INSERT INTO edge_observation_health(edge_id,last_seen_at) VALUES(?1,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(edge_id) DO UPDATE SET last_seen_at=excluded.last_seen_at`, c.EdgeID)
	return err
}

func (c *Controller) destinationFiles(ctx context.Context, streamID string, dests []persistence.Destination) (map[string][]byte, error) {
	files := map[string][]byte{}
	documents := []config.DestinationDocument{}
	for _, d := range dests {
		secret, err := appcrypto.Decrypt(ctx, c.Keys, appcrypto.Envelope{KeyID: d.KeyID, Nonce: d.Nonce, Ciphertext: d.Ciphertext}, []byte(streamID+":"+d.Name))
		if err != nil {
			return nil, errors.New("destination decryption failed")
		}
		name := "destination-" + d.ID
		files[name] = secret
		documents = append(documents, config.DestinationDocument{ID: d.ID, Generation: uint64(d.Generation), URL: d.Endpoint, SecretFile: "/run/secrets/" + name})
	}
	var err error
	files["destinations.json"], err = json.Marshal(documents)
	return files, err
}
