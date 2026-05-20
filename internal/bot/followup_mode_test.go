package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func TestRequestFollowUpModeParsesAnthropicJSON(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		body = string(bodyBytes)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"mode\":\"answer_only\",\"confidence\":0.93,\"reason\":\"simple greeting\"}"}]}`))
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	decision, err := requestFollowUpMode(context.Background(), orgcfg.Config{AnthropicAPIKey: "sk-ant"}, convstore.Record{
		ThreadID: "thread-1",
		Branch:   "feature/demo",
		PRURL:    "https://github.com/acme/repo/pull/1",
		History:  []string{"Add README art"},
	}, "just say hi")
	if err != nil {
		t.Fatalf("requestFollowUpMode: %v", err)
	}
	if decision.Mode != followUpModeAnswerOnly || decision.Confidence != 0.93 {
		t.Fatalf("decision = %+v", decision)
	}
	if !strings.Contains(body, "just say hi") || !strings.Contains(body, "Add README art") {
		t.Fatalf("classifier request body missing context: %s", body)
	}
}

func TestDecideFollowUpModeDefaultsLowConfidenceNonChangeToChange(t *testing.T) {
	b := &Bot{
		log: discardLogger(),
		followUpModeFn: func(context.Context, orgcfg.Config, convstore.Record, string) followUpModeDecision {
			return followUpModeDecision{Mode: followUpModeInspect, Confidence: 0.2, Reason: "unclear"}
		},
	}
	decision := b.decideFollowUpMode(context.Background(), orgcfg.Config{}, convstore.Record{}, "maybe do something")
	if decision.Mode != followUpModeChange {
		t.Fatalf("mode = %q, want change", decision.Mode)
	}
}

func TestDecideFollowUpModeForcesChangeForMissingProofRemediation(t *testing.T) {
	b := &Bot{
		log: discardLogger(),
		followUpModeFn: func(context.Context, orgcfg.Config, convstore.Record, string) followUpModeDecision {
			return followUpModeDecision{Mode: followUpModeInspect, Confidence: 0.95, Reason: "looks like inspection"}
		},
	}
	decision := b.decideFollowUpMode(context.Background(), orgcfg.Config{}, convstore.Record{}, "you didn't attach proof of your change to the PR")
	if decision.Mode != followUpModeChange {
		t.Fatalf("mode = %q, want change", decision.Mode)
	}
	if !strings.Contains(decision.Reason, "remediation") {
		t.Fatalf("reason = %q, want remediation override", decision.Reason)
	}
}

func TestPriorWorkRemediationRequestAvoidsCommitInspectFalsePositive(t *testing.T) {
	if isPriorWorkRemediationRequest("what's missing in this commit message?") {
		t.Fatal("commit inspection question should not force change mode")
	}
	if !isPriorWorkRemediationRequest("the PR body is missing proof") {
		t.Fatal("missing proof in PR body should force change mode")
	}
}

func TestFollowUpModeTimeoutAllowsRoutineLLMLatency(t *testing.T) {
	if followUpModeTimeout < 8*time.Second {
		t.Fatalf("followUpModeTimeout = %s, want at least 8s", followUpModeTimeout)
	}
}

func TestFollowUpModeSystemPromptTreatsPriorDeliverableComplaintsAsChange(t *testing.T) {
	for _, want := range []string{
		"missing prior-run deliverables",
		"you didn't attach proof",
		"the PR body lacks evidence",
		"rerun validation",
	} {
		if !strings.Contains(followUpModeSystemPrompt, want) {
			t.Fatalf("follow-up mode system prompt missing %q\n%s", want, followUpModeSystemPrompt)
		}
	}
}

func TestBuildFollowUpPromptAnswerOnlyOmitsMutationInstructions(t *testing.T) {
	prompt := buildFollowUpPrompt("owner/repo", convstore.Record{
		Branch:  "feature/demo",
		PRURL:   "https://github.com/owner/repo/pull/1",
		History: []string{"Ship a feature"},
	}, "just say hi", nil, 0, defaultChatTaskOptions(), followUpModeAnswerOnly)

	for _, bad := range []string{"When you are done implementing", "POST-CHANGE VALIDATION", "Push the branch", "gh pr checks"} {
		if strings.Contains(prompt, bad) {
			t.Fatalf("answer-only prompt contains mutation instruction %q:\n%s", bad, prompt)
		}
	}
	if !strings.Contains(prompt, "answer-only") || !strings.Contains(prompt, "just say hi") {
		t.Fatalf("answer-only prompt missing expected context:\n%s", prompt)
	}
}
