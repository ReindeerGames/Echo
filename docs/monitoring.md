# Service monitoring

Echo accepts normalized health signals from small, read-only observers. Observers do not receive GitHub, WhatsApp, database, or deployment credentials and do not expose a Docker socket to Echo. Built-in probes cover HTTPS, TCP listener reachability, bounded host resources, exact-name Linux process health, sanitized Docker state, and metadata-only artifact freshness.

Echo includes `echo-observer`, a small health-endpoint and Linux host-resource observer with a signed event forwarder. Build it with `make build`; its only secret is the monitor webhook secret supplied as `ECHO_OBSERVER_WEBHOOK_SECRET`.

## Configure the catalogue

Open **Monitoring → Add service** and map each runtime workload to its Echo project. Use a stable key such as `nudgie-web`; the returned service ID is the identifier observers send. Set **Heartbeat timeout** to at least twice the observer interval (for example, 120 seconds for a 30-second observer). Use `0` only for services where missing signals must not alert.

Set `ECHO_MONITOR_WEBHOOK_SECRET` to a random value of at least 32 characters. Leaving it empty disables the public monitoring endpoint.

## Service topology

Open a service in **Monitoring** to declare what it directly depends on and which services depend on it. Relationships are directed, remain inside one project, and use a bounded type (`runtime`, `data`, `messaging`, `network`, or `other`) plus optional short context. Echo shows them in service detail and prioritizes them in investigations, but still requires alerts, signals, incidents, deployments, or repository evidence before suggesting a cause. A declared dependency does not make a remote request or grant Echo access to either workload.

Keep the graph limited to direct operational dependencies and remove stale relationships promptly. A service with dependency relationships cannot be moved to another project until those relationships are removed, which prevents a project edit from silently leaking another project's catalogue into an investigation.

## Service repository ownership

Open a service in **Monitoring** and select **Link repository** to declare which code repositories directly build or operate that workload. Links are restricted to the service's project. Once at least one link exists, `investigate <service>` inspects only the linked repositories that are enabled; link every application, worker, infrastructure, or deployment repository that is genuinely part of that service.

Services created before this feature remain useful: with no explicit links, Echo visibly falls back to all enabled repositories in the project. Treat that as a compatibility state, not the preferred steady state, because a broad project can introduce unrelated commits or workflow failures into a diagnosis. A linked service or repository cannot move to another project until its links are removed. Disabling a linked repository preserves the ownership record but excludes that repository from live investigation.

The Monitoring catalogue and owner `status`, `coverage`, and daily-brief replies report ownership as **explicit**, **project fallback**, or **unavailable**. Unavailable means that links exist but every linked repository is disabled, so Echo deliberately does not widen the investigation back to the project. Treat fallback and unavailable counts as setup work: select the service to add or repair its repository links.

## Service runbooks

Open a service in **Monitoring** and select **Add runbook** to record a concise operational check and, optionally, one HTTPS documentation reference. A service may have at most twenty runbooks. Titles are limited to 120 characters, guidance to 4,000 characters, and references cannot contain credentials, query strings, or fragments. Never store passwords, API tokens, private keys, signed URLs, raw logs, or customer data in a runbook.

Runbooks appear in `service <name>` and correlated investigations so Echo can answer what to check next. The assistant receives at most ten, common secret patterns are redacted first, and every runbook is explicitly labelled untrusted reference material. Echo never follows embedded instructions, runs a command, contacts a host, or claims a check was performed merely because a runbook says to do it. Keep guidance current, observable, and phrased as a suggested verification rather than a conclusion.

## Event contract

Send `POST /webhooks/monitor` with `Content-Type: application/json` and an `X-Echo-Signature-256` header containing `sha256=<hex HMAC-SHA256 of the exact request body>`.

```json
{
  "eventId": "observer-01:container:nudgie-web:1730275200",
  "serviceId": "svc_0123456789abcdef01234567",
  "source": "docker-observer",
  "status": "unhealthy",
  "severity": "critical",
  "summary": "Container health check failed three times",
  "fingerprint": "container-health",
  "observedAt": "2026-10-30T08:00:00Z",
  "details": {
    "container": "nudgie-web",
    "health": "unhealthy",
    "restarts": 4
  }
}
```

