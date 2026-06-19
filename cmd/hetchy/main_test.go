package main

import (
	"log/slog"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		raw  string
		want slog.Level
	}{
		{raw: "", want: slog.LevelInfo},
		{raw: " debug ", want: slog.LevelDebug},
		{raw: "WARN", want: slog.LevelWarn},
		{raw: "warning", want: slog.LevelWarn},
		{raw: "error", want: slog.LevelError},
		{raw: "trace", want: slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := parseLogLevel(tt.raw); got != tt.want {
				t.Fatalf("parseLogLevel(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestJobDispatchSchemaMatchesRequiresExactCleanVersion(t *testing.T) {
	tests := []struct {
		name     string
		current  uint
		expected uint
		want     bool
	}{
		{name: "matching clean version", current: 10, expected: 10, want: true},
		{name: "database behind binary", current: 9, expected: 10, want: false},
		{name: "database ahead of binary", current: 11, expected: 10, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jobDispatchSchemaMatches(tt.current, tt.expected); got != tt.want {
				t.Fatalf("jobDispatchSchemaMatches(%d, %d) = %t, want %t",
					tt.current, tt.expected, got, tt.want)
			}
		})
	}
}
