# SX Skills Setup

Hetchy uses SX to install agent personas and scoped skills into sandboxes. This
is optional for basic operation, but it improves agent behavior and lets an
organization maintain its own skills vault.

## Public Vault

By default, an empty `HETCHY_SX_PUBLIC_VAULT_URL` uses Hetchy's public SX vault:

```dotenv
HETCHY_SX_PUBLIC_VAULT_URL=
```

To use a fork or another public Git vault:

```dotenv
HETCHY_SX_PUBLIC_VAULT_URL=https://github.com/example/hetchy-sx-vault.git
```

To skip the public vault install:

```dotenv
HETCHY_SX_PUBLIC_VAULT_URL=disabled
```

`off`, `none`, and `-` are also treated as disabled values.

## Per-Org Skills.new Vault

Organization admins can add an SX key in **Organization settings -> Integrations
-> SX skills vault**. That key is stored per organization and encrypted at rest.

## Cache Settings

For persistent server-side SX Git cache, set:

```dotenv
HETCHY_SX_CACHE_DIR=/data/hetchy/sx-cache
HETCHY_SX_CACHE_MIN_FREE_MB=512
HETCHY_SX_GIT_OPERATION_TIMEOUT_SECONDS=180
HETCHY_SX_GIT_MAX_CONCURRENT_OPS=4
```

In local Docker Compose, leaving these values empty is fine.
