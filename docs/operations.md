# Operations runbook

## Health endpoints

- `/healthz`: process liveness only
- `/readyz`: operational readiness across core MariaDB, exact agreement between applied and embedded migrations, and every required supervised runtime loop (worker, worker watchdog, monitor watchdog, and WhatsApp gateway when enabled)

The System view shows each background component's current state and last transition. Echo restarts a component with bounded exponential backoff if it exits unexpectedly; readiness remains unavailable until the component is running again. The first failure opens one durable component alert and privately notifies the owner. Further failures refresh the same alert without message spam. Echo resolves the alert and sends one recovery only after the component has run continuously for 30 seconds; this also reconciles stale alerts after a whole-process restart. If the WhatsApp gateway is the failed component, its notifications remain in the durable outbox and are delivered in order after the gateway recovers. Disabling a component intentionally clears a stale alert. Readiness responses and notifications expose only fixed safe state labels and summaries, while the underlying error is retained in server logs.

The WhatsApp, AI, GitHub, worker, deployment, and audit states are visible in the authenticated System UI.

The supervised GitHub watchdog reads the configured default branch and recent Actions metadata every `ECHO_GITHUB_MONITOR_INTERVAL` (15 minutes by default). A newest failed result opens one persistent repository alert and privately notifies the owner; repeated observations do not spam. A queued/running workflow, an empty result, or a failed GitHub request is inconclusive and cannot erase an earlier failure. Only completed healthy latest results resolve the alert. Open workflow alerts appear above the repository catalogue and in `status` and `alerts`; reply with `repo owner/name` for current evidence. Removing the GitHub connection or disabling the repository explicitly clears its alert as a policy change. The watchdog is read-only and never reruns, cancels, dispatches, or edits a workflow.

The Overview and System screens also show pending and dead WhatsApp outbox counts. A small pending count can be normal while the gateway sends; a growing pending count or any dead message means a notification or assistant reply has not been confirmed after bounded retries. System adds a read-only delivery table containing only direct/group channel type, queue state, attempt count, availability and lease timing, a normalized failure category, and timestamps. It omits message bodies, destination JIDs, WhatsApp message IDs, and raw errors, and intentionally provides no retry button. The owner can request the same privacy-safe evidence with `deliveries`, `outbox`, or “why didn't WhatsApp send?”. Check gateway health and server logs before intervening. A process crash releases an expired delivery lease for automatic recovery using the same deterministic WhatsApp message ID, which lets the recipient deduplicate an ambiguous retry. Echo does not automatically replay messages already marked dead. `ECHO_WHATSAPP_OUTBOX_RETENTION` defaults to 30 days and removes only rows with a confirmed `sent_at` older than the cutoff; queued, sending, retrying, and dead rows remain visible regardless of age.

Overview summarizes queued, running, retrying, and dead worker jobs. System provides the read-only job queue with incident correlation, attempt counts, timestamps, and bounded failure evidence. When ready work exceeds `ECHO_WORKER_QUEUE_STALL_TIMEOUT` and no heartbeat has arrived within `ECHO_WORKER_HEARTBEAT_TIMEOUT`, Echo opens one worker-queue alert and privately notifies the owner; a fresh worker heartbeat resolves it and sends one recovery message. Investigate the worker process and database path before intervening; the UI intentionally has no blind retry control. The owner can request the same safe summary over WhatsApp with `status`, `alerts`, `jobs`, or “work queue”; that path exposes error categories, never raw payloads or failure text.

For planned service work, schedule an exact maintenance window under **Monitoring → Planned maintenance**. Choose only the affected service, use the shortest practical interval, and state the reason. Echo continues ingesting and displaying every signal; only that service's owner alert notifications pause. Overlapping windows are rejected. If an unhealthy condition remains after expiry, the watchdog produces one maintenance-ended notification without waiting for a new observer event. Cancelling an active window restores notifications immediately for every still-open condition. Use `maintenance`, `alerts`, or `service <name>` in WhatsApp to verify the current state. Do not disable the observer as a substitute for maintenance, because that removes the evidence needed to distinguish planned work from monitor failure.

Critical alerts repeat at the bounded `ECHO_MONITOR_ALERT_REMINDER_INTERVAL` until acknowledged or resolved. Reply with the exact `ack <code>` shown in the private notification, or use **Monitoring → Acknowledge**. This records attention and pauses reminders; it does not mark the service healthy. Use **Clear acknowledgement** if an acknowledgement was accidental. Set the interval to `0` only when another paging system owns persistent escalation; opening, escalation, recovery, and maintenance-ended notifications remain enabled.

For every observer-managed service, configure a heartbeat timeout at least twice its reporting interval. An `observer-silent` alert means Echo is running but has stopped receiving evidence from that service's observer; check the observer process, network path, webhook secret, and the target health endpoint. Do not treat silence as proof that the workload itself is down. Any fresh observer result clears the silence lifecycle independently, so an observer can be restored while its separate workload alert correctly remains open.

