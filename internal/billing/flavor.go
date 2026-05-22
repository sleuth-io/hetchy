package billing

import (
	"errors"
	"math"
	"strings"
	"time"
)

const (
	FlavorStandard = "standard"
	FlavorPlus     = "plus"
	// FlavorPro, FlavorMax, and FlavorEnterprise are legacy/deferred codes.
	// They normalize to Plus until Daytona account limits support larger
	// launch flavors.
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

var (
	standardFlavor = Flavor{
		Code: FlavorStandard, Label: "Standard", Rank: 1,
		VCPU: 2, MemoryGiB: 6, DiskGiB: 10, Multiplier: 1,
	}
	plusFlavor = Flavor{
		Code: FlavorPlus, Label: "Plus", Rank: 2,
		VCPU: 4, MemoryGiB: 8, DiskGiB: 10, Multiplier: 2,
	}
	flavors = map[string]Flavor{
		FlavorStandard:   standardFlavor,
		FlavorPlus:       plusFlavor,
		FlavorPro:        plusFlavor,
		FlavorMax:        plusFlavor,
		FlavorEnterprise: plusFlavor,
	}
	launchFlavorOrder = []Flavor{standardFlavor, plusFlavor}
)

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
	out := make([]Flavor, 0, len(launchFlavorOrder))
	for _, f := range launchFlavorOrder {
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
