import os
import re
import shlex
import textwrap

from daytona import CreateSandboxFromSnapshotParams, Daytona, DaytonaConfig
from dotenv import load_dotenv
from slack_bolt import App
from slack_bolt.adapter.socket_mode import SocketModeHandler

load_dotenv()

SLACK_BOT_TOKEN = os.environ["SLACK_BOT_OAUTH_TOKEN"]
SLACK_SOCKET_TOKEN = os.environ["SLACK_SOCKET_TOKEN"]
ANTHROPIC_API_KEY = os.environ["ANTHROPIC_API_KEY"]
GITHUB_TOKEN = os.environ["GITHUB_TOKEN"]
GITHUB_REPO = os.environ["GITHUB_REPO"]
BASE_BRANCH = os.getenv("GITHUB_BASE_BRANCH", "main")
SANDBOX_SNAPSHOT = os.getenv("DAYTONA_SNAPSHOT", "claude-playwright:1")
WORKDIR = "/home/daytona/work"

app = App(token=SLACK_BOT_TOKEN)

_daytona_api_url = os.getenv("DAYTONA_API_URL")
daytona = Daytona(DaytonaConfig(api_url=_daytona_api_url) if _daytona_api_url else None)
print(f"Daytona: {'local @ ' + _daytona_api_url if _daytona_api_url else 'cloud (app.daytona.io)'}", flush=True)

PR_URL_RE = re.compile(r"https://github\.com/[^\s]+/pull/\d+")

AGENT_PROMPT = textwrap.dedent("""
    You are working inside a fresh sandbox. The repo {repo} has been cloned
    to {workdir} and {base} is checked out. Your task is the user request below.

    USER REQUEST:
    {user_request}

    When you are done implementing the change:
      1. Create a new branch named feature/sf-{request_id}.
      2. Stage and commit your changes with a clear message.
      3. Push the branch to origin (gh CLI is already authenticated).
      4. Open a pull request against {base} with `gh pr create`, giving it a
         clear title and a markdown body describing what changed and why.
      5. The very last line of your output MUST be just the PR URL — no other
         text on that line.
""").strip()


def sh(sb, cmd, timeout=120):
    res = sb.process.exec(cmd, timeout=timeout)
    if res.exit_code != 0:
        raise RuntimeError(f"sandbox cmd failed (exit {res.exit_code}): {cmd}\n{res.result}")
    return res.result


def process_request(event, client):
    if event.get("bot_id") or event.get("thread_ts"):
        return
    text = (event.get("text") or "").strip()
    if not text:
        return

    channel = event["channel"]
    ts = event["ts"]
    user = event["user"]
    request_id = ts.replace(".", "")

    # Strip the @-mention prefix if present
    text = re.sub(r"^<@[A-Z0-9]+>\s*", "", text).strip()
    if not text:
        return

    def reply(msg):
        client.chat_postMessage(channel=channel, thread_ts=ts, text=msg)

    reply(f"<@{user}> Spinning up an isolated sandbox for your request...")

    sb = daytona.create(CreateSandboxFromSnapshotParams(
        snapshot=SANDBOX_SNAPSHOT,
        env_vars={
            "ANTHROPIC_API_KEY": ANTHROPIC_API_KEY,
            "GITHUB_TOKEN": GITHUB_TOKEN,
        },
    ))
    reply(f"<@{user}> Sandbox `{sb.id}` ready — cloning repo and starting Claude Code.")

    try:
        sh(sb, "echo $GITHUB_TOKEN | gh auth login --with-token && gh auth setup-git", timeout=60)
        sh(sb,
            f"git clone https://github.com/{GITHUB_REPO}.git {WORKDIR} "
            f"&& cd {WORKDIR} && git checkout {BASE_BRANCH} "
            f"&& git config user.email 'software-factory-bot@users.noreply.github.com' "
            f"&& git config user.name 'software-factory-bot'",
            timeout=180,
        )

        prompt = AGENT_PROMPT.format(
            repo=GITHUB_REPO,
            workdir=WORKDIR,
            base=BASE_BRANCH,
            user_request=text,
            request_id=request_id,
        )
        out = sh(sb,
            f"cd {WORKDIR} && claude --print {shlex.quote(prompt)}",
            timeout=900,
        )

        match = PR_URL_RE.search(out)
        if not match:
            raise RuntimeError(f"no PR URL found in agent output. Tail:\n{out[-1500:]}")

        pr_url = match.group(0)
        reply(f"<@{user}> Done! :tada: {pr_url}")
        sb.delete()

    except Exception as e:
        reply(
            f"<@{user}> Something went wrong: `{e}`\n"
            f"Sandbox `{sb.id}` was left running for debugging."
        )


@app.event("message")
def handle_message(event, client):
    process_request(event, client)


@app.event("app_mention")
def handle_mention(event, client):
    process_request(event, client)


if __name__ == "__main__":
    SocketModeHandler(app, SLACK_SOCKET_TOKEN).start()
