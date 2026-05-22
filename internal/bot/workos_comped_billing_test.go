package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	workos "github.com/workos/workos-go/v7"

	"github.com/hetchyhq/hetchy/internal/billing"
	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

func TestWorkOSWebhookHandlerMissingSecret(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want int
	}{
		{name: "dev noops", env: "dev", want: http.StatusOK},
		{name: "prod rejects", env: "prod", want: http.StatusServiceUnavailable},
		{name: "default rejects", env: "", want: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bot{cfg: Config{Env: tc.env}, log: discardLogger()}
			req := httptest.NewRequest(http.MethodPost, "/workos/webhook", strings.NewReader(`{}`))
			rr := httptest.NewRecorder()

			b.workOSWebhookHandler(rr, req)

			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%q", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestWorkOSFeatureFlagEventOrgIDsUsesCurrentAndPreviousTargets(t *testing.T) {
	event := &workos.EventSchema{
		Context: map[string]any{
			"configured_targets": map[string]any{
				"organizations": []any{
					map[string]any{"id": "org_b"},
					map[string]any{"id": "org_a"},
				},
			},
			"previous_attributes": map[string]any{
				"context": map[string]any{
					"configured_targets": map[string]any{
						"organizations": []any{
							map[string]any{"id": "org_c"},
							map[string]any{"id": "org_a"},
						},
					},
				},
			},
		},
	}

	got := workOSFeatureFlagEventOrgIDs(event)
	want := []string{"org_a", "org_b", "org_c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workOSFeatureFlagEventOrgIDs = %#v, want %#v", got, want)
	}
}

func TestWorkOSFeatureFlagEventOrgIDsIgnoresMalformedTargets(t *testing.T) {
	event := &workos.EventSchema{
		Context: map[string]any{
			"configured_targets": map[string]any{
				"organizations": []any{
					map[string]any{"id": ""},
					map[string]any{"name": "Missing ID"},
					"bad",
				},
			},
		},
	}

	if got := workOSFeatureFlagEventOrgIDs(event); len(got) != 0 {
		t.Fatalf("workOSFeatureFlagEventOrgIDs = %#v, want empty", got)
	}
}

func TestSyncWorkOSCompedBillingForOrgCachedSkipsRecentSuccess(t *testing.T) {
	billingDB := &workOSBillingDB{}
	b := newWorkOSBillingTestBot(billingDB)
	featureCalls := 0
	b.workOSOrgHasFeatureFlagFn = func(_ context.Context, orgID, slug string) (bool, error) {
		featureCalls++
		if orgID != "org_1" || slug != workOSCompedBillingFlagSlug {
			t.Fatalf("feature flag lookup = (%q, %q), want org_1/%s", orgID, slug, workOSCompedBillingFlagSlug)
		}
		return true, nil
	}

	if err := b.syncWorkOSCompedBillingForOrgCached(t.Context(), "org_1"); err != nil {
		t.Fatalf("first cached sync: %v", err)
	}
	if err := b.syncWorkOSCompedBillingForOrgCached(t.Context(), "org_1"); err != nil {
		t.Fatalf("second cached sync: %v", err)
	}
	if featureCalls != 1 || len(billingDB.setExemptCalls) != 1 {
		t.Fatalf("recent cached sync calls = feature:%d set:%d, want 1/1", featureCalls, len(billingDB.setExemptCalls))
	}

	b.workOSCompedBillingSyncs.Store("org_1", time.Now().Add(-workOSCompedBillingSyncTTL-time.Second))
	if err := b.syncWorkOSCompedBillingForOrgCached(t.Context(), "org_1"); err != nil {
		t.Fatalf("expired cached sync: %v", err)
	}
	if featureCalls != 2 || len(billingDB.setExemptCalls) != 2 {
		t.Fatalf("expired cached sync calls = feature:%d set:%d, want 2/2", featureCalls, len(billingDB.setExemptCalls))
	}
}

