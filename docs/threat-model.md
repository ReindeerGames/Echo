# Threat model

## Protected assets

- WhatsApp device credentials, Signal sessions, prekeys, and pairing codes
- Partner reports, attachments, and limited conversation context
- Private repository contents and the fine-grained GitHub token
- Administrator sessions and approval authority
- Staging and production workflow permissions
- AI provider credentials and submitted source context
- Audit and deployment evidence

## Primary adversaries

- An untrusted or compromised WhatsApp group member
- Prompt injection embedded in a message, source file, issue, log, or generated artifact
- A compromised repository or dependency
- A stolen administrator token or WhatsApp owner device
- A forged GitHub webhook
- A second process attempting to use the same WhatsApp session
- Accidental operator approval of an ambiguous or stale action

## Enforced mitigations

### Untrusted content

Message text, GitHub metadata, repository contents, operator-entered service topology, repository ownership, and service runbooks are labelled as evidence in AI prompts. Dependency types and descriptions are bounded, dependency and ownership relationships cannot cross project boundaries, and a declared edge or ownership link is never treated as proof of causation. Explicit ownership narrows an investigation to linked enabled repositories; the no-link compatibility fallback is labelled in tool output. Moving a linked service or repository across projects is rejected until its links are removed, preventing a configuration edit from widening another project's evidence boundary. Runbooks are capped per service and per tool result, common secret forms are redacted before AI use, and their optional references must be HTTPS URLs without credentials, queries, or fragments. Embedded runbook instructions cannot grant execution or mutation authority, and the model is told never to claim a suggested check was performed. Model output is parsed into bounded structures and is never interpreted as an authorization. GitHub inventory exposes only capped repository identity, branch, visibility/archive/fork state, and Echo catalogue relationships; it cannot expand the token's scope or inspect unconfigured source. Owner-initiated repository search is restricted to enabled repositories and the configured default branch, never executes source, excludes credential/key/dependency/generated paths, and returns only capped line-numbered excerpts after common-secret redaction. Search traversal, files, bytes, per-file size, matches, output, and wall time are bounded and truncation is explicit. Patch output is restricted to eight text files, one MiB each, with traversal, secret, workflow, lockfile, and deployment-path rejection.

Service investigation is correlation over existing normalized evidence, not remote collection. Signal, alert, dependency, ownership-link, runbook, incident, deployment, and repository collections have independent caps and truncation flags. Monitoring details retain only allowlisted typed fields, including aggregate process counts, states, threads, and RSS from previously signed observer evidence; PIDs, command lines, environment, identities, paths, and logs are excluded. Dependency and repository checks remain within the service's configured project, and upstream errors are reduced to availability state. The tool has no live process, log, filesystem, Docker, shell, SSH, or mutation capability.

Proactive GitHub monitoring uses the existing fine-grained token only for default-branch commit and Actions metadata. It inspects at most 100 enabled repositories with four workers, per-request and whole-sweep deadlines, and stores only repository identity, branch, head SHA, bounded workflow names, state, summary, and timestamps. Raw GitHub errors are not persisted or sent to WhatsApp. Running, absent, and failed-to-fetch evidence is never treated as recovery. The watchdog exposes no rerun, dispatch, cancel, source-download, or repository-write path.

The signed monitoring webhook rejects Echo's reserved watchdog source. Healthy evidence with a fingerprint can resolve only that older condition, while an empty fingerprint is an explicit aggregate recovery. Any fresh non-watchdog event independently resolves observer silence, so restoring evidence transport cannot either conceal a continuing workload failure or leave a stale silence alert behind.

The bundled observer exposes no inbound API. Its TCP probe performs only a bounded connect-and-close, sends no application bytes, and omits the configured host and raw network error from retained evidence. It establishes listener reachability only and never authenticates to or queries the target protocol.

Observer configuration checking does not execute probes or require a webhook secret. Dry-run observers are constructed with delivery disabled, cannot call the signed delivery cycle, and print only the same bounded normalized events accepted by Echo; response bodies and raw operating-system, network, proxy, and daemon errors remain excluded. A nonzero dry-run exit reports only that normalized degraded or unhealthy evidence exists.

The separate Linux process proxy performs a bounded numeric `/proc` directory scan only after an exact configured `comm` name passes its allowlist. It reads only `comm` and the `State`, `Threads`, and `VmRSS` fields from `status`, rechecks the name to limit PID-reuse races, and emits aggregate counters over a loopback-only route. Local read and parse errors collapse to one fixed category; no raw operating-system error reaches the observer, Echo, or WhatsApp. The proxy has no Echo, GitHub, WhatsApp, AI, Docker, or webhook secret, while the observer retains a hidden process table. Neither component executes `ps`, `systemctl`, a shell, or any process-control operation.

