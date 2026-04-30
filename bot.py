import json
import os
import re

from anthropic import Anthropic
from github import Auth, Github, GithubException
from slack_bolt import App
from slack_bolt.adapter.socket_mode import SocketModeHandler

app = App(token=os.environ["SLACK_BOT_OAUTH_TOKEN"])
anthropic = Anthropic()
gh_repo = Github(auth=Auth.Token(os.environ["GITHUB_TOKEN"])).get_repo(os.environ["GITHUB_REPO"])
base_branch = os.getenv("GITHUB_BASE_BRANCH", "main")

SYSTEM_PROMPT = """You are a software engineer. Given a feature request, implement it.
Respond with valid JSON only — no markdown fences, no extra text:
{
  "filename": "relative/path/to/file.ext",
  "code": "full file content as a string",
  "pr_title": "concise PR title",
  "pr_body": "markdown PR description explaining what was done and why"
}"""


def process_request(event, client):
    if event.get("bot_id") or event.get("thread_ts"):
        return

    channel = event["channel"]
    ts = event["ts"]
    user = event["user"]
    text = event.get("text", "").strip()

    if not text:
        return

    # Strip the @-mention prefix if present
    text = re.sub(r"^<@[A-Z0-9]+>\s*", "", text).strip()
    if not text:
        return

    def reply(msg):
        client.chat_postMessage(channel=channel, thread_ts=ts, text=msg)

    reply(f"<@{user}> On it — generating code for your request...")

    try:
        response = anthropic.messages.create(
            model="claude-sonnet-4-6",
            max_tokens=4096,
            system=SYSTEM_PROMPT,
            messages=[{"role": "user", "content": text}],
        )
        result = json.loads(response.content[0].text)
        filename = result["filename"]
        code = result["code"]

        base_sha = gh_repo.get_branch(base_branch).commit.sha
        branch = f"feature/sf-{ts.replace('.', '')}"
        gh_repo.create_git_ref(f"refs/heads/{branch}", base_sha)

        try:
            existing = gh_repo.get_contents(filename, ref=base_branch)
            gh_repo.update_file(filename, result["pr_title"], code, existing.sha, branch=branch)
        except GithubException:
            gh_repo.create_file(filename, result["pr_title"], code, branch=branch)

        pr = gh_repo.create_pull(
            title=result["pr_title"],
            body=result["pr_body"],
            head=branch,
            base=base_branch,
        )
        reply(f"<@{user}> Done! :tada: {pr.html_url}")

    except Exception as e:
        reply(f"<@{user}> Something went wrong: `{e}`")


@app.event("message")
def handle_message(event, client):
    process_request(event, client)


@app.event("app_mention")
def handle_mention(event, client):
    process_request(event, client)


if __name__ == "__main__":
    SocketModeHandler(app, os.environ["SLACK_SOCKET_TOKEN"]).start()
