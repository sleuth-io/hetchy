package bot

import "testing"

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
