# Log Triage

1. Use filtered logs only (deduplicated, truncated, and scored).
2. Prioritize `error`, `exception`, `timeout`, and `connection refused` patterns.
3. Identify repeating signatures and likely subsystem.
4. Return concise summary with operational next actions.
