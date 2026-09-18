# Single-VDS maintenance and loss

Stop accepting new sessions by disabling routing or draining the local Agent through
its private operator interface. Existing streams can finish normally. Updates use an
explicit maintenance stop and interrupt broadcasts; schedule them between streams.

The self-hosted product has no second node or cloud fencer. Whole-VDS loss interrupts
media. Recover the same host or restore a verified backup with matching images/keys,
confirm publisher/worker disconnection, then allow a new source connection. Never
infer that another host stopped merely because observations or leases expired.

Healthy API/Agent restarts adopt workers; worker crashes use bounded replacement.
Restore retains the greater Agent high watermark and ends old active sessions so an
older snapshot cannot resurrect a completed broadcast.
See [self-host backup/update/restore](../SELFHOST.md) and [failure matrix](failure-matrix.md).
