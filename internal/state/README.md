# Instance state

`New(domain.Service, Options) (*Tracker, error)` requires a configured, stable
`InstanceID`. It creates a cryptographically random epoch for this tracker.
`Run(context.Context) error` starts one sampler and an independent HTTP sender,
samples immediately, and blocks until cancellation. A tracker can be run once.
`Snapshot() Snapshot` returns an isolated copy; `Observe(notify.Observation)`
accepts native activity independently of notification delivery.

Zero-valued intervals default to 5 seconds for sampling, 30 seconds for
heartbeats, and 2 minutes for staleness. Negative intervals are rejected.

## Snapshot semantics

Every emitted envelope is version 1, type `state.snapshot`, and contains a full
replacement snapshot. Snapshot revision equals envelope sequence. Receivers
should deduplicate `eventId`, order by sequence within `(instanceId, epoch)`, and
replace all previously stored sections, including empty arrays. Epoch changes
identify process restarts; sequence is not ordered across epochs.

Projects, tmux windows, agents, and host statistics have independent section
freshness. `available` indicates at least one successful sample, `updatedAt` the
last successful sample, and `attemptedAt` the last attempted sample. Errors retain
old data and mark it stale. Agent-section freshness describes the pane detector;
individual native observations have their own `observedAt` and `stale` values.
`StatsSnapshot.AgentsError` affects agents without discarding healthy host data.
Successful empty results remove catalog entries, windows, or pane agents.

Native sessions are bounded to 1024 entries, evicting the least recently observed
when full. Quiet native sessions become stale/unknown, never implicitly
terminated. Only an explicit observation sets `terminated`. Each source/session
pair is distinct; pane observations are separate evidence and are not merged
with native sessions based on a shared directory. Coverage and optional last
notification describe the native evidence available.

IDs use individually base64url-encoded components prefixed with the instance ID;
native identifiers are exposed separately. Project identity uses its canonical
path. Correlation uses the longest canonical ancestor path (including symlink
resolution); nested worktrees remain separate projects. Native project IDs are
never treated as catalog project IDs. Windows retain profile and all panes.

The sampler uses the service's existing detector, preserving its baseline without
a dashboard. The production service's `GetStatsForTmux` classifies the same tmux
snapshot used for references. Alternate services can fall back to
`GetStatsSnapshot`; their tmux and stats may be adjacent observations. Each
dependency call receives a 10-second deadline.

## Forwarding semantics

`ParentURL` is the exact POST endpoint, including its configured path and query.
No path is appended. `ParentToken` becomes a Bearer authorization header.
Redirects are not followed. Every non-2xx result or transport error retries the
same immutable bytes and event ID with exponential backoff from 100 milliseconds
to a 30-second cap. HTTP attempts have a 10-second timeout and inherit cancellation.

There is one in-flight envelope plus one nonblocking, latest-only pending slot.
Intermediate snapshots can be coalesced during outages; this is state replication,
not an event log. After a retried request succeeds, a fresh full snapshot replaces
the backlog immediately. Sampling and periodic heartbeats also publish full
snapshots, so eventual recovery includes deletions and current freshness.
