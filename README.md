# Echo v2

Echo turns WhatsApp bug reports into evidence-backed, approval-controlled software changes.

A partner mentions Echo in a configured WhatsApp group. Echo records the report, acknowledges it in the group, inspects only the repositories mapped to that project, and sends the owner a private diagnosis. Code changes, staging deployments, production promotions, and rollbacks each require a separate approval bound to an immutable action.

Echo is designed around four rules:

1. Messages and repository contents are untrusted evidence, never instructions.
2. AI may propose; deterministic policy decides what is permitted.
3. No consequential action inherits approval from an earlier action.
4. Production always requires a separate approval bound to an exact commit; when staging exists, production receives the same artifact staging verified.

## Architecture

Echo is a Go application with three logical trust zones:

- **WhatsApp gateway:** HyperMeow, `mdbstore`, pairing, inbound message normalization, and a durable outbound queue. It never receives GitHub credentials.
- **Control plane:** authenticated API and embedded responsive UI, project routing, policy, approvals, audit, and deployment verification.
- **Workers:** bounded repository downloads, advisory AI analysis, draft pull requests, and approved workflow dispatches.

One externally managed MariaDB server hosts two isolated databases and accounts:

- `echo_whatsapp` is owned by `mdbstore` and contains cryptographic WhatsApp session state.
- `echo_core` contains Echo's projects, incidents, jobs, approvals, deployments, outbox, and audit events.

See [the architecture](docs/architecture.md), [threat model](docs/threat-model.md), [monitoring contract](docs/monitoring.md), [WhatsApp operator guide](docs/whatsapp-operator.md), and [deployment contract](docs/deployment-contract.md).

## Implemented workflow

1. HyperMeow receives a group message with a real mention of Echo's JID.
2. The group JID is resolved to an enabled project binding.
3. Echo atomically creates an incident, analysis job, group acknowledgement, and audit event.
4. A worker downloads only enabled project repositories using the encrypted fine-grained token configured in Echo.
5. Bounded text evidence is sent to the configured AI provider with an explicit untrusted-data boundary.
6. The diagnosis and confidence are stored and sent privately to the owner.
7. Patch approval queues a one-time `prepare_patch` job. The worker can create a draft branch and pull request, never a direct default-branch write.
8. With configured environments, staging approval dispatches a GitHub Actions workflow with an immutable commit SHA; a signed `workflow_run` webhook records verification.
9. With no staging environment, a separate repository-native production approval is bound to the PR number, exact head SHA, base branch, repository identity, and repository-configuration hash. The worker refuses changed heads or incomplete/failing GitHub checks, marks a draft ready, and squash-merges that exact SHA.
10. Echo then monitors the repository's existing push workflows for the resulting default-branch commit. It resolves the incident only after all matching runs finish successfully; a failed run marks the deployment and incident failed.

Separately, the operations catalogue maps projects to runtime services. Signed, idempotent observer events maintain an alert lifecycle and privately notify the owner only when a condition opens, reopens, or resolves. Echo never needs a production Docker socket or SSH credential for monitoring.

Approvals can be resolved in the UI or by replying privately from the configured owner JID with `approve <code>` or `reject <code>`.

The owner can also ask Echo directly for `status`, `alerts`, `services`, `service <name>`, `projects`, or `incident <id>`. With OpenAI configured, natural follow-up questions use a locally retained conversation and bounded Responses API function tools. The tool set is read-only, remote response storage is disabled, and every tool run is reviewable in the Assistant screen; questions can never authorize an action.

Configure the owner without handling JIDs manually: send Echo a direct WhatsApp message, open **WhatsApp** in the authenticated control plane, refresh discovered contacts, and select that contact as owner. Echo discovers direct-message contacts only; group participants are not harvested. The selection takes effect immediately and overrides the optional `ECHO_OWNER_JID` environment fallback.

Configure OpenAI under **System → OpenAI connection**. Echo validates the key and selected model, encrypts the key in MariaDB with `ECHO_ENCRYPTION_KEY`, never returns it to the browser, and applies the new provider settings immediately. `ECHO_AI_*` remains only as an optional environment fallback.

## Requirements

- Go 1.26.10 or newer
- MariaDB 11.4 LTS
- A dedicated WhatsApp number
- A fine-grained GitHub personal access token limited to selected repositories
- An OpenAI-compatible API when AI diagnosis and patch preparation are enabled

HyperMeow is an unofficial WhatsApp client. Use a dedicated number and plan for re-pairing or account restrictions.

## Local start

For a disposable end-to-end environment that includes local databases, a safe mock AI, real WhatsApp QR pairing, and a seeded test project, follow the [local WhatsApp test guide](docs/local-test.md):

```bash
make local-up
```

The normal deployment below continues to use the externally managed MariaDB host configured in `.env`.

Create the two databases and database-scoped users on your existing MariaDB server before starting Echo. Both accounts need permission to create and migrate tables within their own database, but need no global privileges:

```sql
CREATE DATABASE echo_core CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE DATABASE echo_whatsapp CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE USER 'echo_core'@'echo-host' IDENTIFIED BY 'use-a-strong-password';
CREATE USER 'echo_whatsapp'@'echo-host' IDENTIFIED BY 'use-a-different-strong-password';
GRANT ALL PRIVILEGES ON echo_core.* TO 'echo_core'@'echo-host';
GRANT ALL PRIVILEGES ON echo_whatsapp.* TO 'echo_whatsapp'@'echo-host';
```

Replace `echo-host` with the narrowest host pattern that matches the Echo container or server. Echo applies its embedded core migrations and `mdbstore` applies the HyperMeow session migrations at startup; neither account needs server-administration access.

