package bot

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestWarnIfSlackOAuthMisconfiguredIgnoresBlankOptionalConfig(t *testing.T) {
	var buf bytes.Buffer
	b := &Bot{
		cfg: Config{Env: "prod"},
		log: slog.New(slog.NewTextHandler(&buf, nil)),
	}

	b.warnIfSlackOAuthMisconfigured()

	if got := buf.String(); got != "" {
		t.Fatalf("unexpected log output for blank optional Slack config: %s", got)
	}
}

func TestWarnIfSlackOAuthMisconfiguredWarnsOnPartialConfig(t *testing.T) {
	var buf bytes.Buffer
	b := &Bot{
		cfg: Config{Env: "prod", SlackClientID: "client"},
		log: slog.New(slog.NewTextHandler(&buf, nil)),
	}

	b.warnIfSlackOAuthMisconfigured()

	got := buf.String()
	if !strings.Contains(got, "slack: HTTP transport not configured") {
		t.Fatalf("missing Slack warning in log output: %s", got)
	}
	if !strings.Contains(got, "SLACK_SIGNING_SECRET") ||
		!strings.Contains(got, "SLACK_CLIENT_SECRET") ||
		!strings.Contains(got, "SLACK_OAUTH_REDIRECT_URI") {
		t.Fatalf("warning did not list missing Slack env vars: %s", got)
	}
}
