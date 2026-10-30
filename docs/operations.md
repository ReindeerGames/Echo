# Operations runbook

## Health endpoints

- `/healthz`: process liveness
- `/readyz`: core MariaDB reachability and migration readiness

The WhatsApp, AI, GitHub, worker, deployment, and audit states are visible in the authenticated System UI.

The Overview and System screens also show pending and dead WhatsApp outbox counts. A small pending count can be normal while the gateway sends; a growing pending count or any dead message means a notification or assistant reply has not been confirmed after bounded retries. Check the gateway connection and logs before intervening. Echo does not automatically replay dead messages because a network failure can be ambiguous and blind replay could duplicate a message.

For every observer-managed service, configure a heartbeat timeout at least twice its reporting interval. An `observer-silent` alert means Echo is running but has stopped receiving evidence from that service's observer; check the observer process, network path, webhook secret, and the target health endpoint. Do not treat silence as proof that the workload itself is down.

For host-resource monitoring, run `echo-observer` directly under the host's service manager as an unprivileged account. A containerized observer measures the namespaces and filesystem presented to that container. Keep host and application probes as separate Echo services so one healthy signal cannot clear the other's alert.

Container-state probes use the bundled `echo-docker-proxy`, bound to numeric loopback and restricted in code to sanitized container inspection plus an explicit exact-name allowlist. Only its dedicated account belongs to the Docker group; the observer user must not. Treat any broader or remotely reachable plaintext Docker API as a critical security defect, not as a supported Echo configuration. If the proxy is unavailable, Echo raises `container-observer`; do not interpret that as proof the workload is down.

## Backup

Back up `echo_whatsapp` and `echo_core` independently using encrypted storage. The session database contains cryptographic key material and must receive the stricter retention and access policy.

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

Jobs retry with bounded backoff and become `dead` after their configured attempt limit. Deployment records are written in `dispatching` state before GitHub is called and are unique per approval. If a network failure leaves a dispatch ambiguous, Echo will not send it twice: reconcile GitHub using `echo_deployment_id`, then allow the signed webhook to complete the record or create a fresh approval after cancelling the stale action. Before replaying any other mutation, reconcile GitHub for an existing branch or pull request. Do not blindly reset a dead mutation job to queued.

## Dependency upgrades

HyperMeow and `mdbstore` are pinned together. Upgrade them in one pull request, run `go test -race ./...`, run the `mdbstore` MariaDB integration suite, restore a production-like session backup into an isolated database, and exercise pairing, reconnect, group mentions, direct approvals, and message sending before release.
