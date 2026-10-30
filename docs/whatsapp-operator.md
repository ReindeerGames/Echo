# WhatsApp operator assistant

The configured owner can message Echo directly for a read-only operational view. Echo authorizes the sender against the selected owner contact, reads only its structured MariaDB state, and audits the interpreted intent without storing the question body in the audit event.

Supported requests include:

- `status` or “how are things?” for the estate summary
- `alerts` or “anything wrong?” for open runtime conditions
- `services` for the operations catalogue
- `service <name-or-key>` for a service's open alerts and latest observer evidence
- `projects` for the project and repository map
- `incident <id>` for the incident state and latest diagnosis
- `help` for the current command summary

Replies are capped for WhatsApp readability. The control plane remains the complete record.

Questions never authorize mutation. Approval commands remain deliberately narrow: `approve <code>` or `reject <code>` resolves only the pending immutable action bound to that one-time code. An unrecognized request cannot run a shell command, contact a production host, execute repository code, or infer permission to change infrastructure.

This deterministic read-only layer is the safe foundation for richer AI-assisted investigations. Future AI routing must use the same bounded read tools and preserve the separate exact-action approval boundary.
