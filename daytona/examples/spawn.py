"""End-to-end smoke test: spawn a sandbox, run Claude Code, start a web app,
drive it with Playwright, and print a preview URL for human review.

Prereqs:
  pip install daytona     (or: uv run --with daytona python examples/spawn.py)
  export DAYTONA_API_KEY=...
  export DAYTONA_API_URL=http://localhost:3000/api
  export ANTHROPIC_API_KEY=...   # forwarded into the sandbox below

The exact SDK surface evolves quickly — if a call here mismatches your
installed daytona version, check `python -c "import daytona; help(daytona)"`
or https://www.daytona.io/docs.
"""

import os
import textwrap
import time

from daytona import CreateSandboxFromSnapshotParams, Daytona

SNAPSHOT = os.environ.get("SNAPSHOT", "claude-playwright:1")
ANTHROPIC_KEY = os.environ["ANTHROPIC_API_KEY"]


def main() -> None:
    client = Daytona()  # picks up DAYTONA_API_KEY / DAYTONA_API_URL from env

    print(f">> creating sandbox from snapshot {SNAPSHOT}")
    sb = client.create(
        CreateSandboxFromSnapshotParams(
            snapshot=SNAPSHOT,
            env_vars={"ANTHROPIC_API_KEY": ANTHROPIC_KEY},
        )
    )
    print(f"   sandbox id: {sb.id}")

    try:
        # 1. Have Claude Code scaffold a tiny FastAPI app inside the sandbox.
        print(">> asking claude code to scaffold a fastapi app")
        prompt = (
            "Create a FastAPI app at /home/daytona/app/main.py with a single "
            "GET / route that returns {'hello': 'world'}. Then write a "
            "requirements.txt with fastapi and uvicorn. Do not run the server."
        )
        sb.process.exec(f"claude --print {prompt!r}", timeout=180)

        # 2. Install deps and start the server in the background.
        print(">> installing deps + starting uvicorn")
        sb.process.exec(
            "cd /home/daytona/app && pip install -q -r requirements.txt"
        )
        sb.process.exec(
            "cd /home/daytona/app && nohup uvicorn main:app "
            "--host 0.0.0.0 --port 3000 > /tmp/uvicorn.log 2>&1 &"
        )
        time.sleep(2)

        # 3. Drive it with Playwright from inside the sandbox.
        print(">> driving the app with playwright")
        playwright_script = textwrap.dedent("""
            const { chromium } = require('playwright');
            (async () => {
              const browser = await chromium.launch();
              const page = await browser.newPage();
              const resp = await page.goto('http://localhost:3000/');
              console.log('STATUS', resp.status());
              console.log('BODY', await page.content());
              await browser.close();
            })().catch(e => { console.error(e); process.exit(1); });
        """).strip()
        sb.process.exec(
            "node -e " + repr(playwright_script),
            timeout=60,
        )

        # 4. Get a preview URL so a human can click through.
        preview = sb.get_preview_link(3000)
        print(f">> preview url: {preview.url}")
        print(f"   x-daytona-preview-token: {preview.token}")

        print("\nSandbox left running. Stop it with:")
        print(f"  python -c \"from daytona import Daytona; "
              f"Daytona().get('{sb.id}').delete()\"")
    except Exception:
        sb.delete()
        raise


if __name__ == "__main__":
    main()