```bash
cp .env.example .env
# Set the existing MariaDB host, two database credentials, and all secrets.
docker compose up --build
```

Open `http://localhost:8080` and sign in with `ECHO_ADMIN_TOKEN`. **Guided setup** resumes at the first incomplete step. Choose **Link device** to request a fresh, rotating WhatsApp pairing QR code; Echo never leaves a stale code waiting on the page.

For direct development:

```bash
set -a
. ./.env
set +a
export ECHO_WHATSAPP_ENABLED=false
export ECHO_DEV_MODE=true
go run ./cmd/echo
```

`ECHO_DEV_MODE` bypasses administrator authentication and must never be used outside local development.

Echo constructs the database DSNs internally, so passwords may contain DSN-reserved characters. `ECHO_MARIADB_TLS=true` verifies the server certificate against the container's trusted certificate authorities. Use `false` only on a trusted private network. `preferred` may downgrade to plaintext, while `skip-verify` encrypts without authenticating the server; neither is recommended for production.

## Initial configuration order

1. Use **Guided setup** to pair the dedicated Echo number.
2. Create or select a **Project**. A project is the hub that owns the team’s group bindings, repository graph and deployment environments.
3. In **Guided setup**, enter a fine-grained GitHub token and add selected **Repositories** as read-only. This is required for code-aware diagnosis, but not for a message-intake smoke test.
4. In WhatsApp, add Echo’s connected account to the partner group. Group membership cannot be changed from Echo.
5. Return to **Guided setup → Bind group**, discover the joined group and bind it to the project with real mentions required. Manual JID entry remains available for recovery.
6. Validate report intake and read-only diagnosis.
7. Change only the repositories intended for autonomous patch preparation from read-only to write-gated.
8. Choose a delivery mode per project:
   - Configure protected `staging` and `production` environments for Echo-dispatched workflows, then add a **Workflow runs** webhook pointing to `POST /webhooks/github` with the same secret as `ECHO_GITHUB_WEBHOOK_SECRET`.
   - Or leave environments unconfigured and use the repository-native production flow. Echo will merge only the separately approved PR commit and monitor the repository's existing default-branch push workflow, such as a GHCR build and Portainer redeploy.

## GitHub token permissions

Start with:

- Contents: read
- Metadata: read

Patch preparation additionally requires:

- Contents: write
- Pull requests: write

Reading checks and monitoring repository-native production workflows requires:

- Actions: read

Workflow dispatch requires:

- Actions: write

Do not grant administration, members, secrets, workflows, or repository deletion permissions. See [GitHub token setup](docs/github-token.md).

## Environment configuration

The UI stores a deployment adapter similar to:

```json
{
  "repositoryId": "repo_...",
  "workflow": "deploy.yml",
  "inputs": {
    "site": "nudgie"
  }
}
```

The environment repository must be the repository containing the approved change. Echo dispatches from the exact `echo/` change branch and adds `echo_deployment_id`, `echo_incident_id`, `echo_artifact_ref`, and `echo_environment` to every dispatch. The workflow must deploy `echo_artifact_ref`, not rebuild an unrelated branch head. Keep the change branch until production verification or rollback retention is complete.

For a project without configured environments, Echo uses repository-native production. The production approval records the exact open PR head and configured default branch. At execution, Echo revalidates the PR and successful GitHub checks, merges only that SHA, and watches push-triggered workflow runs for the merge commit on the default branch. The repository's workflow remains responsible for building, deploying, and verifying the application; a successful merge by itself is never treated as a successful deployment.

## Security controls

- HTTP-only, SameSite-strict owner sessions and CSRF protection
- Strict content-security and framing headers
- Constant-time token and webhook signature verification
- Separate MariaDB accounts and schemas
- Database leases for WhatsApp single-client ownership and worker jobs
- Idempotent WhatsApp incident intake by group and source message ID
- Bounded repository archive extraction with traversal and symlink rejection
- Protected patch paths and eight-file/one-MiB limits
- No execution of repository code on the Echo host
- AES-GCM-encrypted, repository-scoped fine-grained GitHub tokens
- Signed CI result ingestion before deployment verification
- Append-only audit records around authorization and mutations
- Container capability drop, read-only root filesystem, and no-new-privileges

Read [the threat model](docs/threat-model.md) before enabling write access.

## Verification

```bash
make check
make test
docker compose config
docker build -t echo:v2 .
```

MariaDB integration tests run only when `ECHO_TEST_CORE_DSN` is set.

## Repository layout

```text
cmd/echo/                 Process lifecycle
internal/ai/              Advisory diagnosis and bounded patch plans
internal/config/          Fail-closed environment configuration
internal/githubapp/       GitHub authentication, repository archives, PRs and Actions
internal/httpapi/         Authenticated API, CSRF and signed webhooks
internal/policy/          Deterministic action rules
internal/store/           MariaDB access and embedded migrations
internal/whatsapp/        HyperMeow gateway and durable outbox
internal/worker/          Leased jobs and disposable workspaces
internal/workflow/        Incident and approval orchestration
webui/                    Embedded responsive control-plane UI
docs/                     Architecture, security and operations
```

## Current operational boundary

Echo creates draft pull requests and can either dispatch explicitly configured environment workflows or, after a distinct production approval, merge an exact PR head and monitor the repository's existing default-branch deployment workflow. It does not bypass branch protection, write GitHub secrets, execute repository scripts on the Echo host, or SSH into staging/production. Repository-native production never rewrites a changed PR head into an existing approval.

## License

MIT. HyperMeow and `mdbstore` contain their own MPL-2.0/MIT licensing boundaries.
