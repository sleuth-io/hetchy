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

eval "$(echo "$response" | python3 -c '
import json, sys, shlex
d = json.load(sys.stdin)
if not d.get("ok"):
    sys.stderr.write("Slack API rejected manifest:\n" + json.dumps(d, indent=2) + "\n")
    sys.exit(1)
print(f"export APP_APP_ID={shlex.quote(d.get(\"app_id\", \"\"))}")
creds = d.get("credentials", {})
for k in ("client_id", "client_secret", "signing_secret", "verification_token"):
    print(f"export APP_{k.upper()}={shlex.quote(creds.get(k, \"\"))}")
')"

cat <<EOF

  Created "Hetchy ($NAME)" — App ID $APP_APP_ID

Next manual steps in https://api.slack.com/apps/$APP_APP_ID :
  1. Install App -> Install to Workspace
       (gives you the xoxb- bot token)
  2. Basic Information -> App-Level Tokens -> Generate Token and Scopes
       name:  socket
       scope: connections:write
       (gives you the xapp- socket token)

Make sure your default Doppler config is dev_personal, then:

  doppler secrets set \\
    SLACK_APP_ID=$APP_APP_ID \\
    SLACK_CLIENT_ID=$APP_CLIENT_ID \\
    SLACK_CLIENT_SECRET=$APP_CLIENT_SECRET \\
    SLACK_SIGNING_SECRET=$APP_SIGNING_SECRET \\
    SLACK_BOT_TOKEN=<xoxb- from step 1> \\
    SLACK_APP_TOKEN=<xapp- from step 2>

EOF
