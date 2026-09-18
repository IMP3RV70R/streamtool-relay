package control

import (
	"context"
	"streamtool-relay/internal/config"
	"streamtool-relay/internal/domain"
)

func (c *Controller) resources(media config.MediaProfile, outputs int) domain.ResourceVector {
	r := c.WorkerResources
	r.Slots = 1
	// Reserve two CPU cores and 1 GiB for decoding and one continuous encoder.
	// Deployment estimates require confirmation on the actual host.
	if r.CPUMillis == 0 {
		r.CPUMillis = 2000
	}
	if r.MemoryBytes == 0 {
		r.MemoryBytes = 1 << 30
	}
	if r.IngressBPS == 0 {
		r.IngressBPS = 20_000_000
	}
	if r.EgressBPS == 0 {
		r.EgressBPS = 10_000_000
	}
	r.EgressBPS = max(r.EgressBPS, int64(media.VideoKbps+media.AudioKbps)*1200*int64(outputs))
	return r
}

func (c *Controller) reserveBudget(ctx context.Context, id string, r domain.ResourceVector) (bool, error) {
	tag, err := c.Store.Pool.Exec(ctx, `UPDATE worker_allocations AS wa SET cpu_millis=?3,memory_bytes=?4,ingress_bps=?5,egress_bps=?6 WHERE wa.id=?1 AND wa.node_id=?2 AND EXISTS(SELECT 1 FROM media_nodes mn WHERE mn.id=?2 AND mn.cpu_millis-COALESCE((SELECT sum(cpu_millis) FROM worker_allocations WHERE node_id=?2 AND id<>?1 AND state NOT IN ('STOPPED','FAILED')),0)>=?3 AND mn.memory_bytes-COALESCE((SELECT sum(memory_bytes) FROM worker_allocations WHERE node_id=?2 AND id<>?1 AND state NOT IN ('STOPPED','FAILED')),0)>=?4 AND mn.ingress_bps-COALESCE((SELECT sum(ingress_bps) FROM worker_allocations WHERE node_id=?2 AND id<>?1 AND state NOT IN ('STOPPED','FAILED')),0)>=?5 AND mn.egress_bps-COALESCE((SELECT sum(egress_bps) FROM worker_allocations WHERE node_id=?2 AND id<>?1 AND state NOT IN ('STOPPED','FAILED')),0)>=?6)`, id, c.NodeID, r.CPUMillis, r.MemoryBytes, r.IngressBPS, r.EgressBPS)
	return tag.RowsAffected() == 1, err
}
