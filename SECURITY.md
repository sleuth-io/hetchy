# Security Policy

## Reporting A Vulnerability

Please report suspected vulnerabilities privately by emailing:

security@hetchy.ai

Include:

- Affected component or route.
- Steps to reproduce.
- Impact and required privileges.
- Any logs or proof of concept details that are safe to share.

We will acknowledge reports as quickly as practical and coordinate remediation
before public disclosure.

## Supported Versions

The `main` branch receives security fixes. Tagged releases, once published, will
document their support window in release notes.

## Handling Secrets

Never include real tokens, credentials, private keys, or production database
URLs in issues or pull requests. Hetchy encrypts per-org credentials at rest
with `SECRETS_ENCRYPTION_KEY`; losing that key can make stored credentials
unreadable, and leaking it can expose stored credentials.