func TestHandleWorkOSEventSyncsFlagCreatedTargets(t *testing.T) {
	billingDB := &workOSBillingDB{}
	b := newWorkOSBillingTestBot(billingDB)
	b.workOSOrgHasFeatureFlagFn = func(_ context.Context, orgID, slug string) (bool, error) {
		if orgID != "org_created" || slug != workOSCompedBillingFlagSlug {
			t.Fatalf("feature flag lookup = (%q, %q), want org_created/%s", orgID, slug, workOSCompedBillingFlagSlug)
		}
		return true, nil
	}

	err := b.handleWorkOSEvent(t.Context(), &workos.EventSchema{
		Event: "flag.created",
		Data:  map[string]any{"slug": workOSCompedBillingFlagSlug},
		Context: map[string]any{
			"configured_targets": map[string]any{
				"organizations": []any{map[string]any{"id": "org_created"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("handle flag.created: %v", err)
	}
	want := []workOSSetExemptCall{{orgID: "org_created", exempt: true}}
	if !reflect.DeepEqual(billingDB.setExemptCalls, want) {
		t.Fatalf("set exempt calls = %#v, want %#v", billingDB.setExemptCalls, want)
	}
}

func TestHandleWorkOSEventSkipsFlagUpdatedFallbackWithoutTargets(t *testing.T) {
	billingDB := &workOSBillingDB{accountOrgIDs: []string{"org_1"}}
	b := newWorkOSBillingTestBot(billingDB)
	b.workOSOrgHasFeatureFlagFn = func(context.Context, string, string) (bool, error) {
		t.Fatal("feature flag lookup should not run for flag.updated without target context")
		return false, nil
	}

	err := b.handleWorkOSEvent(t.Context(), &workos.EventSchema{
		Event: "flag.updated",
		Data:  map[string]any{"slug": workOSCompedBillingFlagSlug},
	})
	if err != nil {
		t.Fatalf("handle flag.updated: %v", err)
	}
	if billingDB.listAccountCalls != 0 || len(billingDB.setExemptCalls) != 0 {
		t.Fatalf("fallback calls = list:%d set:%d, want 0/0", billingDB.listAccountCalls, len(billingDB.setExemptCalls))
	}
}

func TestHandleWorkOSEventFallbacksForRuleUpdateAndDelete(t *testing.T) {
	t.Run("rule update syncs all billing accounts", func(t *testing.T) {
		billingDB := &workOSBillingDB{accountOrgIDs: []string{"org_a", "org_b"}}
		b := newWorkOSBillingTestBot(billingDB)
		b.workOSOrgHasFeatureFlagFn = func(_ context.Context, orgID, slug string) (bool, error) {
			if slug != workOSCompedBillingFlagSlug {
				t.Fatalf("slug = %q, want %q", slug, workOSCompedBillingFlagSlug)
			}
			return orgID == "org_a", nil
		}

		err := b.handleWorkOSEvent(t.Context(), &workos.EventSchema{
			Event: "flag.rule_updated",
			Data:  map[string]any{"slug": workOSCompedBillingFlagSlug},
		})
		if err != nil {
			t.Fatalf("handle flag.rule_updated: %v", err)
		}
		want := []workOSSetExemptCall{
			{orgID: "org_a", exempt: true},
			{orgID: "org_b", exempt: false},
		}
		if billingDB.listAccountCalls != 1 || !reflect.DeepEqual(billingDB.setExemptCalls, want) {
			t.Fatalf("rule fallback = list:%d set:%#v, want 1/%#v", billingDB.listAccountCalls, billingDB.setExemptCalls, want)
		}
	})

	t.Run("delete clears currently exempt accounts", func(t *testing.T) {
		billingDB := &workOSBillingDB{exemptOrgIDs: []string{"org_old"}}
		b := newWorkOSBillingTestBot(billingDB)
		b.workOSOrgHasFeatureFlagFn = func(context.Context, string, string) (bool, error) {
			t.Fatal("feature flag lookup should not run for flag.deleted")
			return false, nil
		}

		err := b.handleWorkOSEvent(t.Context(), &workos.EventSchema{
			Event: "flag.deleted",
			Data:  map[string]any{"slug": workOSCompedBillingFlagSlug},
		})
		if err != nil {
			t.Fatalf("handle flag.deleted: %v", err)
		}
		want := []workOSSetExemptCall{{orgID: "org_old", exempt: false}}
		if billingDB.listExemptCalls != 1 || !reflect.DeepEqual(billingDB.setExemptCalls, want) {
			t.Fatalf("delete fallback = list:%d set:%#v, want 1/%#v", billingDB.listExemptCalls, billingDB.setExemptCalls, want)
		}
	})
}

type workOSSetExemptCall struct {
	orgID  string
	exempt bool
}

type workOSBillingDB struct {
	accountOrgIDs    []string
	exemptOrgIDs     []string
	setExemptCalls   []workOSSetExemptCall
	listAccountCalls int
	listExemptCalls  int
}

func newWorkOSBillingTestBot(billingDB *workOSBillingDB) *Bot {
	return &Bot{
		log: discardLogger(),
		billing: billing.NewService(
			billing.NewStore(&db.Store{Queries: sqlc.New(billingDB)}),
			nil,
		),
	}
}

func (f *workOSBillingDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *workOSBillingDB) Query(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
	switch {
	case strings.Contains(query, "ListBillingAccountOrgIDs"):
		f.listAccountCalls++
		return &workOSStringRows{values: append([]string(nil), f.accountOrgIDs...)}, nil
	case strings.Contains(query, "ListBillingExemptOrgIDs"):
		f.listExemptCalls++
		return &workOSStringRows{values: append([]string(nil), f.exemptOrgIDs...)}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

func (f *workOSBillingDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	if !strings.Contains(query, "SetBillingExempt") {
		return workOSErrRow{err: fmt.Errorf("unexpected query row: %s", query)}
	}
	orgID, _ := args[0].(string)
	exempt, _ := args[1].(bool)
	f.setExemptCalls = append(f.setExemptCalls, workOSSetExemptCall{orgID: orgID, exempt: exempt})
	return workOSBillingAccountRow{orgID: orgID, exempt: exempt}
}

type workOSBillingAccountRow struct {
	orgID  string
	exempt bool
}

func (r workOSBillingAccountRow) Scan(dest ...any) error {
	values := []any{
		r.orgID, "", "", billing.PlanFree, billing.PlanFree,
		pgtype.Timestamptz{}, pgtype.Timestamptz{},
		int32(0), int32(0), int32(0),
		billing.FlavorStandard, int32(4), r.exempt, "",
		pgtype.Timestamptz{}, pgtype.Timestamptz{}, "", pgtype.Timestamptz{},
	}
	if len(dest) != len(values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(values))
	}
	for i := range dest {
		if err := assignWorkOSScanValue(dest[i], values[i]); err != nil {
			return err
		}
	}
	return nil
}

type workOSErrRow struct {
	err error
}

func (r workOSErrRow) Scan(...any) error {
	return r.err
}

type workOSStringRows struct {
	values []string
	idx    int
	closed bool
}

func (r *workOSStringRows) Close() {
	r.closed = true
}

func (r *workOSStringRows) Err() error {
	return nil
}

func (r *workOSStringRows) CommandTag() pgconn.CommandTag {
	return pgconn.CommandTag{}
}

func (r *workOSStringRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}

func (r *workOSStringRows) Next() bool {
	if r.idx >= len(r.values) {
		r.Close()
		return false
	}
	r.idx++
	return true
}

func (r *workOSStringRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.values) {
		return errors.New("scan called without current row")
	}
	if len(dest) != 1 {
		return fmt.Errorf("scan destination count = %d, want 1", len(dest))
	}
	return assignWorkOSScanValue(dest[0], r.values[r.idx-1])
}

func (r *workOSStringRows) Values() ([]any, error) {
	if r.idx == 0 || r.idx > len(r.values) {
		return nil, errors.New("values called without current row")
	}
	return []any{r.values[r.idx-1]}, nil
}

func (r *workOSStringRows) RawValues() [][]byte {
	if r.idx == 0 || r.idx > len(r.values) {
		return nil
	}
	return [][]byte{[]byte(r.values[r.idx-1])}
}

func (r *workOSStringRows) Conn() *pgx.Conn {
	return nil
}

func assignWorkOSScanValue(dest, value any) error {
	switch d := dest.(type) {
	case *string:
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("cannot scan %T into *string", value)
		}
		*d = v
	case *int32:
		v, ok := value.(int32)
		if !ok {
			return fmt.Errorf("cannot scan %T into *int32", value)
		}
		*d = v
	case *bool:
		v, ok := value.(bool)
		if !ok {
			return fmt.Errorf("cannot scan %T into *bool", value)
		}
		*d = v
	case *pgtype.Timestamptz:
		v, ok := value.(pgtype.Timestamptz)
		if !ok {
			return fmt.Errorf("cannot scan %T into *pgtype.Timestamptz", value)
		}
		*d = v
	default:
		return fmt.Errorf("unsupported scan destination %T", dest)
	}
	return nil
}