Allowed statuses are `healthy`, `degraded`, `unhealthy`, and `unknown`. Allowed severities are `info`, `warning`, and `critical`. A stable fingerprint should identify the condition, not a particular metric value. Echo generates a stable fallback when a failing event omits one. `echo-watchdog` is reserved as an internal source and is rejected at the signed webhook.

`eventId` is an idempotency key. Retrying the exact event does not create another signal or notification. A degraded or unhealthy signal opens or updates its fingerprinted alert. A healthy signal that repeats a fingerprint resolves only that older condition; an empty fingerprint retains the aggregate-probe behavior and resolves all older conditions for the service. Delayed evidence cannot resolve a newer alert. The bundled observer always supplies one stable fingerprint per aggregate probe so an HTTP recovery cannot accidentally clear an unrelated host, container, or backup condition.

## Missing observer detection

Echo's internal watchdog checks heartbeat-enabled services every `ECHO_MONITOR_WATCHDOG_INTERVAL` (30 seconds by default). If no non-watchdog signal arrives within the service's timeout, Echo opens an `observer-silent` alert. High and critical services produce a critical alert; low and medium services produce a warning. Any newer accepted observer event resolves the silence condition even when it reports that the workload is still unhealthy; that workload condition remains open under its own fingerprint. A delayed event cannot clear newer silence, and a second silence period reopens the same alert without notification noise.

The first timeout starts after the service policy is saved, giving a newly configured observer a full grace window. Watchdog events never count as observer heartbeats. Disabling the heartbeat policy or service resolves only the silence alert; it does not clear unrelated application-health alerts.

## Planned maintenance

The authenticated Monitoring screen can schedule a maintenance window for one enabled service. Start and end are converted from the browser's local time to UTC, the reason is bounded to 300 characters, duration must be between one minute and 30 days, the start must be within 365 days, and windows for the same service may not overlap. Creation and cancellation are audited.

Maintenance never disables the webhook, observer, heartbeat watchdog, evidence retention, or alert lifecycle. Events and open alerts remain visible. Only owner WhatsApp notifications for that service's alert transitions are suppressed while the server clock is inside the exact window. The active window and affected alerts are labelled in the control plane and the `status`, `alerts`, `maintenance`, `service`, and AI investigation paths.

When the window expires, the supervised watchdog clears suppression and sends one `MAINTENANCE ENDED` notification if the condition persists; it does not need to wait for another observer event. A healthy observation inside the window clears a maintenance-born condition without sending noise. Cancelling a current window atomically clears suppression and immediately notifies once for each still-open condition. An expired window cannot be cancelled, and a cancelled window cannot be reused.

## Acknowledgement and reminders

An unacknowledged critical alert that remains open is reminded at `ECHO_MONITOR_ALERT_REMINDER_INTERVAL` (four hours by default, configurable from 15 minutes to seven days; `0` disables reminders). Reminder selection, the updated counter, and its durable outbox message commit in one transaction and use row locking, so concurrent watchdog replicas cannot send the same reminder.

Each critical notification contains an exact 12-hex `ack <code>` command. Only the configured WhatsApp owner can use it, it resolves to one currently open alert, and it is handled deterministically outside the AI tools. The Monitoring screen provides the same acknowledgement and a reversible **Clear acknowledgement** control. Reopening a resolved alert or escalation to critical clears the old acknowledgement. Acknowledgement pauses notification reminders only; it never stops probes, evidence, alert updates, or recovery messages.

The body is limited to one MiB. Unknown and disabled services are rejected. Details are retained as evidence and must never contain credentials or unrestricted log dumps.

## Retention

`ECHO_MONITOR_RETENTION` controls retained superseded signal history and defaults to 90 days. It accepts a Go duration between 24 hours and 365 days. Echo prunes in batches at startup and daily, but always preserves the newest non-watchdog observation for every service and every event currently referenced by an open or resolved alert. Freshness checks and alert evidence therefore remain valid even when the rest of a service's older timeline expires. Retention uses the time Echo received the signed event, so a delayed observation still receives the configured local retention period. Maintenance is a supervised readiness component: repeated failures open one deduplicated owner alert instead of silently allowing unbounded growth.