Record direct runtime, data, messaging, and network dependencies from each service's Monitoring evidence view. Echo prioritizes those declared relationships during an investigation but treats them as operator-maintained context rather than proof of failure propagation. Review the graph when an application is split, retired, or rewired; remove its relationships before moving it to another project.

Link each service to the repositories that directly build or operate it. Explicit ownership keeps commit and workflow correlation focused; without links, Echo reports that it is using every enabled repository in the project as a compatibility fallback. Review links whenever a repository is split, renamed, disabled, transferred, or gains a new workload. Remove links before moving either side to another project, and link all relevant repositories before relying on service-level source correlation.

Maintain short per-service runbooks in the same evidence view for the checks that are specific to that workload: where its safe dashboard lives, which normalized signal should move, or what dependency should be verified next. Use only HTTPS references without query parameters and never paste a credential, signed link, unrestricted log, or customer record. Echo redacts common secret shapes before assistant use, but redaction is a backstop rather than permission to store secrets. Review runbooks whenever ownership, deployment, dependencies, or recovery procedure changes. They guide read-only answers and are never executed automatically.

For host-resource monitoring, run `echo-observer` directly under the host's service manager as an unprivileged account. A containerized observer measures the namespaces and filesystem presented to that container. Keep host and application probes as separate Echo services so one healthy signal cannot clear the other's alert.

Container-state probes use the bundled `echo-docker-proxy`, bound to numeric loopback and restricted in code to sanitized container inspection plus an explicit exact-name allowlist. Only its dedicated account belongs to the Docker group; the observer user must not. Treat any broader or remotely reachable plaintext Docker API as a critical security defect, not as a supported Echo configuration. Proxy availability, container runtime state, and Docker health are one aggregate `container-health` lifecycle whose safe summary identifies the observed failure; a later successful result updates or resolves that same lifecycle without leaving a stale overlapping condition.

## Backup

Back up `echo_whatsapp` and `echo_core` independently using encrypted storage. The session database contains cryptographic key material and must receive the stricter retention and access policy.

Set `ECHO_MONITOR_RETENTION` to the operational history window you actually need (90 days by default, 24 hours to 365 days allowed). Echo's supervised retention component prunes only superseded monitoring events at startup and daily, preserves current freshness evidence and alert-referenced events, and participates in readiness. Database backups remain the recovery boundary for history beyond the live retention window.

Treat backup creation and backup verification as separate controls. An entry under `freshnessProbes` can prove only that a matching artifact's modification time is recent; it does not prove that the artifact is complete, decryptable, or restorable. Keep restore tests in an isolated environment and use the freshness alert as an early signal that the scheduled backup pipeline may have stopped.

At least quarterly, restore both databases to an isolated MariaDB server, start Echo without external egress, and verify schema validation and incident history.

## WhatsApp recovery

If the session is logged out:

1. Disable worker writes and deployment approvals.
2. Open the WhatsApp UI as the owner.
3. Pair the dedicated number with the rotating QR code.
4. Send Echo a direct message from the intended owner, refresh discovered contacts, and reselect the owner if identity mapping changed.
5. Send a mention from a test group and confirm exactly one incident and acknowledgement.

## Emergency stop

Stop the worker process or set `ECHO_WORKER_ENABLED=false`. This preserves report intake, history, and approvals while preventing repository or deployment jobs from executing. Remove the token in Echo and revoke it in GitHub if credential misuse is suspected.

## Failed jobs

Jobs retry with bounded backoff and become `dead` after their configured attempt limit. While an operation runs, the owning worker renews its lease with the exact worker ID and attempt number; losing that fence cancels the operation instead of allowing stale work to continue. For an incident job that fails on its final executed attempt, the dead transition and safe owner notification are committed atomically and fenced by attempt number, so a completion retry cannot send a duplicate. The notification contains a failure category rather than raw dependency error text. A `running` job with an expired lease is reclaimed after a worker crash; an expired final-attempt lease is finalized as `dead` instead of remaining stuck forever and remains visible in the control plane. Deployment records are written in `dispatching` state before GitHub is called and are unique per approval. If a network failure leaves a dispatch ambiguous, Echo will not send it twice: reconcile GitHub using `echo_deployment_id`, then allow the signed webhook to complete the record or create a fresh approval after cancelling the stale action. Before replaying any other mutation, reconcile GitHub for an existing branch or pull request. Do not blindly reset a dead mutation job to queued.

Incident analysis is serialized with a MariaDB named lock per incident. Each retained follow-up increments `evidence_revision`; jobs skip a revision already covered by the newest analysis, and an analysis that finishes after newer evidence arrives is stored for audit but does not become the incident's current owner notification.

## Dependency upgrades

HyperMeow and `mdbstore` are pinned together. Upgrade them in one pull request, run `go test -race ./...`, run the `mdbstore` MariaDB integration suite, restore a production-like session backup into an isolated database, and exercise pairing, reconnect, group mentions, direct approvals, and message sending before release.
