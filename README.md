<p align="center">
  <img src="./Echo-main.png" alt="Echo logo" width="220" />
</p>

# Echo

Echo is a WhatsApp-native SRE assistant for Docker hosts.  
It combines deterministic host telemetry, filtered log triage, and concise operational messaging so you can run incident checks from chat without losing rigor.

## Table of contents

- [Why Echo](#why-echo)
- [Core capabilities](#core-capabilities)
- [How Echo works](#how-echo-works)
- [Command reference](#command-reference)
- [Guarded remediation flow](#guarded-remediation-flow)
- [API endpoints](#api-endpoints)
- [Webhook setup (GoChatAPI)](#webhook-setup-gochatapi)
- [Prerequisites](#prerequisites)
- [Quick start](#quick-start)
- [Testing](#testing)
- [Container runtime](#container-runtime)
- [Environment variables](#environment-variables)
- [Runtime configuration (`config/echo.json`)](#runtime-configuration-configechojson)
- [Data storage](#data-storage)
- [Safety model](#safety-model)
- [Operational notes](#operational-notes)
- [Development](#development)
- [CI/CD](#cicd)
- [Troubleshooting](#troubleshooting)
- [Project structure](#project-structure)
- [License](#license)

## Why Echo

Most chat assistants are good at wording but weak on operational grounding. Echo is built for the opposite:

- deterministic checks drive issue detection
- AI is used for summarization, not invented telemetry
- safety controls are explicit for remediation actions
- responses are concise and WhatsApp-friendly

## Core capabilities

- Monitor container state and health across the host.
- Detect deterministic issues: exited/dead, restarting, unhealthy, high CPU, high memory.
- Group containers and detect outliers by service baseline.
- Analyze change signals between snapshots.
- Triage filtered logs (deduped/truncated/scored) without dumping raw log streams.
- Troubleshoot a specific container end-to-end (state + issues + log signals + change context).
- Run guarded remediation (`start` / `restart`) with explicit two-step confirmation.
- Track AI token usage and estimated USD costs.
- Persist snapshots, anomalies, issues, queries, and usage events in SQLite.

## How Echo works

1. Receives WhatsApp webhooks from GoChatAPI.
2. Validates sender allowlist and deduplicates repeated inbound messages.
3. Captures a fresh Docker estate snapshot (except for identity/usage-only requests).
4. Routes the message to an intent handler (report, priority, logs, troubleshoot, remediation, etc.).
5. Optionally asks OpenAI to summarize verified facts in a natural operator-facing response.
6. Returns a plain-text WhatsApp reply with a bold title and concise body.

## Command reference

| Intent | Example messages |
|---|---|
| Identity/help | `help`, `who are you`, `what can you do` |
| Full report | `full report`, `report`, `overview`, `summary` |
| Priority queue | `priority`, `critical`, `urgent`, `sev1`, `p1` |
| Top resources | `top cpu`, `top memory`, `top mem` |
| Group check | `group wordpress`, `check group api`, `group:edge` |
| Container check | `check homepage`, `status db`, `container-name status` |
| Logs triage | `logs homepage`, `log db` |
| Change detection | `what changed`, `delta`, `changes since` |
| Troubleshooting | `troubleshoot homepage`, `investigate db`, `debug api` |
| Guarded remediation | `restart homepage`, `start worker` |
| Confirm remediation | `confirm <code>` |
| Cancel remediation | `cancel`, `abort`, `stop` |
| AI usage summary | `usage`, `usage daily`, `usage weekly`, `usage monthly` |

Follow-up context is supported for natural chat continuity, for example:
- `priority`
- `troubleshoot it`
- `restart it`

## Guarded remediation flow

1. Request action with `restart <container>` or `start <container>`.
2. Echo prepares a guarded plan and returns a short confirmation code.
3. Echo performs no change yet.
4. You confirm explicitly with `confirm <code>` within the configured TTL.
5. Echo executes the action and returns post-action verification.
6. `cancel` aborts any pending guarded action.

## API endpoints

- `GET /health`  
  Returns service liveness for local checks.

- `POST /webhook`  
  Receives GoChatAPI inbound message events.

## Webhook setup (GoChatAPI)

Configure your GoChatAPI instance to send inbound messages to:

- `POST https://<your-host>/webhook`

Recommended setup checks:

- Ensure the webhook can reach your Echo host from the internet or your VPN.
- Ensure the sender phone number exists in `WA_ALLOWED_NUMBERS`.
- Set a shared webhook secret in both GoChat and Echo (`WA_WEBHOOK_SECRET`).
- Send a test WhatsApp message and confirm Echo logs receipt.

Payload contract for shared-secret validation:

```json
{
  "event_type": "message_received",
  "webhook_secret": "YOUR_SHARED_SECRET",
  "data": {
    "...": "..."
  }
}
```

## Prerequisites

- Node.js `>=20`
- Docker Engine available on the host
- Access to Docker socket (default `/var/run/docker.sock`)
- GoChatAPI instance and token
- OpenAI API key (optional but recommended for higher-quality summaries)

## Quick start

```bash
cp .env.example .env
npm install
npm start
```

Default service port is `3000`.

## Testing

```bash
npm run check
npm test
```

The test suite uses Node's built-in test runner and covers:

- intent routing and AI fallback behavior
- webhook validation and secret enforcement
- deterministic issue and change detection
- grouping, outlier detection, and log filtering
- SQLite-backed state persistence and usage aggregation

## Container runtime

### Build and run with Docker

```bash
docker build -t echo-sre-assistant:local .
docker run --rm -p 3000:3000 --env-file .env \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v echo_data:/app/data \
  echo-sre-assistant:local
```

### Run with Docker Compose

```bash
docker compose up --build -d
```

Compose file included: `docker-compose.yml`  
Docker build context controls included files via `.dockerignore`.

## Environment variables

Copy `.env.example` and configure the following:

| Variable | Required | Default | Notes |
|---|---|---|---|
| `OPENAI_API_KEY` | No | - | If omitted, Echo falls back to deterministic non-AI responses. |
| `OPENAI_MODEL` | No | `gpt-4o-mini` | Model for summarization. |
| `AI_RATE_GPT4O_MINI_INPUT_PER_M` | No | `0.15` | USD per 1M input tokens. |
| `AI_RATE_GPT4O_MINI_CACHED_INPUT_PER_M` | No | `0.075` | USD per 1M cached input tokens. |
| `AI_RATE_GPT4O_MINI_OUTPUT_PER_M` | No | `0.60` | USD per 1M output tokens. |
| `WA_BASE_URL` | Yes | `https://api.gochatapi.com` | GoChatAPI base URL. |
| `WA_INSTANCE` | Yes | - | GoChat instance ID. |
| `WA_TOKEN` | Yes | - | GoChat access token. |
| `WA_ALLOWED_NUMBERS` | Yes | - | Comma-separated phone numbers (digits only). |
| `WA_WEBHOOK_SECRET` | No | - | If set, Echo requires exact match against top-level `webhook_secret` in every incoming webhook payload. |
| `WA_STATUS_RETRIES` | No | `3` | Startup WhatsApp status retries. |
| `WA_STATUS_TIMEOUT_MS` | No | `10000` | Startup status request timeout. |
| `WA_STATUS_RETRY_DELAY_MS` | No | `1200` | Delay between status retries. |
| `WA_SEND_TIMEOUT_MS` | No | `30000` | Outbound send timeout. |
| `PORT` | No | `3000` | HTTP server port. |
| `DOCKER_SOCKET` | No | `/var/run/docker.sock` | Docker socket path. |

## Runtime configuration (`config/echo.json`)

`config/echo.json` controls thresholds and feature toggles.

- `thresholds`  
  CPU/memory limits, change thresholds, outlier sensitivity, and log filtering limits.
- `grouping.overrides.byName/byImage`  
  Service grouping rules for estate-level analysis.
- `features.aiSummaries`  
  Enables/disables AI summarization.
- `features.scheduler`  
  Enables/disables scheduled background snapshots.
- `features.skillDrafting`  
  Enables repeated-query skill draft generation.
- `features.guardedRemediation`  
  Enables guarded `start/restart` workflow.
- `scheduler.cron`  
  Cron expression for background scans.
- `remediation.confirmationTtlSeconds`  
  Confirmation window for guarded actions.
- `remediation.restartTimeoutSeconds`  
  Docker restart timeout passed to the restart operation.

## Data storage

Echo stores state in SQLite at `data/echo.db`:

- `snapshots` (container metrics/state over time)
- `issues` (deterministic findings)
- `anomalies` (outlier detections)
- `recent_queries` (inbound prompts and responses)
- `skill_proposals` (draft skills from repeated workflows)
- `ai_pricing` (rate-card metadata)
- `ai_usage_events` (token/cost usage events)

## Safety model

- Deterministic telemetry is primary; AI is secondary summarization.
- No secret/token echoing in responses.
- No raw log dumps; only filtered signal summaries.
- Guarded remediation always requires explicit confirmation.
- Pending confirmation windows expire automatically.

## Operational notes

- Echo acknowledges webhook requests quickly, then processes asynchronously.
- Duplicate inbound messages are ignored within a short time window.
- Startup runs an immediate estate snapshot and optional WhatsApp status check.
- If scheduler is enabled, periodic scans run using the configured cron expression.

## Development

```bash
# Run with file watching
npm run dev

# Syntax check (entrypoint parse check)
npm run check

# Run full tests
npm test
```

## CI/CD

GitHub Actions workflow included at:

- `.github/workflows/docker-publish.yml`

Behavior:

- runs `npm run check` and `npm test`
- builds Docker image from `Dockerfile`
- publishes image to GHCR (`ghcr.io/<owner>/<repo>`) on:
  - pushes to `main`
  - version tags matching `v*`
  - manual `workflow_dispatch`

## Troubleshooting

| Symptom | Checks |
|---|---|
| No WhatsApp replies | Verify `WA_BASE_URL`, `WA_INSTANCE`, `WA_TOKEN`; verify sender is in `WA_ALLOWED_NUMBERS`; verify GoChat webhook targets `POST /webhook`. |
| Webhook requests are ignored with secret errors | Verify GoChat sends top-level `webhook_secret`; verify it matches `WA_WEBHOOK_SECRET` exactly. |
| No Docker data in responses | Verify Docker is running; verify process permission to access `DOCKER_SOCKET`. |
| AI summaries not appearing | Verify `OPENAI_API_KEY`; verify `features.aiSummaries=true` in `config/echo.json`. |
| Guarded actions unavailable | Verify `features.guardedRemediation=true` in `config/echo.json`. |
| Confirmation code keeps expiring | Increase `remediation.confirmationTtlSeconds` in `config/echo.json`. |

## Project structure

```text
.github/      CI workflows
config/       Runtime configuration
data/         SQLite database files
prompts/      System identity/prompt files
skills/       Skill registry + core and draft skills
src/          Service implementation
tests/        Node test suite
Dockerfile    Container image build definition
.dockerignore Docker build exclusions
docker-compose.yml Local compose runtime
README.md     Project documentation
```

## License

This project is licensed under the MIT License. See `LICENSE`.
