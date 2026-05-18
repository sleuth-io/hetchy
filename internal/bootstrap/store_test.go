package bootstrap

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPostgresTextSanitizesInvalidUTF8(t *testing.T) {
	raw := "setup output\n" + string([]byte{0xe2, 0x80, 0x5b}) + "\n"
	got := postgresText(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("postgresText returned invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "\uFFFD[") {
		t.Fatalf("invalid bytes were not replaced: %q", got)
	}
}
