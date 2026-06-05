package migrations

import (
	"strconv"
	"strings"
	"testing"
)

func TestExpectedVersionMatchesLatestUpMigration(t *testing.T) {
	const minimumExpectedVersion = 20260603150000

	entries, err := sqlFS.ReadDir(".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}

	var want uint64
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		versionText, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Fatalf("migration %q has no version separator", name)
		}
		version, err := strconv.ParseUint(versionText, 10, 64)
		if err != nil {
			t.Fatalf("parse migration version %q: %v", name, err)
		}
		if version > want {
			want = version
		}
	}
	if want == 0 {
		t.Fatal("no up migrations found")
	}

	got, err := ExpectedVersion()
	if err != nil {
		t.Fatalf("ExpectedVersion: %v", err)
	}
	if got != uint(want) {
		t.Fatalf("ExpectedVersion = %d, want %d", got, want)
	}
	if got < minimumExpectedVersion {
		t.Fatalf("ExpectedVersion = %d, suspiciously low (want >= %d)", got, minimumExpectedVersion)
	}
}
