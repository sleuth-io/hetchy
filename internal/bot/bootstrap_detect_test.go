package bot

import "testing"

func TestIsNotBase64(t *testing.T) {
	valid := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="
	for _, r := range valid {
		if isNotBase64(r) {
			t.Fatalf("isNotBase64(%q) = true, want false", r)
		}
	}
	for _, r := range " \n-_" {
		if !isNotBase64(r) {
			t.Fatalf("isNotBase64(%q) = false, want true", r)
		}
	}
}
