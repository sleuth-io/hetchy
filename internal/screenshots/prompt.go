package screenshots

import "fmt"

// UploadInstructions returns the prompt block that teaches the in-sandbox
// agent how to PUT screenshots into the pre-signed S3 slots stored in
// $HETCHY_SCREENSHOT_SLOTS, then embed the matching GET URLs in PR
// markdown. Pass slotCount = len(slots) from MintSlots.
//
// Callers append this verbatim to whichever prompt they ship (initial
// run, follow-up, etc.) so the wording is identical across surfaces.
// When slotCount is zero an empty string is returned — the prompt the
// agent receives in that case is context-specific (validation wants a
// "describe what you saw in summary.md" fallback; follow-ups want no
// guidance at all), so the caller owns it.
func UploadInstructions(slotCount int) string {
	if slotCount <= 0 {
		return ""
	}
	return fmt.Sprintf(`
SCREENSHOT UPLOAD — read this carefully.

The host has minted %d pre-signed S3 URL pairs for you. They live in
the env var $HETCHY_SCREENSHOT_SLOTS as a JSON array:

  [{"put_url":"https://s3...","get_url":"https://s3..."}, ...]

For each screenshot you want in the PR body or a PR comment:

  1. Take the screenshot via Playwright MCP and save it locally,
     e.g. /tmp/shot-light.png.
  2. Pick the next unused slot index N (start at 0, never reuse).
  3. Upload with curl, taking exactly the put_url for that slot:

       PUT_URL=$(echo "$HETCHY_SCREENSHOT_SLOTS" | jq -r ".[$N].put_url")
       curl -fSs -X PUT --data-binary @/tmp/shot-light.png \
            -H "Content-Type: image/png" "$PUT_URL"

  4. Embed the matching get_url in the markdown:

       GET_URL=$(echo "$HETCHY_SCREENSHOT_SLOTS" | jq -r ".[$N].get_url")
       # in your PR body / comment: ![Light mode]($GET_URL)

The PUT URLs accept exactly one upload each and expire 30 minutes
from the start of this task. Don't try to re-upload a slot. The GET
URLs render in the PR markdown for 7 days; that's enough to cover
review turnaround.

DO NOT reference local filenames like screenshot-001.png in the PR
markdown — there is no host-side rewriter and those links will be
broken. Embed the get_url directly, or skip the image entirely if
you can't upload.
`, slotCount)
}
