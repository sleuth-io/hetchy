package main

import "testing"

func TestJobDispatchSchemaMatchesRequiresExactCleanVersion(t *testing.T) {
	tests := []struct {
		name     string
		current  uint
		expected uint
		dirty    bool
		want     bool
	}{
		{name: "matching clean version", current: 10, expected: 10, want: true},
		{name: "database behind binary", current: 9, expected: 10, want: false},
		{name: "database ahead of binary", current: 11, expected: 10, want: false},
		{name: "dirty matching version", current: 10, expected: 10, dirty: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jobDispatchSchemaMatches(tt.current, tt.expected, tt.dirty); got != tt.want {
				t.Fatalf("jobDispatchSchemaMatches(%d, %d, %t) = %t, want %t",
					tt.current, tt.expected, tt.dirty, got, tt.want)
			}
		})
	}
}
