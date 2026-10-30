# Service monitoring

Echo accepts normalized health signals from small, read-only observers. Observers do not receive GitHub, WhatsApp, database, or deployment credentials and do not expose a Docker socket to Echo.

Echo includes `echo-observer`, a small health-endpoint and Linux host-resource observer with a signed event forwarder. Build it with `make build`; its only secret is the monitor webhook secret supplied as `ECHO_OBSERVER_WEBHOOK_SECRET`.

## Configure the catalogue

Open **Monitoring → Add service** and map each runtime workload to its Echo project. Use a stable key such as `nudgie-web`; the returned service ID is the identifier observers send. Set **Heartbeat timeout** to at least twice the observer interval (for example, 120 seconds for a 30-second observer). Use `0` only for services where missing signals must not alert.

Set `ECHO_MONITOR_WEBHOOK_SECRET` to a random value of at least 32 characters. Leaving it empty disables the public monitoring endpoint.

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

Allowed statuses are `healthy`, `degraded`, `unhealthy`, and `unknown`. Allowed severities are `info`, `warning`, and `critical`. A stable fingerprint should identify the condition, not a particular metric value. Echo generates a stable fallback when a failing event omits one.

`eventId` is an idempotency key. Retrying the exact event does not create another signal or notification. A degraded or unhealthy signal opens or updates its fingerprinted alert. A healthy signal resolves all older open alerts for that service; delayed healthy events cannot resolve a newer outage.

## Missing observer detection

Echo's internal watchdog checks heartbeat-enabled services every `ECHO_MONITOR_WATCHDOG_INTERVAL` (30 seconds by default). If no non-watchdog signal arrives within the service's timeout, Echo opens an `observer-silent` alert. High and critical services produce a critical alert; low and medium services produce a warning. A later healthy observer event resolves it, and a second silence period reopens the same alert without notification noise.

The first timeout starts after the service policy is saved, giving a newly configured observer a full grace window. Watchdog events never count as observer heartbeats. Disabling the heartbeat policy or service resolves only the silence alert; it does not clear unrelated application-health alerts.

The body is limited to one MiB. Unknown and disabled services are rejected. Details are retained as evidence and must never contain credentials or unrestricted log dumps.

## Retention

`ECHO_MONITOR_RETENTION` controls retained superseded signal history and defaults to 90 days. It accepts a Go duration between 24 hours and 365 days. Echo prunes in batches at startup and daily, but always preserves the newest non-watchdog observation for every service and every event currently referenced by an open or resolved alert. Freshness checks and alert evidence therefore remain valid even when the rest of a service's older timeline expires. Retention uses the time Echo received the signed event, so a delayed observation still receives the configured local retention period. Maintenance is a supervised readiness component: repeated failures open one deduplicated owner alert instead of silently allowing unbounded growth.

## Notification policy

Echo queues a private WhatsApp message to the configured owner when an alert opens, reopens, or resolves. Opening and reopening messages include copyable `service <name>` and `investigate <name>` replies for immediate evidence or bounded cross-source correlation; the latter requires configured AI tools. The alert transition and durable WhatsApp outbox item commit in the same MariaDB transaction, so a crash cannot preserve one while losing the other. Repeated failing observations update `lastSeenAt` without generating noise. All accepted events and alert state are visible in **Monitoring**.

Select any service, alert, or recent signal in **Monitoring** to open the service evidence view. It shows heartbeat freshness, open-alert timing and fingerprints, and the ten most recent normalized evidence payloads. **Edit service policy** updates the existing catalogue entry without replacing its identity, so observer service IDs and alert history remain stable. Disabling a service stops new intake and resolves only its observer-silent policy alert; historical workload alerts and signals remain available.

## Observer boundary

Run observers beside workloads with only the narrow read permissions required to collect health data. Prefer HTTP health endpoints and metrics exporters. If Docker metadata is required, use a constrained socket proxy or a dedicated observer rather than mounting the engine socket into Echo.

The bundled observer accepts a JSON configuration such as [`deploy/observer/echo-observer.example.json`](../deploy/observer/echo-observer.example.json). HTTP probes permit only `GET` and `HEAD`, reject URL credentials and query strings, refuse redirects, ignore environment HTTP proxies, require HTTPS with TLS 1.2 or newer unless `allowInsecureHttp` is explicitly enabled, never record response bodies, and retry the same signed event ID after delivery failures.

