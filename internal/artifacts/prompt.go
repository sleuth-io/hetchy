package artifacts

import "fmt"

// UploadInstructions returns the prompt block that teaches the
// in-sandbox agent how to PUT proof artifacts into pre-signed S3 slots
// stored in $HETCHY_ARTIFACT_SLOTS, and how to request more slots from
// the run-scoped endpoint.
func UploadInstructions(slotCount int) string {
	if slotCount <= 0 {
		return ""
	}
	return fmt.Sprintf(`
PROOF ARTIFACT UPLOAD - read this carefully.

The host has minted %d pre-signed S3 artifact slots for this run. They
live in $HETCHY_ARTIFACT_SLOTS as a JSON array:

  [{"kind":"screenshot","content_type":"image/png","put_url":"https://s3...","get_url":"https://s3..."}, ...]

Each slot can be used once. Pick a slot whose kind and content_type
match the artifact you produced:

  - screenshot: image/png
  - recording: video/mp4
  - diagram: image/png or image/svg+xml

Upload with curl using exactly the slot's put_url and content_type:

  SLOT_INDEX=0
  FILE=/tmp/hetchy-validate/proof.png
  CONTENT_TYPE=$(echo "$HETCHY_ARTIFACT_SLOTS" | jq -r ".[$SLOT_INDEX].content_type")
  PUT_URL=$(echo "$HETCHY_ARTIFACT_SLOTS" | jq -r ".[$SLOT_INDEX].put_url")
  curl -fSs -X PUT --data-binary @"$FILE" -H "Content-Type: $CONTENT_TYPE" "$PUT_URL"

Then link the matching get_url in the PR body:

  GET_URL=$(echo "$HETCHY_ARTIFACT_SLOTS" | jq -r ".[$SLOT_INDEX].get_url")
  # image proof: ![Validation proof]($GET_URL)
  # video proof: [Validation recording]($GET_URL)

For recordings, use whole-screen MP4 files encoded as H.264 for widest
browser compatibility. GitHub inline playback is not guaranteed for
external S3 video URLs, so include a clear markdown link even if the
video happens to render inline for you.

If the initial slots do not include the kind or count you need, request
more slots with the run-scoped endpoint:

  curl -fSs -X POST "$HETCHY_ARTIFACT_SLOT_URL" \
    -H "Authorization: Bearer $HETCHY_ARTIFACT_SLOT_TOKEN" \
    -H "Content-Type: application/json" \
    --data '{"kind":"recording","content_type":"video/mp4","count":1}' \
    > /tmp/hetchy-validate/more-slots.json

The response is another JSON array of slots. The total slot budget for
the run is capped; keep proof concise.

DO NOT reference local filenames in PR markdown. There is no host-side
rewriter, so local paths will be broken for reviewers. Link or embed
the S3 get_url directly.
`, slotCount)
}
