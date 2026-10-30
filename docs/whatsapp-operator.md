# WhatsApp operator assistant

The configured owner can message Echo directly for a read-only operational view. Echo authorizes the sender against the selected owner contact, reads only its structured MariaDB state, and audits the interpreted intent without storing the question body in the audit event.

Selecting an owner in the control plane atomically queues a private confirmation describing the notification scope and the `brief` command. If that message later remains pending or becomes dead, the same durable outbox state is visible on Overview and System; Echo never silently treats a half-saved owner change as complete.

Under **WhatsApp → Daily briefing**, the owner can opt into one automatic brief at an explicit local time and IANA timezone. Echo skips rendering while the schedule is disabled, before the local due time, or while no owner is configured. The outbox message, audit event, and last-sent local date commit atomically, so scheduler retries and restarts cannot send two briefs for the same local day. Immediate alerts are independent of this schedule.

When OpenAI is configured, requests outside the deterministic command set use the Responses API with `store: false`. Echo keeps a bounded local conversation history and gives the model only fourteen function tools: current status, open alerts, service search, service detail, worker queue, deployment records, project listing, estate coverage gaps, joined project health, bounded GitHub estate health, one live read-only GitHub repository status, safe recent activity, recent incident search, and incident detail. Tool outputs, duration, success or failure, and the final reply are recorded locally for review under **Assistant**. There is no shell, SSH, general web, code execution, deployment mutation, or other write tool.

Service detail includes observer freshness, evidence age, alert fingerprints and times, plus a strict allowlist of useful operational counters such as disk, memory, load, HTTP latency, container lifecycle, health, restarts, and heartbeat policy. Arbitrary detail keys and free-form failure text are never sent to the model, so a custom observer cannot smuggle credentials or unrestricted logs through monitoring evidence. Evidence objects above 64 KiB are omitted. Tool collections are bounded and declare when results were truncated.

Supported requests include:

- `brief`, `daily brief`, or “good morning” for runtime health, monitoring coverage, open alerts and incidents, deployments needing attention, and bounded GitHub workflow health
- `status` or “how are things?” for the estate summary
- `coverage`, `setup status`, or “what isn't monitored?” for projects missing repository, group, service, heartbeat-policy, or fresh observer coverage
- `activity`, `audit`, or “what happened?” for recent action/outcome/resource/time audit fields without actors or metadata
- `alerts` or “anything wrong?” for open runtime conditions
- `services` for the operations catalogue
- `service <name-or-key>` for a service's open alerts, latest observer evidence, and whether that evidence is fresh
- `jobs` or “work queue” for queued, running, retrying, and dead worker jobs
- `deployments` or “what is deployed?” for recent dispatch, rollback, and verification state
- `projects` for the project and repository map
- `project <name-or-key>` for one joined view of repositories, service freshness, open alerts, recent incidents, and deployments
- `github status` for current default-branch workflow health across up to 20 enabled repositories
- `repo <name>` for the configured default-branch head and up to ten recent GitHub Actions runs
- `incidents` for a bounded recent list with IDs, project, severity, status, and summary
- `incident <id>` for the report, latest diagnosis, bounded change/PR state, and deployment verification that followed
- `help` for the current command summary

Replies are capped for WhatsApp readability. The control plane remains the complete record.

When an accepted group mention contains a PNG, JPEG, or WEBP screenshot no larger than 5 MiB, Echo verifies the downloaded bytes, stores the image and SHA-256 hash with the incident, and supplies at most two screenshots to the configured vision-capable model at high detail. Image contents remain untrusted evidence and never authorize an action. The incident view exposes only attachment metadata, not a public media URL. Unsupported image formats, oversized or mismatched images, and failed downloads are omitted and recorded as categorized audit evidence. Retained attachment records and bytes are pruned after `ECHO_ATTACHMENT_RETENTION` (30 days by default). PDFs and other document types remain disabled until their separate processing path is configured safely.

Duplicate WhatsApp message IDs are idempotent and do not run tools or send another reply. Stateless Responses tool loops are capped at six rounds and eight calls. Echo preserves typed response items within the current loop, sanitizes tool errors before returning them to the model, and falls back to deterministic guidance when the Responses endpoint is unavailable.

Local owner messages and replies are retained for `ECHO_ASSISTANT_RETENTION` (30 days by default). `ECHO_ASSISTANT_HISTORY_LIMIT` controls how many recent messages, from 4 to 50, are supplied as conversational context. Expired messages and their tool evidence are pruned during startup.

Questions never authorize mutation. Approval commands remain deliberately narrow: `approve <code>` or `reject <code>` resolves only the pending immutable action bound to that one-time code. An unrecognized request cannot run a shell command, contact a production host, execute repository code, or infer permission to change infrastructure.

Live repository inspection is restricted to repositories already enabled in Echo. A short name that matches more than one configured repository produces a clarification list instead of guessing. The GitHub call reads only the configured default-branch head and recent Actions metadata, uses a 15-second investigation deadline, and never downloads or executes repository code.

The `status`, `alerts`, and `jobs` commands include persistent worker-heartbeat and stalled-queue state plus pending and dead WhatsApp delivery counts. `status` and `alerts` also report Echo's supervised runtime components, including a missing, stopped, or retrying worker, watchdog, or enabled WhatsApp gateway. A component's first failure proactively sends one fixed, safe owner message; repeated restart failures are deduplicated, and 30 seconds of stable execution sends one recovery. If the gateway itself is unavailable, those lifecycle messages wait in the durable outbox. Only fixed component names and state labels are exposed; raw internal errors remain in server logs. A queue stall is reported only after ready work exceeds its configured grace period without a fresh worker heartbeat. Worker errors sent to the model are reduced to safe categories; raw job payloads and failure text are never included. The same state is visible on Overview and System, which remains useful when WhatsApp itself cannot deliver the warning. The deterministic commands remain available when AI is disabled or unavailable. Both paths preserve the separate exact-action approval boundary.
