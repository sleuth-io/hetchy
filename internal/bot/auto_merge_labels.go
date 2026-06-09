package bot

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v66/github"
)

const (
	autoMergeSafeLabelDescription        = "Current PR head is low-risk; auto merge waits for GitHub requirements."
	autoMergeHumanReviewLabelDescription = "Current PR head was not eligible for Hetchy auto merge."
)

func applyAutoMergeLabel(ctx context.Context, client *github.Client, owner, repo string, number int, desired string, out autoMergeOutcomeDetail) autoMergeOutcomeDetail {
	labels, err := ensureAutoMergeLabelState(ctx, client, owner, repo, number, desired)
	if err != nil {
		out.BlockedReason = "apply auto merge label: " + err.Error()
		out.LabelsApplied = labels
		return out
	}
	out.LabelsApplied = labels
	return out
}

func autoMergeLabelFailed(out autoMergeOutcomeDetail) bool {
	return strings.HasPrefix(out.BlockedReason, "apply auto merge label:")
}

func (b *Bot) applyAutoMergeHumanLabelBestEffort(ctx context.Context, orgID, prURL string, out autoMergeOutcomeDetail) autoMergeOutcomeDetail {
	parsed, err := parseGitHubPRURL(prURL)
	if err != nil {
		return out
	}
	client, err := b.githubClientForAutoMerge(ctx, orgID, parsed.Owner, parsed.Repo)
	if err != nil {
		return out
	}
	return applyAutoMergeLabel(ctx, client, parsed.Owner, parsed.Repo, parsed.Number, autoMergeHumanReviewLabel, out)
}

func ensureAutoMergeLabelState(ctx context.Context, client *github.Client, owner, repo string, number int, desired string) ([]string, error) {
	desired, opposite, err := autoMergeLabelPair(desired)
	if err != nil {
		return nil, err
	}
	if err := ensureAutoMergeLabel(ctx, client, owner, repo, autoMergeSafeLabel, "2da44e", autoMergeSafeLabelDescription); err != nil {
		return nil, err
	}
	if err := ensureAutoMergeLabel(ctx, client, owner, repo, autoMergeHumanReviewLabel, "d73a4a", autoMergeHumanReviewLabelDescription); err != nil {
		return nil, err
	}
	if _, err := client.Issues.RemoveLabelForIssue(ctx, owner, repo, number, opposite); err != nil && githubHTTPStatus(nil, err) != http.StatusNotFound {
		return nil, err
	}
	if _, _, err := client.Issues.AddLabelsToIssue(ctx, owner, repo, number, []string{desired}); err != nil {
		return nil, err
	}
	return []string{desired}, nil
}

func autoMergeLabelPair(desired string) (string, string, error) {
	switch desired {
	case autoMergeSafeLabel:
		return autoMergeSafeLabel, autoMergeHumanReviewLabel, nil
	case autoMergeHumanReviewLabel:
		return autoMergeHumanReviewLabel, autoMergeSafeLabel, nil
	default:
		return "", "", fmt.Errorf("unknown auto merge label %q", desired)
	}
}

func ensureAutoMergeLabel(ctx context.Context, client *github.Client, owner, repo, name, color, description string) error {
	if _, resp, err := client.Issues.GetLabel(ctx, owner, repo, name); err == nil {
		return nil
	} else if githubHTTPStatus(resp, err) != http.StatusNotFound {
		return err
	}
	_, _, err := client.Issues.CreateLabel(ctx, owner, repo, &github.Label{
		Name:        stringPtr(name),
		Color:       stringPtr(color),
		Description: stringPtr(description),
	})
	return err
}

func autoMergePullRequestOptions(headSHA string) *github.PullRequestOptions {
	// Leave MergeMethod empty so GitHub applies the repository's configured default.
	return &github.PullRequestOptions{SHA: headSHA}
}
