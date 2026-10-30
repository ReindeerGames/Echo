# GitHub token setup

Echo uses a fine-grained personal access token entered from the authenticated **Repositories** or **Guided setup** screen. The token is validated against GitHub before it is encrypted and stored in MariaDB. Echo never returns the value to the browser after saving it.

After connecting, choose **Discover from GitHub**. Echo reads the repositories visible to the token in deterministic pages, capped at 500, and shows only owner/name, default branch, public/private state, archive/fork state, and coarse write permission. Archived and already-linked repositories are excluded from the picker. Linking always starts in read-only mode; discovery cannot grant patch or deployment authority. Manual owner/name entry remains available for recovery.

Create the token under the GitHub account that owns or can access the intended repositories. Prefer a dedicated automation account, choose only the required repositories, set an expiry, and rotate it before that date.

## Permissions

Start with:

- Contents: read
- Metadata: read

Patch preparation additionally requires:

- Contents: write
- Pull requests: write

Reading checks and monitoring repository-native production workflows requires:

- Actions: read

GitHub Actions workflow dispatch additionally requires:

- Actions: write

Do not grant Administration, Secrets, Members, Workflows, or repository deletion access.

## Deployment webhook

Token authentication does not create webhooks. On each repository used for deployment, add a repository webhook with:

- Payload URL: `https://<echo-host>/webhooks/github`
- Content type: `application/json`
- Secret: the same high-entropy value as `ECHO_GITHUB_WEBHOOK_SECRET`
- Event: **Workflow runs**

Echo accepts only correctly signed `workflow_run` events and correlates the repository, workflow and immutable artifact before treating staging or production as verified.

## Storage and rotation

`ECHO_ENCRYPTION_KEY` stays in the deployment environment and encrypts application-managed credentials with AES-GCM. The ciphertext and non-secret GitHub account name are stored in `echo_core`. Back up the encryption key separately: replacing or losing it makes stored credentials unreadable.

To rotate the token, choose **Replace token** in Echo, validate the replacement, and then revoke the old token in GitHub. To stop all GitHub access immediately, remove the token in Echo and revoke it in GitHub.

## Repository configuration

Each Echo repository record contains the owner, name, default branch, project relationship, role, and access mode. Keep repositories read-only until report routing and diagnosis have been verified. Switching `readOnly` off permits an approved worker to create an `echo/` branch and draft pull request. It permits a default-branch update only through a separate `merge_production` approval bound to the exact PR head; GitHub branch protection and required reviews remain authoritative.

See GitHub's [fine-grained personal access token documentation](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens) for token creation and organization approval requirements.
