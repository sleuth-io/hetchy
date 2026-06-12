package billing

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestNewStore(t *testing.T) {
	s := NewStore(nil)
	if s == nil {
		t.Fatal("NewStore returned nil")
	}
	if s.db != nil {
		t.Error("NewStore(nil): expected nil db")
	}
}

func TestStoreEnabledNilStore(t *testing.T) {
	var s *Store
	if s.Enabled() {
		t.Error("nil Store should not be enabled")
	}
}

func TestStoreEnabledNilDB(t *testing.T) {
	s := &Store{db: nil}
	if s.Enabled() {
		t.Error("Store with nil db should not be enabled")
	}
}

func TestTimestamptz(t *testing.T) {
	zero := timestamptz(time.Time{})
	if zero.Valid {
		t.Error("timestamptz(zero time) should return invalid Timestamptz")
	}

	ts := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	pg := timestamptz(ts)
	if !pg.Valid {
		t.Error("timestamptz(non-zero) should return valid Timestamptz")
	}
	if !pg.Time.Equal(ts) {
		t.Errorf("timestamptz: time = %v, want %v", pg.Time, ts)
	}
}

func TestPgTime(t *testing.T) {
	got := pgTime(pgtype.Timestamptz{Valid: false})
	if !got.IsZero() {
		t.Errorf("pgTime(invalid): expected zero time, got %v", got)
	}

	ts := time.Date(2026, 3, 10, 8, 30, 0, 0, time.UTC)
	got = pgTime(pgtype.Timestamptz{Time: ts, Valid: true})
	if !got.Equal(ts) {
		t.Errorf("pgTime(valid): got %v, want %v", got, ts)
	}
}

func TestCurrentBillingMonth(t *testing.T) {
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{
			name: "specific date",
			t:    time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC),
			want: "2026-03",
		},
		{
			name: "first of month",
			t:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: "2026-01",
		},
		{
			name: "december",
			t:    time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC),
			want: "2025-12",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := currentBillingMonth(tc.t)
			if got != tc.want {
				t.Errorf("currentBillingMonth(%v) = %q, want %q", tc.t, got, tc.want)
			}
		})
	}
}

func TestCurrentBillingMonthZeroUsesNow(t *testing.T) {
	got := currentBillingMonth(time.Time{})
	// Just verify it returns a non-empty string in "YYYY-MM" format
	if len(got) != 7 || got[4] != '-' {
		t.Errorf("currentBillingMonth(zero) = %q, want YYYY-MM format", got)
	}
}
