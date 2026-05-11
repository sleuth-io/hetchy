package bot

import (
	"testing"
	"time"
)

func TestHeartbeatElapsedString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "1m"},
		{5 * time.Minute, "5m"},
		{time.Hour, "1h"},
		{time.Hour + 2*time.Minute, "1h2m"},
	}

	for _, tc := range cases {
		if got := heartbeatElapsedString(tc.in); got != tc.want {
			t.Fatalf("heartbeatElapsedString(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
