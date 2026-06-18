# Troubleshooting Guide

This guide covers common issues and their solutions.

## Sandbox Creation Fails

**Symptoms:** Bot fails to create or resume a Daytona sandbox

**Solutions:**
- Confirm `DAYTONA_API_URL` points at Daytona Cloud: `https://app.daytona.io/api`
- Check `DAYTONA_API_KEY` is valid for the Daytona org that should own sandboxes
- Verify the versioned snapshot `${DAYTONA_SNAPSHOT}-${sandbox_version}` exists and is active in that org
- Check the bot startup log for `daytona configured` and the request log for the created sandbox ID

## No PR Created

**Symptoms:** Bot completes execution but doesn't create a pull request

**Solutions:**
- Check Claude Code output in bot logs
- Verify the org has a connected GitHub PAT or GitHub App installation
- Confirm the token has repository Contents and Pull requests read/write access
- Ensure base branch exists in repository

## Slack Connection Issues

**Symptoms:** Bot doesn't respond to Slack messages or shows connection errors

**Solutions:**
- Confirm bot token and socket token are correct
- Verify bot is invited to the channel
- Check socket mode is enabled in Slack app settings
- Ensure the Slack app has the `reactions:write` OAuth scope (required for status reactions)
- Look for `slack socket connected — listening for events` in logs

For detailed Slack setup issues, see the [Slack Setup Guide](slack-setup.md#common-setup-issues).

## Web UI Not Accessible

**Symptoms:** Cannot access the web UI at the configured port

**Solutions:**
- Verify port is not already in use
- Check `WEB_PORT` environment variable
- Review firewall/network settings

## Database Connection Issues

**Symptoms:** Bot fails to connect to Postgres or migrations fail

**Solutions:**
- Verify `DATABASE_URL` is correctly formatted
- With Docker Compose, run `docker compose logs postgres`
- For local development, ensure `make pg-up` has been run
- For Supabase, verify you're using the direct connection (port 5432) for migrations
- Check that connection pooler URLs include `?default_query_exec_mode=exec`
- Review database logs: `make pg-logs` (for local)

## Local Auth Issues

**Symptoms:** Signup/login fails or sessions disappear

**Solutions:**
- Verify `HETCHY_AUTH_MODE=local`
- Verify `SECRETS_ENCRYPTION_KEY` is stable across restarts
- Use `COOKIE_INSECURE=1` only for plain HTTP; remove it behind HTTPS
- Set `HETCHY_PUBLIC_BASE_URL` to the exact browser origin users open

## Build Failures

**Symptoms:** `make build` or `make prepush` fails

**Solutions:**
- Ensure Go 1.25.6 or later is installed
- Run `go mod tidy` to sync dependencies
- Check for syntax errors in recent changes
- Review build logs for specific error messages

## Environment Configuration Issues

**Symptoms:** Bot fails to start with missing environment variables

**Solutions:**
- Start from the self-host template: `cp .env.example .env`
- Set `DATABASE_URL`, `SECRETS_ENCRYPTION_KEY`, `DAYTONA_API_KEY`, and `DAYTONA_SNAPSHOT`
- In local auth mode, leave WorkOS values empty
- In WorkOS mode, set the required `WORKOS_*` values
- Run `docker compose config` to catch malformed compose or env values
