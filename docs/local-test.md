# Local WhatsApp test environment

The local stack is deliberately separate from Echo's normal Compose deployment. It runs disposable MariaDB databases and a deterministic mock AI, while HyperMeow connects to the real WhatsApp network. It does not need a GitHub token or AI API key to validate incident intake.

## What runs locally

- Echo on `http://localhost:8080`, bound only to `127.0.0.1`
- MariaDB 11.4 with separate `echo_local_core` and `echo_local_whatsapp` databases and users
- a deterministic OpenAI-compatible stub that marks the report as processed without claiming to inspect code
- a seeded **Local Test** project with disposable HTTP, TCP, host-resource, process, and container services
- persistent Docker volumes for the databases, WhatsApp session, and worker state

The local database passwords and administrator token are predictable test defaults and must never be reused in production. The stack is isolated to `docker-compose.local.yml`; MariaDB is not exposed on a host port, and the control plane binds only to loopback.

## Start and pair

```bash
make local-up
```

If the workstation's saved Docker context points somewhere else, override the client explicitly, for example `make DOCKER='docker --host=unix:///var/run/docker.sock' local-up`. Echo never guesses or rewrites Docker contexts.

Open `http://localhost:8080` and sign in with the local administrator token:

```text
echo-local-admin-token
```

Open **Guided setup**. It will resume at the first incomplete relationship. In **Connect WhatsApp**:

1. Open WhatsApp on the phone holding the dedicated Echo number.
2. Open **Linked devices** and choose **Link a device**.
3. In Echo, choose **Link device** to request a fresh QR code.
4. Scan the newly displayed code immediately.
5. Wait for the status in Echo to change to **connected**.

Echo polls the pairing state and replaces the image whenever WhatsApp rotates the code. If the pairing sequence times out, choose **Link device** again to start a new sequence.

Use a dedicated test number. HyperMeow is an unofficial WhatsApp client, and the session volume contains sensitive authentication material.

## Bind a test group

1. Add the dedicated Echo account to a WhatsApp test group.
2. Refresh the Echo control plane and open **Guided setup → Bind group**.
3. Choose **Discover and bind group**.
4. Select the joined group from discovery and select the seeded **Local Test** project.
5. Leave **Require actual mention** enabled and save.

The wizard treats the seeded **Local Test** project as the hub for this group. Repository access is optional for validating message intake; the wizard shows code diagnosis as unavailable until a GitHub token and repository are configured.

If the group was created or joined after pairing, wait a few seconds and reopen **Bind group** to refresh discovery.

## Log the first issue

From another member of the test group, send a message such as:

> @Echo the lead form submits but the confirmation is blank

Select Echo from WhatsApp's mention picker. Typing the same text without actual mention metadata is intentionally ignored.

Expected results:

1. Echo acknowledges the report in the group.
2. An incident appears under **Incidents** in the control plane.
3. Within a few seconds, it moves to `awaiting_owner` with low-confidence local test analysis.
4. **Audit trail** records `incident.reported` and `incident.analyzed` events.

The local mock diagnosis proves orchestration only. It never claims repository evidence and cannot prepare a patch. Repository-backed diagnosis requires the real AI and an application-managed GitHub token.

## Exercise the read-only observer

With the stack healthy on the default local port, run one signed observation cycle:

```bash
make local-observe
```

The target starts temporary loopback-only Docker and process metadata proxies plus a disposable `sleep` process, probes `/readyz` without reading its body, opens and immediately closes a TCP connection to Echo without sending application data, reads bounded kernel/filesystem counters from the local Linux environment, inspects only the allowlisted process aggregate and mock-AI container's sanitized lifecycle and resource state, signs five normalized health events, and submits them to Echo. The proxies and process exit when this one-shot test finishes. **Monitoring → Recent signals** should show `Local Echo`, `Local Echo TCP Listener`, `Local Host`, `Local Sleep Process`, and `Local Mock AI Container` as healthy. The local example explicitly permits plaintext HTTP because every endpoint binds to loopback; observer configuration requires HTTPS by default. Its deliberately high host and container resource thresholds make this a data-path test rather than a workstation-capacity alarm.

The observer resolves the Docker Unix socket from the same client endpoint selected by `DOCKER`, `DOCKER_HOST`, or the active Docker context. It refuses a TCP endpoint because the local proxy is deliberately limited to a host-local socket. This keeps the container check aligned with the engine that started the test stack without changing the workstation's Docker context.

All five seeded services expect a heartbeat within 60 seconds. If the observer is intentionally left stopped, the local watchdog opens `observer-silent` warnings. Running `make local-observe` records fresh evidence and resolves those alerts.

To enable real analysis, open **System → OpenAI connection**, enter the OpenAI API key and model, and save. Echo validates the credentials before encrypting the key in MariaDB. Application settings override the local deterministic mock immediately without restarting the stack.

## Select the private owner

1. From your personal WhatsApp account, send Echo a direct message such as `Hello Echo`.
2. Echo confirms that it discovered the contact.
3. Open **WhatsApp** in the control plane and choose **Refresh contacts**.
4. Select the discovered contact under **Owner contact** and choose **Set as owner**.

Echo records only people who message it directly; it does not collect every group participant. The selected owner immediately receives private incident analysis, approval codes, and deployment results and is the only WhatsApp identity allowed to resolve approvals.

`ECHO_OWNER_JID` remains an optional environment fallback for unattended deployments, but the in-application selection takes precedence and does not require a restart.

## Operations

```bash
make local-logs     # follow Echo and mock-AI logs
make local-down     # stop without losing pairing or incidents
make local-up       # resume the same local state
make local-reset    # delete all local data, including the WhatsApp pairing
```

`make local-reset` is destructive only to the Docker volumes belonging to the `echo-local-test` Compose project. After a reset, WhatsApp must be paired again.
You should also remove the old Echo entry from WhatsApp's **Linked devices** list when intentionally resetting or retiring the test environment.
