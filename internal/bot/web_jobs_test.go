package bot

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/jobs"
)

func TestWriteJobsStoreErrorHidesUnexpectedErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Bot{log: discardLogger()}).writeJobsStoreError(rec, errors.New("database column secret_value exploded"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "internal error") {
		t.Fatalf("body = %q, want generic internal error", body)
	}
	if strings.Contains(body, "secret_value") {
		t.Fatalf("body leaked raw error: %q", body)
	}
}

func TestWriteJobsStoreErrorKeepsInvalidInputUserFacing(t *testing.T) {
	rec := httptest.NewRecorder()
	err := fmt.Errorf("%w: job name is required", jobs.ErrInvalidInput)
	(&Bot{log: discardLogger()}).writeJobsStoreError(rec, err)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "job name is required") || strings.Contains(body, jobs.ErrInvalidInput.Error()) {
		t.Fatalf("body = %q, want user-facing validation message only", body)
	}
}
