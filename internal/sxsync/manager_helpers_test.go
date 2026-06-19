package sxsync

import (
	"archive/zip"
	"bytes"
	"testing"

	sxlib "github.com/sleuth-io/sx/pkg/sxvault"
)

func testSkillZip(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Write([]byte("---\nname: " + name + "\ndescription: Test skill.\n---\n\nUse this skill."))
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func botHasDirectSkill(bots []sxlib.BotSummary, botName, skillName string) bool {
	for _, bot := range bots {
		if bot.Name != botName {
			continue
		}
		for _, skill := range bot.InstalledSkills {
			if skill.Name == skillName && skill.IsDirectInstall {
				return true
			}
		}
	}
	return false
}