## Notification policy

Echo queues a private WhatsApp update to the configured owner when an alert opens, reopens, escalates from warning to critical, or resolves. Transitions of the same kind arriving within an eight-second window are coalesced transactionally into one bounded digest, so one observer or shared dependency cannot produce one message per affected service. The digest retains each returned service, severity, fingerprint, safe summary, exact critical-alert acknowledgement code, and copyable `service <name>` and `investigate <name>` replies. It includes at most ten full items and 3,500 characters, then directs the owner to **Monitoring** for the remaining independently persisted transitions. Different lifecycle kinds remain separate, and claiming a digest closes its batch key so later transitions start a new delivery rather than mutating an in-flight message.

The alert transition and its durable digest update commit in the same MariaDB transaction, so a crash cannot preserve one while losing the other. The first item delays delivery by only eight seconds; later items do not extend the deadline. Repeated observations at the same severity update `lastSeenAt` without generating noise, and a downgrade remains quiet. All accepted events and alert state are visible in **Monitoring**.

Select any service, alert, or recent signal in **Monitoring** to open the service evidence view. It shows heartbeat freshness, open-alert timing and fingerprints, and the ten most recent normalized evidence payloads. **Edit service policy** updates the existing catalogue entry without replacing its identity, so observer service IDs and alert history remain stable. Disabling a service stops new intake and resolves only its observer-silent policy alert; historical workload alerts and signals remain available.

## Observer boundary

Run observers beside workloads with only the narrow read permissions required to collect health data. Prefer HTTP health endpoints and metrics exporters. If Docker metadata is required, use a constrained socket proxy or a dedicated observer rather than mounting the engine socket into Echo.

The bundled observer accepts a JSON configuration such as [`deploy/observer/echo-observer.example.json`](../deploy/observer/echo-observer.example.json). HTTP probes permit only `GET` and `HEAD`, reject URL credentials and query strings, refuse redirects, ignore environment HTTP proxies, require HTTPS with TLS 1.2 or newer unless `allowInsecureHttp` is explicitly enabled, never record response bodies, and retry the same signed event ID after delivery failures.

`tcpProbes` verify that one configured hostname or IP address accepts a TCP connection on a port from 1 to 65535. The observer closes immediately after the handshake and never sends a protocol preface, credentials, query, or other application bytes. Evidence contains the safe probe name, port, connection latency, and a fixed `timeout` or `connection_failed` category; it deliberately omits the host and raw network error. Reachability proves only that a listener accepted the connection—not that MariaDB, Redis, SMTP, a queue, or another protocol is authenticated or operational—so use an application health endpoint when deeper semantics are required.

Every successful HTTPS response also checks the verified leaf certificate's expiry time. `tlsWarningDays` defaults to 30 and produces a degraded warning; `tlsCriticalDays` defaults to 7 and produces a critical unhealthy signal. The critical threshold must be lower than the warning threshold, and both are bounded to 1–365 days. Certificate evidence contains only the expiry time, rounded days remaining, and configured thresholds—never certificate subjects, issuers, SANs, or chain material. HTTP response and certificate failures share the probe's aggregate `http-health` fingerprint, preventing overlapping stale alerts; a fully healthy response after renewal resolves it normally.

On Linux, `systemProbes` read only `/proc/meminfo`, `/proc/loadavg`, `/proc/uptime`, and filesystem capacity counters for one absolute `diskPath`. They report disk and memory utilization, one/five/fifteen-minute load, load per logical CPU, and uptime—never process arguments, environment variables, file contents, logs, usernames, or network connections. Warning and critical thresholds are explicit in the configuration; defaults are 85/95 percent for disk and memory and 1/2 load per CPU.

`processProbes` cover host-native daemons and workers that are not represented by a Docker container. The observer requests one exact Linux `/proc/*/comm` name of at most 15 safe ASCII characters from the separate loopback-only `echo-process-proxy`; that proxy accepts only names in `ECHO_PROCESS_PROXY_PROCESSES` and scans at most 131,072 process-directory entries. It reports only aggregate process, running, sleeping, stopped, zombie, unknown-state, thread, and resident-memory counts. It never reads or returns PIDs, command lines, arguments, environment variables, executable paths, open files, sockets, user IDs, usernames, logs, or process output, and it never invokes a command or shell.

