package billing

import (
	"errors"
	"math"
	"strings"
	"time"
)

const (
	FlavorStandard   = "standard"
	FlavorPro        = "pro"
	FlavorMax        = "max"
	FlavorEnterprise = "enterprise"
)

var ErrUnknownFlavor = errors.New("billing: unknown flavor")

type Flavor struct {
	Code       string
	Label      string
	Rank       int
	VCPU       int
	MemoryGiB  int
	DiskGiB    int
	Multiplier int
}

var flavors = map[string]Flavor{
	FlavorStandard: {
		Code: FlavorStandard, Label: "Standard", Rank: 1,
		VCPU: 2, MemoryGiB: 3, DiskGiB: 4, Multiplier: 1,
	},
	FlavorPro: {
		Code: FlavorPro, Label: "Pro", Rank: 2,
		VCPU: 4, MemoryGiB: 8, DiskGiB: 10, Multiplier: 3,
	},
	FlavorMax: {
		Code: FlavorMax, Label: "Max", Rank: 3,
		VCPU: 8, MemoryGiB: 16, DiskGiB: 25, Multiplier: 6,
	},
	// Enterprise resource tuples are ultimately account-specific. Until
	// a custom tuple is present, using Max's concrete tuple keeps admission
	// and metering deterministic while preserving the higher plan cap.
	FlavorEnterprise: {
		Code: FlavorEnterprise, Label: "Enterprise", Rank: 4,
		VCPU: 8, MemoryGiB: 16, DiskGiB: 25, Multiplier: 6,
	},
}

func ParseFlavor(code string) (Flavor, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		code = FlavorStandard
	}
	f, ok := flavors[code]
	if !ok {
		return Flavor{}, ErrUnknownFlavor
	}
	return f, nil
}

func MustFlavor(code string) Flavor {
	f, err := ParseFlavor(code)
	if err != nil {
		return flavors[FlavorStandard]
	}
	return f
}

func AllowedFlavors(maxCode string) []Flavor {
	maxFlavor := MustFlavor(maxCode)
	out := make([]Flavor, 0, len(flavors))
	for _, code := range []string{FlavorStandard, FlavorPro, FlavorMax, FlavorEnterprise} {
		f := flavors[code]
		if f.Rank <= maxFlavor.Rank {
			out = append(out, f)
		}
	}
	return out
}

func FlavorAllowed(code, maxCode string) bool {
	f, err := ParseFlavor(code)
	if err != nil {
		return false
	}
	maxFlavor := MustFlavor(maxCode)
	return f.Rank <= maxFlavor.Rank
}

func BillableCredits(start, end time.Time, multiplier int) (minutes int, credits int) {
	if multiplier < 1 {
		multiplier = 1
	}
	if !end.After(start) {
		return 0, 0
	}
	minutes = int(math.Ceil(end.Sub(start).Minutes()))
	minutes = max(minutes, 1)
	blocks := (minutes + 14) / 15
	return minutes, blocks * multiplier
}
