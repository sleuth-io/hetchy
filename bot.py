import argparse
import json
import os
import queue
import re
import shlex
import textwrap
import threading
import time

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
SANDBOX_SNAPSHOT = os.getenv("DAYTONA_SNAPSHOT", f"ghcr.io/{GITHUB_REPO}/sandbox:latest")
WORKDIR = "/home/daytona/work"

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


def handle_request(text, request_id, on_update):
    """Core logic: spin up a sandbox, run Claude, open a PR. on_update(msg) is called for progress."""
    sb = None
    try:
        on_update("Spinning up an isolated sandbox for your request...")
        sb = daytona.create(CreateSandboxFromSnapshotParams(
            snapshot=SANDBOX_SNAPSHOT,
            env_vars={
                "ANTHROPIC_API_KEY": ANTHROPIC_API_KEY,
                "GITHUB_TOKEN": GITHUB_TOKEN,
            },
        ))
        on_update(f"Sandbox `{sb.id}` ready — cloning repo and starting Claude Code.")

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
        out = sh(sb, f"cd {WORKDIR} && claude --print {shlex.quote(prompt)}", timeout=900)

        match = PR_URL_RE.search(out)
        if not match:
            raise RuntimeError(f"no PR URL found in agent output. Tail:\n{out[-1500:]}")

        on_update(f"Done! :tada: {match.group(0)}")
        sb.delete()

    except Exception as e:
        msg = f"Something went wrong: `{e}`"
        if sb:
            msg += f"\nSandbox `{sb.id}` was left running for debugging."
        on_update(msg)
        print(f"[error] {e}", flush=True)


# ── Slack ─────────────────────────────────────────────────────────────────────

slack_app = App(token=SLACK_BOT_TOKEN)


def process_request(event, client):
    if event.get("bot_id") or event.get("thread_ts"):
        return
    text = (event.get("text") or "").strip()
    text = re.sub(r"^<@[A-Z0-9]+>\s*", "", text).strip()
    if not text:
        return

    channel = event["channel"]
    ts = event["ts"]
    user = event["user"]
    request_id = ts.replace(".", "")

    def on_update(msg):
        client.chat_postMessage(channel=channel, thread_ts=ts, text=f"<@{user}> {msg}")

    threading.Thread(target=handle_request, args=(text, request_id, on_update), daemon=True).start()


@slack_app.event("message")
def handle_message(event, client):
    process_request(event, client)


@slack_app.event("app_mention")
def handle_mention(event, client):
    process_request(event, client)


# ── Web UI ────────────────────────────────────────────────────────────────────

CHAT_HTML = """<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>Software Factory</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, sans-serif; display: flex; flex-direction: column;
         height: 100vh; background: #f5f5f5; }
  #log  { flex: 1; overflow-y: auto; padding: 1.5rem; display: flex;
          flex-direction: column; gap: .75rem; }
  .msg  { max-width: 70%; padding: .6rem 1rem; border-radius: 1rem; line-height: 1.4; }
  .user { align-self: flex-end; background: #0b93f6; color: #fff; border-bottom-right-radius: .25rem; }
  .bot  { align-self: flex-start; background: #fff; border: 1px solid #ddd;
          border-bottom-left-radius: .25rem; white-space: pre-wrap; }
  #bar  { display: flex; gap: .5rem; padding: 1rem; background: #fff;
          border-top: 1px solid #ddd; }
  #inp  { flex: 1; padding: .6rem 1rem; border: 1px solid #ccc; border-radius: 2rem;
          font-size: 1rem; outline: none; }
  #inp:focus { border-color: #0b93f6; }
  button { padding: .6rem 1.2rem; background: #0b93f6; color: #fff; border: none;
           border-radius: 2rem; font-size: 1rem; cursor: pointer; }
  button:disabled { opacity: .5; cursor: default; }
</style>
</head>
<body>
<div id="log"></div>
<div id="bar">
  <input id="inp" placeholder="Describe a feature…" autofocus>
  <button id="btn" onclick="send()">Send</button>
</div>
<script>
const log = document.getElementById('log');
const inp = document.getElementById('inp');
const btn = document.getElementById('btn');

inp.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); } });

function addMsg(text, cls) {
  const d = document.createElement('div');
  d.className = 'msg ' + cls;
  d.textContent = text;
  log.appendChild(d);
  log.scrollTop = log.scrollHeight;
  return d;
}

async function send() {
  const text = inp.value.trim();
  if (!text) return;
  inp.value = '';
  btn.disabled = true;
  addMsg(text, 'user');
  const bot = addMsg('…', 'bot');

  const res = await fetch('/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text })
  });

  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  bot.textContent = '';

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    const parts = buf.split('\\n\\n');
    buf = parts.pop();
    for (const part of parts) {
      if (part.startsWith('data: ')) {
        bot.textContent += (bot.textContent ? '\\n' : '') + JSON.parse(part.slice(6));
        log.scrollTop = log.scrollHeight;
      }
    }
  }
  btn.disabled = false;
  inp.focus();
}
</script>
</body>
</html>"""


def make_web_app():
    from flask import Flask, Response
    from flask import request as freq

    web = Flask(__name__)

    @web.route("/")
    def index():
        return CHAT_HTML

    @web.route("/chat", methods=["POST"])
    def chat():
        text = freq.json["text"].strip()
        request_id = str(int(time.time() * 1000))
        q = queue.Queue()

        def on_update(msg):
            q.put(msg)

        def run():
            handle_request(text, request_id, on_update)
            q.put(None)

        threading.Thread(target=run, daemon=True).start()

        def stream():
            while True:
                msg = q.get()
                if msg is None:
                    return
                yield f"data: {json.dumps(msg)}\n\n"

        return Response(stream(), mimetype="text/event-stream")

    return web


# ── Entrypoint ────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=3000)
    args = parser.parse_args()

    # Slack bot in background thread
    threading.Thread(target=lambda: SocketModeHandler(slack_app, SLACK_SOCKET_TOKEN).start(), daemon=True).start()

    # Web UI in main thread
    print(f"Web UI: http://localhost:{args.port}", flush=True)
    make_web_app().run(port=args.port, debug=False)
