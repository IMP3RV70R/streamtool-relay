# Failure matrix

| Fault | Expected blast radius | Recovery signal |
|---|---|---|
| One RTMP receiver refuses/resets | One destination branch | branch `RECONNECTING` then `STREAMING` |
| Worker crash | One session | controller creates a bounded replacement with generation/fencing |
| Agent restart | No healthy media interruption | existing labeled containers adopted |
| Controller/API outage | No healthy media interruption | reconcilers converge after DB/API return |
| Edge hook loss | No durable divergence | periodic Control API observation restores state |
| Node loss | Sessions on that node; non-seamless | single VDS interrupts media; recover host or restore verified backup; no automatic cross-host failover |
| SQLite/control outage | Mutations/scheduling pause | healthy media continues; reconcile on return |
| Certificate expiry | Affected control link only | idle private-leaf renewal or maintenance restores heartbeat |
