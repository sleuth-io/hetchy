# GitHub PAT Setup

Self-hosted Hetchy can work without a GitHub App. Each organization can store a
GitHub personal access token in the settings UI, and Hetchy uses that token to
list writable repositories, push branches, and open pull requests.

## Create A Fine-Grained Token

1. In GitHub, open **Settings -> Developer settings -> Personal access tokens -> Fine-grained tokens**.
2. Generate a new token for the account or organization that owns the target repositories.
3. Select only the repositories Hetchy should be allowed to modify.
4. Set repository permissions:
   - **Contents**: read and write
   - **Pull requests**: read and write
   - **Metadata**: read-only
5. Optional permissions:
   - **Workflows**: read and write, only if Hetchy should edit files under `.github/workflows/`.
   - **Issues**: read and write, only if you want issue comments or labels managed through the same token.
6. Generate the token and copy it once. GitHub will not show it again.

## Connect In Hetchy

1. Sign in to Hetchy as an organization admin.
2. Open **Organization settings -> Integrations -> GitHub**.
3. Paste the token into the personal access token form and save.
4. Sync repositories.
5. Pick a default repository in the repository settings if you want chat requests to use a default target.

The token is encrypted at rest with `SECRETS_ENCRYPTION_KEY`.

## PAT Mode Behavior

PAT mode does not receive GitHub App webhooks. Hetchy refreshes repository and
pull request state during normal actions, but self-hosted operators can run a
periodic backfill if stale PR status is noticeable:

```bash
docker compose run --rm hetchy --backfill-pr-states
```

For hosted-style webhook sync, configure a GitHub App with `GITHUB_APP_*`
environment variables instead of relying only on PAT mode.
