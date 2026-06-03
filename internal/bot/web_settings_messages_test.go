package bot

import "testing"

func TestSavedMessage(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"unknown":                    "",
		"1":                          "Settings saved.",
		"invited":                    "Invitation sent.",
		"revoked":                    "Invitation revoked.",
		"removed":                    "Member removed.",
		"role":                       "Role updated.",
		"slack_installed":            "Slack installed.",
		"slack_install_cancelled":    "Slack install cancelled.",
		"slack_install_conflict":     "That Slack workspace is already connected to another Hetchy organization. Have the existing org uninstall first.",
		"github_installed":           "GitHub App installed. Repos and teams have been synced.",
		"github_synced":              "Sync complete.",
		"github_install_conflict":    "That GitHub installation is already connected to another Hetchy organization. Have the existing org uninstall first (or pick a different account).",
		"github_disconnected":        "GitHub installation removed. The Hetchy GitHub App has been uninstalled from that account.",
		"slack_disconnected":         "Slack disconnected. The Hetchy app has been removed from that workspace.",
		"slack_already_disconnected": "Slack was already disconnected.",
		"agent_saved":                "Agent saved.",
		"agent_skill_removed":        "Skill removed.",
		"agent_team_added":           "Team added.",
		"agent_team_removed":         "Team removed.",
		"agent_deleted":              "Agent deleted.",
		"job_saved":                  "Job saved.",
		"job_started":                "Job started.",
		"job_deleted":                "Job deleted.",
	}
	for in, want := range cases {
		if got := savedMessage(in); got != want {
			t.Errorf("savedMessage(%q) = %q, want %q", in, got, want)
		}
	}
}
