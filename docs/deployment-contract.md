# Deployment contract

Echo supports two delivery contracts. A project with configured environments uses the explicit dispatch contract below. A project without a staging environment can use the repository-native production contract, which is designed for repositories whose normal default-branch workflow already builds and deploys the application.

## Repository-native production

This mode requires a separate `merge_production` approval. Echo stores a canonical action containing the change-set ID, repository ID and full name, repository-configuration hash, PR number, exact 40-character head SHA, and configured base branch.

At execution Echo:

1. Revalidates that the repository is enabled, write-gated, belongs to the incident project, and still matches the approved configuration hash.
2. Requires the PR to have the approved head SHA and configured default branch.
3. Requires at least one GitHub Actions run for the approved head and rejects pending, cancelled, timed-out, action-required, skipped, neutral, or failing runs.
4. Marks a draft PR ready and asks GitHub to squash-merge with the approved SHA as the merge precondition.
5. Records the resulting merge commit as the production artifact.
6. Monitors push-triggered GitHub Actions runs for that exact commit on the default branch.
7. Marks production verified and resolves the incident only when all matching runs complete successfully. Any other completed conclusion marks production failed.

The repository workflow is responsible for deployment and application-specific verification. Echo does not treat the merge response as deployment success, does not alter repository workflows, and does not bypass branch protection. If no matching workflow appears, monitoring times out and the job is retried within the configured worker limit before stopping with an owner notification.

## Explicit environment dispatch

An Echo-compatible GitHub Actions workflow accepts these `workflow_dispatch` inputs:

- `echo_deployment_id`: the durable, approval-bound dispatch identifier.
- `echo_incident_id`: the durable Echo incident identifier.
- `echo_artifact_ref`: an immutable 40-character Git commit SHA.
- `echo_environment`: `staging` or `production`.

Project-specific inputs may be added in the environment configuration.

## Required workflow behavior

1. Check out exactly `echo_artifact_ref`.
2. Refuse a mutable branch or tag in place of the commit.
3. Build once and record the resulting digest.
4. Run unit, integration, security, and project-specific tests.
5. For staging, deploy that artifact and run smoke or browser tests.
6. For production, verify that the artifact matches the one recorded for the successful staging run.
7. Use a protected GitHub environment with environment-scoped secrets.
8. Emit a failing workflow conclusion on any incomplete check.
9. Retain logs and artifact metadata for audit.
10. Provide a separate rollback workflow targeting a known-good immutable artifact.

Echo dispatches the workflow from the approved change branch and listens for the signed `workflow_run` completion event. The environment's repository must therefore be the same repository as the approved change. The repository full name, workflow filename, and `head_sha` must match the queued deployment's immutable artifact. A dispatch response does not count as success. Do not delete the change branch until production verification and the required rollback-retention window have completed.

## Example dispatch declaration

```yaml
on:
  workflow_dispatch:
    inputs:
      echo_incident_id:
        required: true
        type: string
      echo_deployment_id:
        required: true
        type: string
      echo_artifact_ref:
        required: true
        type: string
      echo_environment:
        required: true
        type: choice
        options: [staging, production]
```

The workflow should validate the SHA before checkout and should use GitHub environment protection independently of Echo's approval.
