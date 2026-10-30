# Service monitoring

Echo accepts normalized health signals from small, read-only observers. Observers do not receive GitHub, WhatsApp, database, or deployment credentials and do not expose a Docker socket to Echo.

Echo includes `echo-observer`, a small health-endpoint probe and signed event forwarder. Build it with `make build`; its only secret is the monitor webhook secret supplied as `ECHO_OBSERVER_WEBHOOK_SECRET`.

## Configure the catalogue

Open **Monitoring → Add service** and map each runtime workload to its Echo project. Use a stable key such as `nudgie-web`; the returned service ID is the identifier observers send.

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

The body is limited to one MiB. Unknown and disabled services are rejected. Details are retained as evidence and must never contain credentials or unrestricted log dumps.

## Notification policy

Echo queues a private WhatsApp message to the configured owner when an alert opens, reopens, or resolves. Repeated failing observations update `lastSeenAt` without generating noise. All accepted events and alert state are visible in **Monitoring**.

## Observer boundary

Run observers beside workloads with only the narrow read permissions required to collect health data. Prefer HTTP health endpoints and metrics exporters. If Docker metadata is required, use a constrained socket proxy or a dedicated observer rather than mounting the engine socket into Echo.

The bundled observer accepts a JSON configuration such as [`deploy/observer/echo-observer.example.json`](../deploy/observer/echo-observer.example.json). It permits only `GET` and `HEAD`, rejects URL credentials and query strings, refuses redirects, ignores environment HTTP proxies, requires HTTPS with TLS 1.2 or newer unless `allowInsecureHttp` is explicitly enabled, never records response bodies, and retries the same signed event ID after delivery failures. Each service may have one probe; expose an aggregate readiness endpoint when a service depends on several internal checks.

Run continuously:

```bash
ECHO_OBSERVER_WEBHOOK_SECRET='use-the-same-32-character-or-longer-secret' \
  ./bin/echo-observer -config /etc/echo-observer.json
```

Use `-once` for installation smoke tests. Create the matching services in Echo before starting the observer. Do not put the webhook secret or health-endpoint credentials in the JSON file; authenticated health endpoints are intentionally not supported by the bundled observer.
