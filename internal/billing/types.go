package billing

import "time"

const (
	PlanFree       = "free"
	PlanTrial      = "trial"
	PlanStarter    = "starter"
	PlanTeam       = "team"
	PlanGrowth     = "growth"
	PlanBusiness   = "business"
	PlanStandard   = "standard"
	PlanPro        = "pro"
	PlanMax        = "max"
	PlanEnterprise = "enterprise"
)

const (
	ReservationReserved = "reserved"
	ReservationCaptured = "captured"
	ReservationReleased = "released"
	ReservationComped   = "comped"
)

const TopupUnitCredits = 10

type Account struct {
	OrgID                string
	StripeCustomerID     string
	StripeSubscriptionID string
	PlanCode             string
	Status               string
	CurrentPeriodStart   time.Time
	CurrentPeriodEnd     time.Time
	IncludedCredits      int
	IncludedCreditsUsed  int
	TopupCredits         int
	MaxFlavor            string
	PerRunMaxCredits     int
	BillingExempt        bool
	LastPaymentError     string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (a Account) IncludedRemaining() int {
	return max(a.IncludedCredits-a.IncludedCreditsUsed, 0)
}

func (a Account) Balance() int {
	return a.IncludedRemaining() + a.TopupCredits
}

type TopupSettings struct {
	OrgID              string
	AutoTopupEnabled   bool
	TriggerThreshold   int
	TargetBalance      int
	MonthlyMaxUnits    int
	MonthlyUnitsUsed   int
	MonthlyAnchorMonth string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Reservation struct {
	RunID               string
	OrgID               string
	ReservedCredits     int
	FromIncludedCredits int
	FromTopupCredits    int
	CapturedCredits     int
	ReleasedCredits     int
	Status              string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type RunMeter struct {
	RunID            string
	OrgID            string
	Flavor           string
	Multiplier       int
	SandboxVCPU      int
	SandboxMemoryGiB int
	SandboxDiskGiB   int
	StartedAt        time.Time
	EndedAt          time.Time
	BillableMinutes  int
	CapturedCredits  int
	TerminalState    string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type RepoSetting struct {
	OrgID       string
	GitHubOwner string
	GitHubRepo  string
	Flavor      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Overview struct {
	Account        Account
	TopupSettings  TopupSettings
	RecentMeters   []RunMeter
	AllowedFlavors []Flavor
}

type AccountMirror struct {
	OrgID                string
	StripeCustomerID     string
	StripeSubscriptionID string
	PlanCode             string
	Status               string
	CurrentPeriodStart   time.Time
	CurrentPeriodEnd     time.Time
	IncludedCredits      int
	MaxFlavor            string
	PerRunMaxCredits     int
	BillingExempt        bool
	LastPaymentError     string
}

type AdmissionRequest struct {
	OrgID       string
	RunID       string
	GitHubOwner string
	GitHubRepo  string
	StartedAt   time.Time
}

type Admission struct {
	Account          Account
	Flavor           Flavor
	ReservedCredits  int
	Comped           bool
	AvailableCredits int
}
