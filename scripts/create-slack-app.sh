#!/usr/bin/env bash
set -euo pipefail

# Create a personal Hetchy Slack app for a developer.
#
# Usage:   ./scripts/create-slack-app.sh <NAME>
# Example: ./scripts/create-slack-app.sh Dylan
#
# Required env: SLACK_CONFIG_TOKEN (xoxe.xoxp-...)
#   Generate at https://api.slack.com/apps under "Your App Configuration
#   Tokens" -> Generate Token. Tokens expire in 12 hours; refresh tokens
#   last 12 weeks. Save both somewhere safe.
#
# Each dev has a `dev_personal` Doppler config that inherits from `dev`
# and allows overrides. Set it as the default with `doppler setup` (or
# `doppler configure set config dev_personal`); then this script's
# printed `doppler secrets set` command will land in your personal
# config without any --config flag.

NAME="${1:?usage: $0 <NAME>}"
TOKEN="${SLACK_CONFIG_TOKEN:?SLACK_CONFIG_TOKEN must be set (generate at https://api.slack.com/apps)}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE="$SCRIPT_DIR/slack-manifest.template.json"

if [ ! -f "$TEMPLATE" ]; then
  echo "missing template: $TEMPLATE" >&2
  exit 1
fi

manifest_json="$(TEMPLATE="$TEMPLATE" NAME="$NAME" python3 -c '
import json, os
with open(os.environ["TEMPLATE"], "r") as f:
    t = f.read()
print(json.dumps(json.loads(t.replace("{{NAME}}", os.environ["NAME"]))))
')"

response="$(curl -sS -X POST 'https://slack.com/api/apps.manifest.create' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode "manifest=$manifest_json")"

# Parse the response and emit the user-facing message in a single
# Python call. Avoids `eval` on API output entirely — the script just
# pipes Python's stdout through. Note: `python3 -c` (not `python3 -`
# with a heredoc) so stdin stays free for json.load(sys.stdin) to
# read the API response.
echo "$response" | NAME="$NAME" python3 -c '
import json, os, sys

d = json.load(sys.stdin)
if not d.get("ok"):
    sys.stderr.write("Slack API rejected manifest:\n" + json.dumps(d, indent=2) + "\n")
    sys.exit(1)

name = os.environ["NAME"]
app_id = d.get("app_id", "")
creds = d.get("credentials", {})
client_id = creds.get("client_id", "")
client_secret = creds.get("client_secret", "")
signing_secret = creds.get("signing_secret", "")

# Pre-extracted into locals so the f-string body has no inner
# quotes — keeps the bash-quoted python literal readable and avoids
# any Python-version-dependent f-string parsing surprises.
print(f"""
  Created \"Hetchy ({name})\" — App ID {app_id}

Next steps:

  1. Install the app to your sandbox workspace.
       Open https://api.slack.com/apps/{app_id}/install-on-team
       Click \"Install to Workspace\", approve scopes, then copy the
       \"Bot User OAuth Token\" (xoxb-...).

  2. Generate a Socket Mode app-level token.
       Open https://api.slack.com/apps/{app_id}/general
       Scroll to \"App-Level Tokens\" -> \"Generate Token and Scopes\"
         Name:   socket
         Scope:  connections:write
       Copy the token (xapp-...).

  3. Paste both tokens into your local Hetchy at
       http://localhost:8080/settings/org
       (Bot token + Socket token fields — they're stored encrypted in
        your local Postgres; they do NOT go in Doppler.)

  4. Restart `make bot` so it opens a Socket Mode connection with the
     new tokens.

Optional: save these app credentials to your dev_personal Doppler
config in case you later want to test the OAuth Install button flow
locally. They are NOT needed for normal Socket Mode dev:

  doppler secrets set \\
    SLACK_APP_ID={app_id} \\
    SLACK_CLIENT_ID={client_id} \\
    SLACK_CLIENT_SECRET={client_secret} \\
    SLACK_SIGNING_SECRET={signing_secret}
""")
'
