package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

func newBillingFakeDB() *billingFakeDB {
	return &billingFakeDB{
		queryRow: map[string]pgx.Row{},
		query:    map[string]pgx.Rows{},
		exec:     map[string]billingExecResult{},
	}
}

func newBillingStoreForFake(fake *billingFakeDB) *Store {
	return NewStore(&db.Store{Queries: sqlc.New(fake)})
}

type billingFakeDB struct {
	queryRow map[string]pgx.Row
	query    map[string]pgx.Rows
	exec     map[string]billingExecResult

	queryRowCalls []billingDBCall
	queryCalls    []billingDBCall
	execCalls     []billingDBCall
}

type billingDBCall struct {
	sql  string
	args []any
}

type billingExecResult struct {
	rows int64
	err  error
}

func (f *billingFakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls = append(f.execCalls, billingDBCall{sql: sql, args: args})
	for frag, res := range f.exec {
		if strings.Contains(sql, frag) {
			if res.err != nil {
				return pgconn.CommandTag{}, res.err
			}
			return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", res.rows)), nil
		}
	}
	return pgconn.CommandTag{}, fmt.Errorf("unexpected exec: %s", sql)
}

func (f *billingFakeDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	f.queryCalls = append(f.queryCalls, billingDBCall{sql: sql, args: args})
	for frag, rows := range f.query {
		if strings.Contains(sql, frag) {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("unexpected query: %s", sql)
}

func (f *billingFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.queryRowCalls = append(f.queryRowCalls, billingDBCall{sql: sql, args: args})
	for frag, row := range f.queryRow {
		if strings.Contains(sql, frag) {
			return row
		}
	}
	return billingErrRow{err: fmt.Errorf("unexpected query row: %s", sql)}
}

func (f *billingFakeDB) queryRowCallCount(fragment string) int {
	count := 0
	for _, call := range f.queryRowCalls {
		if strings.Contains(call.sql, fragment) {
			count++
		}
	}
	return count
}

func (f *billingFakeDB) execCallCount(fragment string) int {
	count := 0
	for _, call := range f.execCalls {
		if strings.Contains(call.sql, fragment) {
			count++
		}
	}
	return count
}

func (f *billingFakeDB) onlyExecCall(t *testing.T, fragment string) billingDBCall {
	t.Helper()
	var matches []billingDBCall
	for _, call := range f.execCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("exec calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

func (f *billingFakeDB) onlyQueryRowCall(t *testing.T, fragment string) billingDBCall {
	t.Helper()
	var matches []billingDBCall
	for _, call := range f.queryRowCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("query row calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

func (f *billingFakeDB) onlyQueryCall(t *testing.T, fragment string) billingDBCall {
	t.Helper()
	var matches []billingDBCall
	for _, call := range f.queryCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("query calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

func assertArg(t *testing.T, args []any, idx int, want any) {
	t.Helper()
	if len(args) <= idx {
		t.Fatalf("arg[%d] missing from %v", idx, args)
	}
	if got := args[idx]; got != want {
		t.Fatalf("arg[%d] = %#v (%T), want %#v (%T)", idx, got, got, want, want)
	}
}

type billingErrRow struct {
	err error
}

func (r billingErrRow) Scan(...any) error {
	return r.err
}

type billingScanRow struct {
	values []any
}

func (r billingScanRow) Scan(dest ...any) error {
	return billingAssignScan(dest, r.values)
}

type billingRows struct {
	rows   []billingScanRow
	idx    int
	closed bool
}

func (r *billingRows) Close()                                       { r.closed = true }
func (r *billingRows) Err() error                                   { return nil }
func (r *billingRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *billingRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *billingRows) Values() ([]any, error)                       { return nil, nil }
func (r *billingRows) RawValues() [][]byte                          { return nil }
func (r *billingRows) Conn() *pgx.Conn                              { return nil }

func (r *billingRows) Next() bool {
	if r.idx >= len(r.rows) {
		r.Close()
		return false
	}
	r.idx++
	return true
}

func (r *billingRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.rows) {
		return errors.New("scan called before Next or after EOF")
	}
	return r.rows[r.idx-1].Scan(dest...)
}

func billingAssignScan(dest, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(values))
	}
	for i := range dest {
		if err := billingAssignScanValue(dest[i], values[i]); err != nil {
			return fmt.Errorf("scan dest %d: %w", i, err)
		}
	}
	return nil
}

func billingAssignScanValue(dest, value any) error {
	switch d := dest.(type) {
	case *string:
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("got %T, want string", value)
		}
		*d = v
	case *bool:
		v, ok := value.(bool)
		if !ok {
			return fmt.Errorf("got %T, want bool", value)
		}
		*d = v
	case *int32:
		v, ok := value.(int32)
		if !ok {
			return fmt.Errorf("got %T, want int32", value)
		}
		*d = v
	case *pgtype.Timestamptz:
		v, ok := value.(pgtype.Timestamptz)
		if !ok {
			return fmt.Errorf("got %T, want pgtype.Timestamptz", value)
		}
		*d = v
	default:
		return fmt.Errorf("unsupported scan destination %T", dest)
	}
	return nil
}

func testBillingAccount(orgID string, mutate func(*sqlc.BillingAccount)) sqlc.BillingAccount {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	account := sqlc.BillingAccount{
		OrgID:                  orgID,
		PlanCode:               PlanStudio,
		Status:                 "active",
		CurrentPeriodStart:     makeTimestamptz(now.Add(-24 * time.Hour)),
		CurrentPeriodEnd:       makeTimestamptz(now.Add(30 * 24 * time.Hour)),
		IncludedCredits:        500,
		IncludedCreditsUsed:    50,
		TopupCredits:           10,
		MaxFlavor:              FlavorPlus,
		PerRunMaxCredits:       12,
		CreatedAt:              makeTimestamptz(now),
		UpdatedAt:              makeTimestamptz(now),
		PendingPlanEffectiveAt: pgtype.Timestamptz{},
	}
	if mutate != nil {
		mutate(&account)
	}
	return account
}

func billingAccountRow(account sqlc.BillingAccount) billingScanRow {
	return billingScanRow{values: []any{
		account.OrgID,
		account.StripeCustomerID,
		account.StripeSubscriptionID,
		account.PlanCode,
		account.Status,
		account.CurrentPeriodStart,
		account.CurrentPeriodEnd,
		account.IncludedCredits,
		account.IncludedCreditsUsed,
		account.TopupCredits,
		account.MaxFlavor,
		account.PerRunMaxCredits,
		account.BillingExempt,
		account.LastPaymentError,
		account.CreatedAt,
		account.UpdatedAt,
		account.PendingPlanCode,
		account.PendingPlanEffectiveAt,
	}}
}

func testTopupSettings(orgID string, mutate func(*sqlc.BillingTopupSetting)) sqlc.BillingTopupSetting {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	settings := sqlc.BillingTopupSetting{
		OrgID:                 orgID,
		AutoTopupEnabled:      true,
		TriggerThreshold:      20,
		TargetBalance:         100,
		MonthlyMaxUnits:       5,
		MonthlyUnitsUsed:      1,
		MonthlyAnchorMonth:    currentBillingMonth(time.Now()),
		CreatedAt:             makeTimestamptz(now),
		UpdatedAt:             makeTimestamptz(now),
		MonthlyMaxCents:       10000,
		MonthlySpendCentsUsed: 1500,
	}
	if mutate != nil {
		mutate(&settings)
	}
	return settings
}

func billingTopupSettingRow(settings sqlc.BillingTopupSetting) billingScanRow {
	return billingScanRow{values: []any{
		settings.OrgID,
		settings.AutoTopupEnabled,
		settings.TriggerThreshold,
		settings.TargetBalance,
		settings.MonthlyMaxUnits,
		settings.MonthlyUnitsUsed,
		settings.MonthlyAnchorMonth,
		settings.CreatedAt,
		settings.UpdatedAt,
		settings.MonthlyMaxCents,
		settings.MonthlySpendCentsUsed,
	}}
}

func testRepoSetting(orgID, owner, repo, flavor string) sqlc.RepoBillingSetting {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return sqlc.RepoBillingSetting{
		OrgID:       orgID,
		GithubOwner: owner,
		GithubRepo:  repo,
		Flavor:      flavor,
		CreatedAt:   makeTimestamptz(now),
		UpdatedAt:   makeTimestamptz(now),
	}
}

func billingRepoSettingRow(setting sqlc.RepoBillingSetting) billingScanRow {
	return billingScanRow{values: []any{
		setting.OrgID,
		setting.GithubOwner,
		setting.GithubRepo,
		setting.Flavor,
		setting.CreatedAt,
		setting.UpdatedAt,
	}}
}

func testReservation(runID, orgID string, mutate func(*sqlc.BillingCreditReservation)) sqlc.BillingCreditReservation {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	reservation := sqlc.BillingCreditReservation{
		RunID:     runID,
		OrgID:     orgID,
		Status:    ReservationReserved,
		CreatedAt: makeTimestamptz(now),
		UpdatedAt: makeTimestamptz(now),
	}
	if mutate != nil {
		mutate(&reservation)
	}
	return reservation
}

func billingReservationRow(reservation sqlc.BillingCreditReservation) billingScanRow {
	return billingScanRow{values: []any{
		reservation.RunID,
		reservation.OrgID,
		reservation.ReservedCredits,
		reservation.FromIncludedCredits,
		reservation.FromTopupCredits,
		reservation.CapturedCredits,
		reservation.ReleasedCredits,
		reservation.Status,
		reservation.CreatedAt,
		reservation.UpdatedAt,
	}}
}

func testRunMeter(runID, orgID, flavor string, mutate ...func(*sqlc.BillingRunMeter)) sqlc.BillingRunMeter {
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	meter := sqlc.BillingRunMeter{
		RunID:            runID,
		OrgID:            orgID,
		Flavor:           flavor,
		Multiplier:       int32(MustFlavor(flavor).Multiplier),
		SandboxVcpu:      int32(MustFlavor(flavor).VCPU),
		SandboxMemoryGib: int32(MustFlavor(flavor).MemoryGiB),
		SandboxDiskGib:   int32(MustFlavor(flavor).DiskGiB),
		StartedAt:        makeTimestamptz(start),
		EndedAt:          makeTimestamptz(start.Add(30 * time.Minute)),
		BillableMinutes:  30,
		CapturedCredits:  2,
		TerminalState:    "success",
		CreatedAt:        makeTimestamptz(start),
		UpdatedAt:        makeTimestamptz(start),
	}
	for _, fn := range mutate {
		if fn != nil {
			fn(&meter)
		}
	}
	return meter
}

func billingRunMeterRow(meter sqlc.BillingRunMeter) billingScanRow {
	return billingScanRow{values: []any{
		meter.RunID,
		meter.OrgID,
		meter.Flavor,
		meter.Multiplier,
		meter.SandboxVcpu,
		meter.SandboxMemoryGib,
		meter.SandboxDiskGib,
		meter.StartedAt,
		meter.EndedAt,
		meter.BillableMinutes,
		meter.CapturedCredits,
		meter.TerminalState,
		meter.CreatedAt,
		meter.UpdatedAt,
	}}
}
