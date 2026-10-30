# Architecture decision record: Echo v2

## Decision

Echo v2 is a Go control plane using HyperMeow directly. The old Node.js Docker monitoring assistant is not retained in the runtime architecture.

## Trust zones

### WhatsApp gateway

The gateway owns the sole active HyperMeow client and `mdbstore` connection. A MariaDB named lock prevents two Echo replicas from controlling one device concurrently. Event handlers do minimal work and enqueue normalized messages into a bounded process queue. Durable database transactions perform incident creation, acknowledgement enqueueing, analysis-job creation, and audit recording.

The gateway does not receive decrypted GitHub credentials or AI keys in a split deployment. The all-in-one binary supports small deployments, while package boundaries allow later process separation.

WhatsApp callbacks enqueue only normalized metadata and, for group images, a cloned media descriptor. The consumer first applies the bound-group and protocol-mention policy; private, unbound, disabled, and unmentioned messages never trigger a media download. After acceptance it streams the candidate screenshot through a hard-limited temporary file with a 30-second deadline, rejects declared or actual payloads above 5 MiB, and accepts only byte-detected PNG, JPEG, or WEBP content matching the declared MIME type. SVG, video, audio, PDFs, archives, and arbitrary documents are not retained. Accepted bytes, hash, and incident are committed together before analysis is queued.

### Control plane

The control plane owns configuration and policy. The embedded UI communicates only with authenticated same-origin JSON endpoints. Every mutating browser request requires a CSRF token tied to the signed session.

Approval state is durable. Approval JSON is hashed before storage, and the one-time WhatsApp code is stored only as a SHA-256 hash. Approving a diagnosis cannot implicitly approve a patch or deployment.

### Workers

Workers claim jobs with MariaDB row locks and expiring leases. Active operations renew their lease and persistent worker heartbeat using the exact worker ID and attempt fence. A watchdog opens one durable `queue-stalled` alert only when ready work has exceeded its grace period without a fresh heartbeat; open and resolve notifications commit atomically with that lifecycle. Repository archives are downloaded with an application-managed fine-grained token, extracted without links, bounded by size, and removed after work. The token is decrypted only when a GitHub operation requires it. Repository code is read as untrusted text; it is never executed on the Echo host.

The patch path uses GitHub's blob/tree/commit/ref APIs. It creates one new commit on an `echo/` branch and opens a draft PR. Protected and generated paths are rejected before any GitHub mutation. A separate repository-native production job may revalidate successful checks and merge only the exact approved PR head, then monitor the existing default-branch workflow; it cannot inherit patch approval.

## Persistence

`mdbstore` exclusively owns `echo_whatsapp`. Echo never joins business data to its session tables.

`echo_core` is migration-owned by Echo. The key durable queues are:

- `echo_jobs`: leased, retryable background work.
- `echo_whatsapp_outbox`: retryable outbound messages with expiring leases and stable WhatsApp message IDs across crash recovery.
- `echo_runtime_component_alerts`: one durable open-or-resolved lifecycle per supervised Echo component, used to deduplicate failure and recovery notifications across retries and process restarts.
- `echo_briefing_settings`: the opt-in local schedule and last successfully queued local date for the owner's daily operational briefing.
- `echo_incident_attachments`: byte-verified, SHA-256-addressed screenshot evidence linked to an incident and pruned according to `ECHO_ATTACHMENT_RETENTION`.
- `echo_approvals`: expiring one-action authorization.
- `echo_audit_events`: append-only operational evidence.
- `echo_encrypted_settings`: AES-GCM ciphertext and non-secret connection metadata; the encryption key remains outside MariaDB.

MariaDB deadlocks and connection failure are allowed to surface to job retry logic; external mutations must carry enough identifiers to be reconciled before a retry.

## Runtime supervision and readiness

The all-in-one process supervises every required long-running loop. The worker, worker-health watchdog, monitor-heartbeat watchdog, enabled WhatsApp gateway, and daily-brief scheduler publish a small in-memory state machine and restart with bounded exponential backoff after an unexpected return. The supervisor reconciles each fixed component name with a durable alert lifecycle: the first unexpected exit opens or reopens the alert, repeated failures only refresh it, and 30 seconds of stable execution resolves it. Each lifecycle transition, audit event, and optional owner notification is one MariaDB transaction. On process startup the same stability window clears stale open alerts left by a previous crash; disabling a component clears its stale alert without pretending that it is running. Raw errors remain in structured server logs; persistence, the authenticated runtime API, and WhatsApp expose only fixed safe status and summary values.

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
