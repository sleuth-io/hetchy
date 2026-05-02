package bot

import (
	"testing"
	"time"
)

// stopAfter returns a channel that fires after a short timeout so tests
// don't hang forever if a goroutine doesn't return.
func stopAfter(t *testing.T) <-chan time.Time {
	t.Helper()
	return time.After(3 * time.Second)
}
