# Draft Skill: full_report

## Trigger
Repeated query detected: "how are the docker containers"

## Workflow
1. Collect deterministic container status and health.
2. Pull resource metrics and compare against group baseline.
3. If needed, fetch filtered logs (dedupe + truncate + score).
4. Return concise issue, evidence, and recommendation.

## Notes
- Pending manual approval before activation in skills/custom/.
