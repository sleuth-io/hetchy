package bot

import "strings"

func bootstrapSkippedMessage(err error) string {
	const base = "Couldn't auto-bootstrap this repo for end-to-end validation — running the agent without a validation spec."
	if err == nil {
		return base + " Check server logs for details."
	}
	return base + "\n\nReason: " + bootstrapErrorSummary(err) + "."
}

func bootstrapErrorSummary(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "invalid byte sequence for encoding"):
		// Current store writes sanitize bootstrap text fields before Postgres.
		// Keep this classification for older deployments or future save paths
		// that may still surface the raw SQLSTATE 22021 error.
		return "could not save the generated spec because captured sandbox output contained invalid UTF-8"
	case strings.Contains(msg, "save spec"):
		// SQLSTATE 22021 errors include both substrings, so keep
		// "invalid byte sequence" above this generic save failure.
		return "could not save the generated spec"
	case strings.Contains(msg, "parse manifest"):
		return "the generated manifest was not valid JSON"
	case strings.Contains(msg, "read manifest"):
		return "could not read the generated manifest from the sandbox"
	case strings.Contains(msg, "read setup.sh"):
		return "could not read the generated setup script from the sandbox"
	case strings.Contains(msg, "read start.sh"):
		return "could not read the generated start script from the sandbox"
	case strings.Contains(msg, "read health.sh"):
		return "could not read the generated health check from the sandbox"
	case strings.Contains(msg, "setup-clone"):
		return "could not prepare the repository checkout for bootstrap"
	case strings.Contains(msg, "detect"):
		return "could not inspect the repository for bootstrap hints"
	default:
		return "bootstrap returned an internal error; check server logs for details"
	}
}
