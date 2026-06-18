# Linear Setup

Linear is optional. When configured, Hetchy can be installed as a Linear agent so
users can mention or delegate issues to the agent and receive progress updates
back on the issue.

## Environment

Configure a public Hetchy base URL first:

```dotenv
HETCHY_PUBLIC_BASE_URL=https://hetchy.example.com
```

Then create a Linear OAuth app or agent integration and set:

```dotenv
LINEAR_CLIENT_ID=...
LINEAR_CLIENT_SECRET=...
LINEAR_OAUTH_REDIRECT_URI=https://hetchy.example.com/integrations/linear/oauth/callback
LINEAR_WEBHOOK_SECRET=...
```

The webhook URL is:

```text
https://hetchy.example.com/integrations/linear/webhook
```

Hetchy requests these OAuth scopes:

```text
read
write
app:assignable
app:mentionable
```

## Connect A Workspace

1. Restart Hetchy after setting the environment variables.
2. Sign in as an organization admin.
3. Open **Organization settings -> Integrations -> Linear**.
4. Click **Enable** and complete Linear's OAuth install.
5. Mention the Hetchy agent on a Linear issue, or assign/delegate the issue to
   the agent if your Linear workspace exposes that flow.

The Linear access token and workspace ID are stored per Hetchy organization.
The access token is encrypted at rest with `SECRETS_ENCRYPTION_KEY`.

## Troubleshooting

- If the Linear card says the environment is not configured, verify
  `LINEAR_CLIENT_ID`, `LINEAR_CLIENT_SECRET`, and `LINEAR_OAUTH_REDIRECT_URI`.
- If events do not arrive, verify `LINEAR_WEBHOOK_SECRET` and the webhook URL.
- If OAuth fails after sign-in, make sure the redirect URI in Linear exactly
  matches `LINEAR_OAUTH_REDIRECT_URI`.
