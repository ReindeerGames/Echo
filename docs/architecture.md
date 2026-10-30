# Architecture decision record: Echo v2

## Decision

Echo v2 is a Go control plane using HyperMeow directly. The old Node.js Docker monitoring assistant is not retained in the runtime architecture.

## Trust zones

### WhatsApp gateway

The gateway owns the sole active HyperMeow client and `mdbstore` connection. A MariaDB named lock prevents two Echo replicas from controlling one device concurrently. Event handlers do minimal work and enqueue normalized messages into a bounded process queue. Durable database transactions perform incident creation, acknowledgement enqueueing, analysis-job creation, and audit recording.

The gateway does not receive decrypted GitHub credentials or AI keys in a split deployment. The all-in-one binary supports small deployments, while package boundaries allow later process separation.

WhatsApp callbacks enqueue only normalized metadata and, for group images, a cloned media descriptor. The consumer first applies the bound-group policy and requires either a protocol mention for a new incident or a quoted-message reference to an open incident thread; private, unbound, disabled, and unrelated messages never trigger a media download. After acceptance it streams the candidate screenshot through a hard-limited temporary file with a 30-second deadline, rejects declared or actual payloads above 5 MiB, and accepts only byte-detected PNG, JPEG, or WEBP content matching the declared MIME type. SVG, video, audio, PDFs, archives, and arbitrary documents are not retained. Accepted bytes, hash, and incident are committed together before analysis is queued.

An open incident forms a bounded WhatsApp evidence thread. A reply is correlated only when its protocol `stanza_id` references the original report or another retained message in the same bound group. The message, optional screenshot, incremented evidence revision, updated-analysis job, group acknowledgement, pending-approval invalidation, and audit record commit atomically. Analysis jobs take a per-incident database lock, skip revisions already covered, and do not notify the owner when newer evidence supersedes their input while they run. Patch approvals carry the current evidence revision and resolution re-checks it transactionally before queuing work.

### Control plane

The control plane owns configuration and policy. The embedded UI communicates only with authenticated same-origin JSON endpoints. Every mutating browser request requires a CSRF token tied to the signed session.

The owner assistant exposes only bounded read tools. A deep service investigation joins retained normalized signals and open alerts with up to twelve enabled services from the same project, the project's incidents and deployments, and up to eight configured repository workflow summaries plus three recent default-branch commit summaries per repository. Explicit same-project dependency relationships are presented separately and prioritized ahead of otherwise related services; the remaining services are ordered by open alerts, unhealthy/degraded/stale/missing evidence, and criticality. Declared topology is operator-maintained evidence, not proof of causation, and services from other projects are excluded. Commit messages are reduced to one bounded line and common credential literals are redacted at the GitHub client boundary. WhatsApp delivery diagnostics are projected at the database boundary into channel type, state, attempts, timing, and a fixed failure category; bodies, destination identifiers, WhatsApp message IDs, and raw errors are never selected into the public model. Active and upcoming maintenance windows are exposed as bounded read evidence, while scheduling and cancellation remain authenticated control-plane mutations rather than assistant tools. Every collection has an independent cap and truncation marker. This correlation path does not contact the workload, retrieve logs, download source, or acquire shell, SSH, Docker, filesystem, or mutation access; temporal proximity is presented as evidence rather than asserted causation.

Approval state is durable. Approval JSON is hashed before storage, and the one-time WhatsApp code is stored only as a SHA-256 hash. Approving a diagnosis cannot implicitly approve a patch or deployment.

### Workers

Workers claim jobs with MariaDB row locks and expiring leases. Active operations renew their lease and persistent worker heartbeat using the exact worker ID and attempt fence. A watchdog opens one durable `queue-stalled` alert only when ready work has exceeded its grace period without a fresh heartbeat; open and resolve notifications commit atomically with that lifecycle. Repository archives are downloaded with an application-managed fine-grained token, extracted without links, bounded by size, and removed after work. The token is decrypted only when a GitHub operation requires it. Repository code is read as untrusted text; it is never executed on the Echo host.

Owner-initiated code investigations use the same safe default-branch archive path in an exact temporary workspace. Search applies file-count, byte, entry, per-file, result-count, and request-time limits; skips credential/key paths, dependencies, generated trees, binaries, and oversized files; redacts common secret literals; and sends only small line-numbered excerpts to the tool model. The workspace is deleted at the end of the request. A search result is explicitly incomplete and untrusted and cannot create an approval or invoke any mutation path.

The patch path uses GitHub's blob/tree/commit/ref APIs. It creates one new commit on an `echo/` branch and opens a draft PR. Protected and generated paths are rejected before any GitHub mutation. A separate repository-native production job may revalidate successful checks and merge only the exact approved PR head, then monitor the existing default-branch workflow; it cannot inherit patch approval.

### Runtime observer

