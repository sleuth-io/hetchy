package bot

import "testing"

func TestHumanList(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"setup.sh"}, "setup.sh"},
		{[]string{"setup.sh", "start.sh"}, "setup.sh and start.sh"},
		{[]string{"setup.sh", "start.sh", "health.sh"}, "setup.sh, start.sh, and health.sh"},
	}
	for _, tc := range cases {
		if got := humanList(tc.in); got != tc.want {
			t.Fatalf("humanList(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