`minimumCount` defaults to one; `maximumCount` of zero disables the upper bound. A missing process or any zombie is critical, excess count and stopped/unknown state are warnings, and optional ordered `rssWarningMiB`/`rssCriticalMiB` thresholds classify aggregate resident memory. Both RSS thresholds must be zero or both configured. Run the proxy in the intended process namespace. The observer systemd unit keeps `ProtectProc=invisible`; the proxy uses `ProtectProc=default` but runs unprivileged, has no Echo secrets, and is restricted to loopback networking. A host-wide `hidepid` policy may require deliberately scoped process-metadata visibility for the proxy account. Do not weaken `/proc` privacy globally for Echo.

`freshnessProbes` monitor scheduled backups or other artifacts using filesystem metadata only. `path` must name one exact absolute regular file or directory and cannot be `/`; symlinks and special files are rejected. For a directory, the observer examines at most 1,000 direct entries without recursion, ignores subdirectories and symlinks, optionally filters regular files by `matchSuffix`, and uses the newest matching modification time. It never opens a matching artifact, reads its contents, follows a symlink, or reports its path or filename. Missing paths, no matching files, inaccessible metadata, unsupported file types, and an over-limit directory produce fixed safe failure categories rather than raw operating-system errors.

`warningAge` and `criticalAge` are Go duration strings between one minute and 365 days, with the critical age strictly greater than the warning age. They default to `26h` and `50h`, which suits a daily backup with scheduling margin; tune both explicitly for hourly, weekly, or irregular jobs. A timestamp more than five minutes in the future opens a warning because it usually indicates clock or metadata trouble. Create a dedicated Echo service such as `database-backup` for each freshness probe so its heartbeat and alert lifecycle stay distinct from the host or application.

Run a system probe as a host service when host metrics are intended. If it runs inside a container, its kernel and filesystem namespace determine what it measures; do not assume containerized readings represent the host. The observer requires no root privileges, shell, Docker socket, or SSH credential.

Each Echo service may have one aggregate probe across the HTTP, TCP, system, process, Docker, and freshness probe classes. Use separate service-catalogue entries for the host, each native process, each listener, each container, each backup stream, and each application health endpoint. Each built-in probe retains its stable fingerprint on healthy evidence, allowing recovery to resolve that probe's lifecycle only. Expose an aggregate readiness endpoint when one application depends on several internal checks.

`dockerProbes` inspect the lifecycle of one explicitly named container through the bundled `echo-docker-proxy`. Echo records only the normalized running/paused/restarting/dead/OOM state, exit code, Docker health status and failing streak, restart count, and sanitized timestamps. Optional `cpuWarningPercent`/`cpuCriticalPercent` and `memoryWarningPercent`/`memoryCriticalPercent` pairs enable a second bounded stats request for a running container. The proxy reduces that response to CPU percentage, cache-adjusted memory usage/limit/percentage, and the current task count; it discards per-interface network data, block-device data, raw cgroup counters, and every unrecognized field. CPU percentage follows Docker's multi-CPU convention and can exceed 100, so CPU thresholds are bounded at 10,000 while memory thresholds are bounded at 100. Each pair must be either zero (disabled) or positive and strictly ordered.

The proxy also discards environment variables, labels, mounts, logs, health-check output, image configuration, and raw daemon errors before the observer sees a response. Both components cap upstream responses at one MiB or less and never copy unrecognized state or error text into alerts. Resource collection is opt-in so an existing lifecycle-only probe remains compatible and does not create an alert when stats were not requested. Once thresholds are configured, missing or malformed resource evidence is an alertable monitoring failure rather than a false healthy result.

Never give Echo or `echo-observer` the Docker socket. `echo-docker-proxy` is the only component that joins the Docker group. It binds only to a numeric loopback address, implements only its health endpoint plus sanitized `GET /containers/{name}/json` and `GET /containers/{name}/stats` routes, and makes one corresponding upstream read only for exact allowlisted names. It does not forward arbitrary Docker API calls. Plaintext Docker proxy URLs are accepted automatically only on `localhost` or a loopback IP; a non-loopback proxy requires HTTPS unless `allowInsecureHttp` is explicitly enabled for an isolated test environment. Do not substitute a broadly capable Docker socket proxy or expose this loopback service on a LAN or public interface.

