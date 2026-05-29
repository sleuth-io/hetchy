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
- Verify GitHub token has repo scope
- Ensure base branch exists in repository
- Confirm the Hetchy GitHub App is installed on the target repository — installation tokens are minted from the App credentials at run time, so an uninstalled App surfaces as an early run failure rather than a successful run with no PR
- For follow-up turns on an existing conversation: if the agent only updated PR metadata or validation notes and no repository files changed, no new commit is created and the existing PR URL is reused — that is by design, not a missing PR
- Check the Anthropic credential resolution path (`ANTHROPIC_API_KEY` or OAuth token); a missing or revoked credential surfaces as an early run failure with no PR

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
- For local development, ensure `make pg-up` has been run
- For Supabase, verify you're using the direct connection (port 5432) for migrations
- Check that connection pooler URLs include `?default_query_exec_mode=exec`
- Review database logs: `make pg-logs` (for local)

## Build Failures

**Symptoms:** `make build` or `make prepush` fails

**Solutions:**
- Ensure Go 1.25.6 or later is installed
- Run `go mod tidy` to sync dependencies
- Check for syntax errors in recent changes
- Review build logs for specific error messages

## Doppler Configuration Issues

**Symptoms:** Bot fails to start with missing environment variables

**Solutions:**
- Verify Doppler is installed and authenticated: `doppler login`
- Ensure you're in the correct project: `doppler setup`
- Check that all required secrets are set: `doppler secrets`
- Verify the Doppler CLI is using the correct config (check `doppler.yaml`)
