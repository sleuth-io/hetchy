package billing

import (
	"testing"
	"time"
)

func TestBillableCredits(t *testing.T) {
	start := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		d          time.Duration
		multiplier int
		minutes    int
		credits    int
	}{
		{name: "zero", d: 0, multiplier: 1, minutes: 0, credits: 0},
		{name: "one second standard", d: time.Second, multiplier: 1, minutes: 1, credits: 1},
		{name: "fifteen minutes standard", d: 15 * time.Minute, multiplier: 1, minutes: 15, credits: 1},
		{name: "fifteen plus one second standard", d: 15*time.Minute + time.Second, multiplier: 1, minutes: 16, credits: 2},
		{name: "pro multiplier", d: 16 * time.Minute, multiplier: 3, minutes: 16, credits: 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			minutes, credits := BillableCredits(start, start.Add(tc.d), tc.multiplier)
			if minutes != tc.minutes || credits != tc.credits {
				t.Fatalf("BillableCredits = (%d, %d), want (%d, %d)", minutes, credits, tc.minutes, tc.credits)
			}
		})
	}
}

func TestFlavorCaps(t *testing.T) {
	if !FlavorAllowed(FlavorStandard, FlavorStandard) {
		t.Fatal("standard should be allowed by standard cap")
	}
	if FlavorAllowed(FlavorPro, FlavorStandard) {
		t.Fatal("pro should not be allowed by standard cap")
	}
	if !FlavorAllowed(FlavorMax, FlavorEnterprise) {
		t.Fatal("max should be allowed by enterprise cap")
	}
	allowed := AllowedFlavors(FlavorPro)
	if len(allowed) != 2 || allowed[0].Code != FlavorStandard || allowed[1].Code != FlavorPro {
		t.Fatalf("AllowedFlavors(pro) = %#v", allowed)
	}
}
