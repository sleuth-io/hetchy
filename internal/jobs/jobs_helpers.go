package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func timeParam(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func stringPtrParam(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func interval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

func invalidInput(err error) error {
	if err == nil {
		return ErrInvalidInput
	}
	return fmt.Errorf("%w: %w", ErrInvalidInput, err)
}

func InvalidInputMessage(err error) string {
	return strings.TrimPrefix(err.Error(), ErrInvalidInput.Error()+": ")
}