Build and configure the proxy beside Docker:

```bash
make build
install -o root -g root -m 0755 bin/echo-docker-proxy /usr/local/bin/echo-docker-proxy
install -o root -g root -m 0644 deploy/observer/echo-docker-proxy.service /etc/systemd/system/echo-docker-proxy.service
install -o root -g root -m 0644 deploy/observer/echo-docker-proxy.env.example /etc/echo-docker-proxy.env
```

Set `ECHO_DOCKER_PROXY_CONTAINERS` in `/etc/echo-docker-proxy.env` to the comma-separated exact names used by `dockerProbes`; no wildcard is supported. Create a locked `echo-docker-proxy` system account, make it a supplementary member of the local Docker group through the unit, then start the proxy. Keep `ECHO_DOCKER_PROXY_LISTEN` at `127.0.0.1:23750`. The optional `ECHO_DOCKER_SOCKET` must be an absolute local Unix socket path.

Build and configure the process proxy beside host-native workloads:

```bash
make build
install -o root -g root -m 0755 bin/echo-process-proxy /usr/local/bin/echo-process-proxy
install -o root -g root -m 0644 deploy/observer/echo-process-proxy.service /etc/systemd/system/echo-process-proxy.service
install -o root -g root -m 0644 deploy/observer/echo-process-proxy.env.example /etc/echo-process-proxy.env
```

Set `ECHO_PROCESS_PROXY_PROCESSES` to the comma-separated exact names used by `processProbes`; no wildcard is supported. Create a locked `echo-process-proxy` system account and start the proxy with `ECHO_PROCESS_PROXY_LISTEN` kept at `127.0.0.1:23751`. The proxy account needs only the process metadata visibility required by local policy and must not receive the observer webhook secret or any Echo, GitHub, WhatsApp, AI, Docker, or application credential.

Run continuously:

```bash
ECHO_OBSERVER_WEBHOOK_SECRET='use-the-same-32-character-or-longer-secret' \
  ./bin/echo-observer -config /etc/echo-observer.json
```

Validate an installation in three explicit stages:

```bash
# Parse and fully validate the configuration. No probes or deliveries occur.
./bin/echo-observer -config /etc/echo-observer.json -check

# Execute every probe and print only normalized evidence. No secret or delivery.
./bin/echo-observer -config /etc/echo-observer.json -dry-run

# After reviewing the output, submit one signed cycle to Echo.
ECHO_OBSERVER_WEBHOOK_SECRET='use-the-same-32-character-or-longer-secret' \
  ./bin/echo-observer -config /etc/echo-observer.json -once
```

`-dry-run` preserves configuration order, summarizes healthy/degraded/unhealthy counts, and exits nonzero when any probe needs attention, making it suitable for installation automation. Probe failures remain normalized fixed evidence and response bodies or raw errors are never printed. `-check`, `-dry-run`, and `-once` are mutually exclusive. Create the matching services in Echo before the signed `-once` stage. Do not put the webhook secret or health-endpoint credentials in the JSON file; authenticated health endpoints are intentionally not supported by the bundled observer.

For a Linux host installation, [`deploy/observer/echo-observer.service`](../deploy/observer/echo-observer.service), [`deploy/observer/echo-process-proxy.service`](../deploy/observer/echo-process-proxy.service), and [`deploy/observer/echo-docker-proxy.service`](../deploy/observer/echo-docker-proxy.service) provide hardened systemd units. The observer unit also runs the configuration-only check before every start or restart. Create separate locked system accounts, install binaries and JSON configuration as root-owned read-only files, and put only `ECHO_OBSERVER_WEBHOOK_SECRET=...` in `/etc/echo-observer.env` with mode `0600`. The observer account keeps its process table hidden and does not belong to the Docker group; only each small allowlisting proxy receives its narrowly required local read access. Run `-check`, then `-dry-run`, and only then the signed `-once` delivery before enabling the service. Installation is an explicit deployment step; Echo never installs observers or changes a server on its own.
