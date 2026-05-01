// Package buildinfo carries build-time metadata stamped in via -ldflags.
package buildinfo

var (
	// Version is set via ldflags at build time.
	Version = "dev"
	// Commit is set via ldflags at build time.
	Commit = "none"
	// Date is set via ldflags at build time.
	Date = "unknown"
)
