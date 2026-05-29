package githubapp

import (
	"testing"
	"time"
)

func TestSameInts(t *testing.T) {
	cases := []struct {
		name string
		a, b []int64
		want bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", []int64{}, []int64{}, true},
		{"nil vs empty", nil, []int64{}, true},
		{"single equal", []int64{42}, []int64{42}, true},
		{"single different", []int64{42}, []int64{43}, false},
		{"length mismatch", []int64{1, 2}, []int64{1}, false},
		{"same elements same order", []int64{1, 2, 3}, []int64{1, 2, 3}, true},
		{"same elements reversed", []int64{1, 2, 3}, []int64{3, 2, 1}, true},
		{"same elements scrambled", []int64{5, 1, 7, 3}, []int64{3, 5, 7, 1}, true},
		{"one element off", []int64{1, 2, 3}, []int64{1, 2, 4}, false},
		{"duplicates equal", []int64{1, 1, 2}, []int64{1, 2, 1}, true},
		{"duplicates differ", []int64{1, 1, 2}, []int64{1, 2, 2}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameInts(tc.a, tc.b); got != tc.want {
				t.Errorf("sameInts(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestSameInts_DoesNotMutateInputs guards against future "optimization"
// that sorts the slices in place — our token-cache caller passes the
// caller's slice and would be surprised to find it reordered after a
// scope-comparison probe.
func TestSameInts_DoesNotMutateInputs(t *testing.T) {
	a := []int64{3, 1, 2}
	b := []int64{1, 2, 3}
	origA := append([]int64(nil), a...)
	origB := append([]int64(nil), b...)
	_ = sameInts(a, b)
	for i := range a {
		if a[i] != origA[i] {
			t.Errorf("sameInts mutated input a: got %v, want %v", a, origA)
			break
		}
	}
	for i := range b {
		if b[i] != origB[i] {
			t.Errorf("sameInts mutated input b: got %v, want %v", b, origB)
			break
		}
	}
}

func TestCachedTokenRequiresRequestedMinTTL(t *testing.T) {
	app := &App{tokens: newTokenCache()}
	app.tokens.entries[42] = &tokenEntry{
		token:     "ghs_cached",
		expiresAt: time.Now().Add(10 * time.Minute),
		repoIDs:   []int64{100},
	}

	if tok, _, ok := app.cachedToken(42, []int64{100}, installationTokenSafetyWindow); !ok || tok != "ghs_cached" {
		t.Fatalf("default safety window should accept cached token, got tok=%q ok=%v", tok, ok)
	}
	if tok, _, ok := app.cachedToken(42, []int64{100}, 55*time.Minute); ok || tok != "" {
		t.Fatalf("long sandbox min TTL should reject cached token, got tok=%q ok=%v", tok, ok)
	}
}
