# WhatsApp operator assistant

The configured owner can message Echo directly for a read-only operational view. Echo authorizes the sender against the selected owner contact, reads only its structured MariaDB state, and audits the interpreted intent without storing the question body in the audit event.

When OpenAI is configured, requests outside the deterministic command set use the Responses API with `store: false`. Echo keeps a bounded local conversation history and gives the model only six function tools: current status, open alerts, service search, service detail, project/repository listing, and incident detail. Tool outputs, duration, success or failure, and the final reply are recorded locally for review under **Assistant**. There is no shell, SSH, web, code execution, deployment, or mutation tool.

Service detail includes observer freshness, evidence age, alert fingerprints and times, plus a strict allowlist of useful operational counters such as disk, memory, load, HTTP latency, container lifecycle, health, restarts, and heartbeat policy. Arbitrary detail keys and free-form failure text are never sent to the model, so a custom observer cannot smuggle credentials or unrestricted logs through monitoring evidence. Evidence objects above 64 KiB are omitted. Tool collections are bounded and declare when results were truncated.

Supported requests include:

- `status` or “how are things?” for the estate summary
- `alerts` or “anything wrong?” for open runtime conditions
- `services` for the operations catalogue
- `service <name-or-key>` for a service's open alerts, latest observer evidence, and whether that evidence is fresh
- `projects` for the project and repository map
- `incident <id>` for the incident state and latest diagnosis
- `help` for the current command summary

Replies are capped for WhatsApp readability. The control plane remains the complete record.

Duplicate WhatsApp message IDs are idempotent and do not run tools or send another reply. Stateless Responses tool loops are capped at six rounds and eight calls. Echo preserves typed response items within the current loop, sanitizes tool errors before returning them to the model, and falls back to deterministic guidance when the Responses endpoint is unavailable.

Local owner messages and replies are retained for `ECHO_ASSISTANT_RETENTION` (30 days by default). `ECHO_ASSISTANT_HISTORY_LIMIT` controls how many recent messages, from 4 to 50, are supplied as conversational context. Expired messages and their tool evidence are pruned during startup.

Questions never authorize mutation. Approval commands remain deliberately narrow: `approve <code>` or `reject <code>` resolves only the pending immutable action bound to that one-time code. An unrecognized request cannot run a shell command, contact a production host, execute repository code, or infer permission to change infrastructure.

The `status` command and its natural-language equivalent include pending and dead WhatsApp delivery counts. The same counts are visible on Overview and System, which remains useful when WhatsApp itself cannot deliver the warning. The deterministic commands remain available when AI is disabled or unavailable. Both paths preserve the separate exact-action approval boundary.
