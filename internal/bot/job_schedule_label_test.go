package bot

import (
	"testing"
	"time"
)

func TestJobScheduleLabel(t *testing.T) {
	cases := []struct {
		name string
		cron string
		want string
	}{
		{name: "hourly", cron: "0 * * * *", want: "Every hour"},
		{name: "four hours", cron: "0 */4 * * *", want: "Every 4 hours"},
		{name: "six hours", cron: "0 */6 * * *", want: "Every 6 hours"},
		{name: "eight hours", cron: "0 */8 * * *", want: "Every 8 hours"},
		{name: "twelve hours", cron: "0 */12 * * *", want: "Every 12 hours"},
		{name: "daily", cron: "0 9 * * *", want: "Daily"},
		{name: "weekly", cron: "0 9 * * 1", want: "Weekly"},
		{name: "normalized spacing", cron: "  0   */4  *   * *  ", want: "Every 4 hours"},
		{name: "custom", cron: "15 7 * * 2", want: "15 7 * * 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jobScheduleLabel(tc.cron); got != tc.want {
				t.Fatalf("jobScheduleLabel(%q) = %q, want %q", tc.cron, got, tc.want)
			}
		})
	}
}

func TestJobTimezoneLabel(t *testing.T) {
	cases := []struct {
		timezone string
		want     string
	}{
		{timezone: "", want: "UTC"},
		{timezone: "UTC", want: "UTC"},
		{timezone: "America/Los_Angeles", want: "Los Angeles time"},
		{timezone: "Europe/London", want: "London time"},
		{timezone: "PST8PDT", want: "PST8PDT"},
	}
	for _, tc := range cases {
		if got := jobTimezoneLabel(tc.timezone); got != tc.want {
			t.Fatalf("jobTimezoneLabel(%q) = %q, want %q", tc.timezone, got, tc.want)
		}
	}
}

func TestJobDisplayTimeUsesJobTimezone(t *testing.T) {
	ts := time.Date(2026, 6, 4, 16, 0, 0, 0, time.UTC)
	if got := jobDisplayTime(ts, "America/Los_Angeles"); got != "Jun 4 at 9:00 AM" {
		t.Fatalf("jobDisplayTime LA = %q", got)
	}
	if got := jobDisplayTime(ts, "bad/timezone"); got != "Jun 4 at 4:00 PM" {
		t.Fatalf("jobDisplayTime fallback = %q", got)
	}
}