The observer has no database, GitHub, WhatsApp, AI, shell, or SSH access. HTTPS probes discard response bodies and reduce verified certificate state to an expiry timestamp and days remaining. System probes read only bounded host counters, freshness probes inspect metadata for one exact regular file or at most 1,000 direct entries in one exact directory, and Docker probes can reach only the local exact-allowlist proxy. Freshness evidence never contains file contents, paths, or filenames. The proxy never returns logs, environment variables, labels, mounts, image configuration, health output, or raw daemon errors.

## Persistence

`mdbstore` exclusively owns `echo_whatsapp`. Echo never joins business data to its session tables.

`echo_core` is migration-owned by Echo. The key durable queues are:

- `echo_jobs`: leased, retryable background work.
- `echo_whatsapp_outbox`: retryable outbound messages with expiring leases and stable WhatsApp message IDs across crash recovery. Confirmed-sent rows are pruned in bounded batches after `ECHO_WHATSAPP_OUTBOX_RETENTION`; every unconfirmed or dead row is preserved.
- `echo_runtime_component_alerts`: one durable open-or-resolved lifecycle per supervised Echo component, used to deduplicate failure and recovery notifications across retries and process restarts.
- `echo_briefing_settings`: the opt-in local schedule and last successfully queued local date for the owner's daily operational briefing.
- `echo_incident_attachments`: byte-verified, SHA-256-addressed screenshot evidence linked to an incident and pruned according to `ECHO_ATTACHMENT_RETENTION`.
- `echo_incident_messages`: the original report and bounded correlated follow-up thread; WhatsApp chat/message IDs provide idempotency and reply correlation without text heuristics.
- `echo_monitor_events`: signed normalized service observations. Superseded history is pruned in bounded startup and daily batches according to `ECHO_MONITOR_RETENTION`; the newest non-watchdog event per service and all alert-referenced events are retained.
- `echo_service_dependencies`: directed, same-project service topology used to prioritize correlated investigations without granting any collection or mutation capability.
- `echo_service_maintenance_windows`: non-overlapping per-service notification-suppression windows; alert evidence continues and suppression provenance stays attached to affected open alerts.
- `echo_service_alerts`: acknowledgement provenance, last notification time, and a monotonic reminder count make critical-alert follow-through durable and idempotent.
- `echo_approvals`: expiring one-action authorization.
- `echo_audit_events`: append-only operational evidence.
- `echo_encrypted_settings`: AES-GCM ciphertext and non-secret connection metadata; the encryption key remains outside MariaDB.

MariaDB deadlocks and connection failure are allowed to surface to job retry logic; external mutations must carry enough identifiers to be reconciled before a retry.

## Runtime supervision and readiness

The all-in-one process supervises every required long-running loop. The worker, worker-health watchdog, monitor-heartbeat watchdog, retention maintenance, enabled WhatsApp gateway, and daily-brief scheduler publish a small in-memory state machine and restart with bounded exponential backoff after an unexpected return. The retention component runs immediately and daily, independently applying conversation, screenshot, monitoring-history, and confirmed-sent outbox windows even during long uptimes. The supervisor reconciles each fixed component name with a durable alert lifecycle: the first unexpected exit opens or reopens the alert, repeated failures only refresh it, and 30 seconds of stable execution resolves it. Each lifecycle transition, audit event, and optional owner notification is one MariaDB transaction. On process startup the same stability window clears stale open alerts left by a previous crash; disabling a component clears its stale alert without pretending that it is running. Raw errors remain in structured server logs; persistence, the authenticated runtime API, and WhatsApp expose only fixed safe status and summary values.

The daily-brief scheduler evaluates an explicit IANA timezone and local time. It does no AI work before a briefing is due or when no owner exists. After rendering the deterministic operational brief, a locked settings row makes the outbox insert, last-sent local date, and audit event one transaction, preventing duplicates across retries and restarts.

`/healthz` proves only that the HTTP process can answer. `/readyz` additionally requires core MariaDB reachability, agreement between applied and embedded migrations, and a `running` state from every component enabled by configuration. Container orchestration therefore stops routing traffic when Echo is alive but operationally incomplete.

## Deployment verification

Dispatch and merge are not verification. In explicit-environment mode, a deployment stays `queued` until a GitHub `workflow_run` event with a valid HMAC signature matches repository, workflow, and artifact SHA. In repository-native mode, the approved worker polls GitHub for push workflows bound to the resulting default-branch commit. Only verified success resolves the incident; failure moves it to `failed` and notifies the owner.

## Future process split

For higher assurance, build three images from the same packages:

1. `echo-gateway` with only MariaDB and WhatsApp network access.
2. `echo-control` with API, UI, policy, and webhook access.
3. `echo-worker` with GitHub and AI egress but no WhatsApp-session database access.

The current package interfaces and durable queues are designed to support this without changing the data model.