The Docker proxy exposes only exact-allowlist lifecycle and non-streaming stats routes. It parses the daemon response into fixed fields and returns normalized state plus optional aggregate CPU percentage, cache-adjusted memory counters, and task count. Network-interface, block-device, per-CPU, raw cgroup, environment, label, mount, configuration, health-log, and daemon-error data are discarded inside the socket-owning process. The observer never receives the socket and cannot select an unallowlisted container or arbitrary API route.

### Authorization

The deterministic policy engine checks incident state, repository project membership, read-only status, environment ownership, protected-environment status, and immutable artifact provenance. Every approval has an expiry, action hash, one-time nonce, actor, incident, exact action type, and hashes of the repository and environment configuration snapshots. A configuration change invalidates the queued action instead of silently changing its meaning.

Explicit-environment production requires `staging_verified`; a successful dispatch alone cannot establish that state. Repository-native production requires its own approval bound to the repository configuration, PR number, exact PR head SHA, and base branch. The worker revalidates successful checks and uses GitHub's expected-head merge precondition. A successful merge alone cannot establish deployment success.

### External credentials

The GitHub token is validated before storage, encrypted with AES-GCM using a deployment-held key, never returned through the API, and limited to selected repositories in both GitHub and Echo. Read-only diagnosis needs Contents: read. Patch creation additionally needs Contents: write and Pull requests: write. Deployment dispatch needs Actions: write.

The WhatsApp gateway and GitHub workers should be deployed as separate services for high-assurance environments.

### Web security

The UI uses an HTTP-only signed session, SameSite Strict cookies, CSRF tokens, CSP, frame denial, content-type protection, and bounded JSON bodies. GitHub webhooks require `sha256` HMAC signatures.

### Availability and replay

Inbound incidents are unique by group JID and source message ID. Jobs and outbox messages use leases, bounded attempts, and backoff. Outbox rows carry a durable interactive/proactive class. A bounded notification-policy row is locked by both proactive enqueue and pause operations, closing the race between an owner lookup and the outbox insert; claim also rechecks the environment and durable gates. Pausing suppresses only queued or retrying proactive rows and cannot revoke a send already in progress. Same-kind monitoring transitions share a nullable queued-only coalescing key for eight seconds; the digest is capped at ten detailed items and 3,500 characters, and the key is cleared atomically on claim so later transactions cannot rewrite a message being sent. Static MariaDB rows serialize alert-index mutation and sorted batch-kind acquisition, while only deadlock and lock-timeout transactions receive bounded replay through the event idempotency key. One MariaDB named lock controls active WhatsApp ownership.

## Residual risks

- HyperMeow is unofficial and the dedicated WhatsApp account may be logged out or restricted.
- AI can produce incorrect diagnoses or unsafe patches despite constraints; human review and CI remain mandatory.
- Repository search can miss relevant code because it is deliberately bounded, and redaction cannot guarantee detection of every novel secret format. Restrict GitHub access to the required repositories and review AI-provider data handling before enabling code-aware questions.
- A compromised GitHub workflow can falsely report success or misuse environment credentials. Workflows and branch protection remain part of the trusted computing base. Repository-native monitoring confirms GitHub's workflow conclusion, not the correctness of an untrusted deployment script.
- The current all-in-one deployment places all environment secrets in one process. Split deployment is recommended before broad production access.
- Owner authorization is anchored to the configured WhatsApp JID and accepts HyperMeow's authenticated primary or alternate PN/LID sender address. Identity changes not covered by that mapping require explicit re-enrolment.
- Screenshot analysis is limited to byte-verified PNG, JPEG, and WEBP files of at most 5 MiB. PDFs, text documents, video, audio, and archives are not analyzed; their captions remain the available evidence. Follow-ups are accepted only when WhatsApp's authenticated quoted-message ID resolves to an open incident message in the same bound group.

## Go-live requirements

- Complete a backup restoration drill for both MariaDB databases.
- Validate WhatsApp logout and re-pairing.
- Exercise duplicate delivery and worker crash recovery.
- Create a fine-grained token for a dedicated automation account, select only required repositories, and set an expiry and rotation schedule.
- Prove branch protection rejects direct default-branch writes.
- Prove staging and production use different environment secrets.
- Prove a forged CI webhook cannot change deployment state.
- Test rollback for every production environment.
- Review data retention and AI-provider data handling with the organization responsible for the partner groups.
