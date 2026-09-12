# Parent orchestrator synchronization

`op serve` samples the local catalog, managed tmux windows, and the existing
agent detector independently of the dashboard. `GET /v1/state` returns the
latest cached snapshot using the same bearer authentication as `/v1/projects`.
Reading the endpoint does not advance the detector's temporal baseline.

## Configuration

Merge this example into your op configuration:

```json
{
  "server": {
    "listen": "127.0.0.1:8787",
    "tokenFile": "/home/ben/.config/op/api-token",
    "state": {
      "instanceId": "workstation-main",
      "parentUrl": "https://muxplane.example/v1/op/state",
      "refreshInterval": "2s",
      "heartbeatInterval": "10s",
      "staleAfter": "1m"
    }
  },
  "agents": { "enabled": true }
}
```

Set `OP_PARENT_TOKEN` for the parent's bearer credential; it overrides
`server.state.parentToken`. `OP_API_TOKEN` (or `server.tokenFile`) authenticates
incoming op requests separately. `parentUrl` is the **complete receiving
endpoint**, implemented by muxplane, not an endpoint served by op. If omitted,
sampling and `GET /v1/state` remain available without forwarding. Existing
notification providers, including the parent's `/v1/notify`, remain independently
configured and retain their three-type notification contract.

Set a distinct `instanceId` for every concurrently running op instance. Without
one, op generates a random identity once in the platform user configuration
directory at `op/instance-id`. Preserve that file across restarts; do not copy it
to another machine. Multiple instances under the same user need explicit unique
IDs. Hostnames are display metadata, not identity.

Omitted/zero intervals default to 5 seconds for sampling, 30 seconds for
heartbeats, and 2 minutes for staleness. Each dependency sample and outbound
HTTP request has a 10-second timeout. Retries continue until success or shutdown,
with backoff starting at 100 milliseconds and capped at 30 seconds. There is
one in-flight event and one latest-only pending snapshot; redirects are rejected.

## Replacement protocol

Each child-initiated POST carries a versioned `state.snapshot` event containing
the full current state. This is also the initial synchronization and heartbeat
contract. The parent should compare successive snapshots to discover catalog
additions/updates/removals, opened/closed windows, and agent activity changes.
It does not need to interpret terminal output. Samples can coalesce during an
outage; this protocol reports reconcilable current state, not an audit history
of every intermediate transition.

The parent must authenticate incoming requests and bind each child credential
to its permitted instance identity. Return a 2xx response only after accepting
the event. Retried events keep the same ID and payload. Apply newer snapshots
atomically, ignore duplicate or lower sequences within the same epoch, and
replace the old instance state on a new epoch. A sequence gap is recoverable
directly from the next full snapshot; the parent never needs to dial the child
back. A child's epoch changes every process start, while its instance identity
persists. Retire old epochs once replaced so late old-epoch traffic cannot
resurrect stale entities. Only one live process may use an instance identity.

Delivery runs separately from sampling and local operations, with bounded
buffering and capped exponential backoff. Periodic full snapshots repair parent
restarts even if earlier events were acknowledged. There is no durable event
log. Shutdown cancels sampling, active requests, and retry waits; there is no
guaranteed final offline message. The parent should mark an instance unreachable
after missed heartbeats (allow at least three heartbeat intervals plus request
time), retain its last state as stale, and avoid treating it as an empty catalog.

## Coverage and correlation

- Open means a **managed tmux project window** was observed. Every window is a
  separate instance, preserving profiles and pane references. A manual tmux
  window closure is reflected by the next successful sample. GUI process
  lifecycles are untracked; missing GUI entries do not mean those apps closed.
- Entity identities are namespaced by op instance. Catalog IDs and local tmux
  references remain available for existing project-opening APIs.
- Native observations keep agent-native project/session IDs separately from the
  op catalog association. Association uses the longest canonical directory
  ancestor on path-component boundaries. A discovered worktree is its own
  catalog entry, not implicitly its main repository. Unmatched sessions remain
  visible. Symlink resolution is used when the local path exists.
- Tmux observations describe detected terminal activity. Native hooks/server
  events are a different source and may have no pane. Notification-only sources
  do not provide a complete session inventory. Their last known attention state
  eventually becomes stale/unknown; silence is not proof of termination.
- Native activity and explicit resolution/lifecycle events clear attention when
  the integration supplies them. Enable the relevant notification ingest or
  agent-server watcher to collect these events. Sources cannot reconstruct
  native sessions that never emitted an event since op started.
- Native evidence retains at most 1,024 sessions, evicting the least recently
  observed at capacity. Observer buffering is also bounded to 1,024 pending
  observations and coalesces/evicts under overload. Eviction means evidence is
  no longer retained, not that a native process terminated. Forwarded legacy
  notifications (`hops > 0`) are not attributed to the receiving op instance.
- `projects.data[].nativeId` is the original op catalog ID; `id` and association
  `projectId` fields in this protocol are namespaced. `tmux.data` includes the
  dashboard and other managed windows; a window with `projectId` is a tracked
  project instance. Native agent `nativeProjectId` is a separate agent-owned ID.
- Each section carries freshness and errors. A failed sample preserves the last
  successful data and marks its freshness; an empty successful section is what
  authorizes removal. Disabled/unavailable detection is distinguished from an
  empty successful agent sample. Parents should also age timestamps if sampling
  stalls while HTTP remains reachable.

See `/openapi.json` for the snapshot and event schemas.

## Updating installed hooks

After upgrading op, rerun `op notify install-claude` or
`op notify install-copilot` (with your original `--target` if customized), then
reload/restart the agent so it uses the updated hook registrations. The bundled
hooks now forward session start/end, prompt submission, and pre-tool execution
in addition to attention/idle events. These lifecycle payloads must contain a
real session ID to become native state evidence; identity-less legacy
notifications are still delivered but are not combined into an "unknown" agent.

Question/permission resolutions carrying request IDs remove only that pending
request. Another unresolved request in the session continues to report attention;
an explicit busy/idle session status supersedes the prior attention observations.