On Linux, `systemProbes` read only `/proc/meminfo`, `/proc/loadavg`, `/proc/uptime`, and filesystem capacity counters for one absolute `diskPath`. They report disk and memory utilization, one/five/fifteen-minute load, load per logical CPU, and uptime—never process arguments, environment variables, file contents, logs, usernames, or network connections. Warning and critical thresholds are explicit in the configuration; defaults are 85/95 percent for disk and memory and 1/2 load per CPU.

Run a system probe as a host service when host metrics are intended. If it runs inside a container, its kernel and filesystem namespace determine what it measures; do not assume containerized readings represent the host. The observer requires no root privileges, shell, Docker socket, or SSH credential.

Each Echo service may have one aggregate probe across the HTTP, system, and Docker probe classes. Use separate service-catalogue entries for the host, each container, and each application health endpoint. Expose an aggregate readiness endpoint when one application depends on several internal checks.

`dockerProbes` inspect the lifecycle of one explicitly named container through the bundled `echo-docker-proxy`. Echo records only the normalized running/paused/restarting/dead/OOM state, exit code, Docker health status and failing streak, restart count, and sanitized timestamps. The proxy discards environment variables, labels, mounts, logs, health-check output, image configuration, and raw daemon errors before the observer sees the response. Both processes cap inspect responses at one MiB and never copy unrecognized state text into alerts.

Never give Echo or `echo-observer` the Docker socket. `echo-docker-proxy` is the only component that joins the Docker group. It binds only to a numeric loopback address, implements only its health endpoint and `GET /containers/{name}/json`, and makes a single upstream inspect request only for exact allowlisted names. It does not forward arbitrary Docker API calls. Plaintext Docker proxy URLs are accepted automatically only on `localhost` or a loopback IP; a non-loopback proxy requires HTTPS unless `allowInsecureHttp` is explicitly enabled for an isolated test environment. Do not substitute a broadly capable Docker socket proxy or expose this loopback service on a LAN or public interface.

Build and configure the proxy beside Docker:

```bash
make build
install -o root -g root -m 0755 bin/echo-docker-proxy /usr/local/bin/echo-docker-proxy
install -o root -g root -m 0644 deploy/observer/echo-docker-proxy.service /etc/systemd/system/echo-docker-proxy.service
install -o root -g root -m 0644 deploy/observer/echo-docker-proxy.env.example /etc/echo-docker-proxy.env
```

Set `ECHO_DOCKER_PROXY_CONTAINERS` in `/etc/echo-docker-proxy.env` to the comma-separated exact names used by `dockerProbes`; no wildcard is supported. Create a locked `echo-docker-proxy` system account, make it a supplementary member of the local Docker group through the unit, then start the proxy. Keep `ECHO_DOCKER_PROXY_LISTEN` at `127.0.0.1:23750`. The optional `ECHO_DOCKER_SOCKET` must be an absolute local Unix socket path.

Run continuously:

```bash
ECHO_OBSERVER_WEBHOOK_SECRET='use-the-same-32-character-or-longer-secret' \
  ./bin/echo-observer -config /etc/echo-observer.json
```

Use `-once` for installation smoke tests. Create the matching services in Echo before starting the observer. Do not put the webhook secret or health-endpoint credentials in the JSON file; authenticated health endpoints are intentionally not supported by the bundled observer.

For a Linux host installation, [`deploy/observer/echo-observer.service`](../deploy/observer/echo-observer.service) and [`deploy/observer/echo-docker-proxy.service`](../deploy/observer/echo-docker-proxy.service) provide hardened systemd units. Create separate locked system accounts, install binaries and JSON configuration as root-owned read-only files, and put only `ECHO_OBSERVER_WEBHOOK_SECRET=...` in `/etc/echo-observer.env` with mode `0600`. The observer account does not belong to the Docker group; only the small allowlisting proxy may access the socket. Validate the observer with `-once` before enabling it. Installation is an explicit deployment step; Echo never installs observers or changes a server on its own.
