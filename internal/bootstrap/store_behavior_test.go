package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/secrets"
)

func TestStoreSaveSpecPersistsValidatedSpecContract(t *testing.T) {
	fake := newBootstrapFakeDB()
	fake.queryRow["UpsertRepoSetupSpec"] = bootstrapSetupSpecRow(testRepoSetupSpec(StatusValidated))
	store := newBootstrapStoreForFake(t, fake)

	spec := testBootstrapSpec(StatusValidated)
	spec.Services = nil
	spec.RequiredSecrets = nil
	spec.DeferredCapabilities = nil
	spec.SuggestedRepoChanges = nil
	spec.ValidationCapability = ValidationCapability{
		CanRunUI:     true,
		TestCommands: []string{"go test ./..."},
	}
	if err := store.SaveSpec(context.Background(), spec); err != nil {
		t.Fatalf("SaveSpec returned error: %v", err)
	}

	call := fake.onlyQueryRowCall(t, "UpsertRepoSetupSpec")
	assertBootstrapArg(t, call.args, 0, int64(11))
	assertBootstrapArg(t, call.args, 1, int64(22))
	assertBootstrapArg(t, call.args, 2, "apps/web")
	assertBootstrapArg(t, call.args, 3, int32(3))
	assertBootstrapArg(t, call.args, 4, CurrentBootstrapGeneration)
	assertBootstrapArg(t, call.args, 5, "node")
	assertBootstrapArg(t, call.args, 9, stringPtr("npm run stop"))
	assertBootstrapJSON(t, call.args, 11, []Service{})
	assertBootstrapJSON(t, call.args, 12, []Secret{})
	assertBootstrapJSON(t, call.args, 13, []string{})
	assertBootstrapJSON(t, call.args, 14, []string{})
	assertBootstrapJSON(t, call.args, 15, spec.ValidationCapability)
	assertBootstrapArg(t, call.args, 17, string(StatusValidated))
	lastValidated, ok := call.args[18].(pgtype.Timestamptz)
	if !ok || !lastValidated.Valid {
		t.Fatalf("last_validated_at = %#v, want valid timestamp", call.args[18])
	}
	assertBootstrapArg(t, call.args, 19, int32(2))
	assertBootstrapArg(t, call.args, 20, int32(1))
	assertBootstrapArg(t, call.args, 21, stringPtr("boot ok"))
}

func TestStoreSaveSpecDoesNotStampUnvalidatedStatuses(t *testing.T) {
	for _, status := range []ValidationStatus{StatusFailing, StatusStale} {
		t.Run(string(status), func(t *testing.T) {
			fake := newBootstrapFakeDB()
			fake.queryRow["UpsertRepoSetupSpec"] = bootstrapSetupSpecRow(testRepoSetupSpec(status))
			store := newBootstrapStoreForFake(t, fake)

			spec := testBootstrapSpec(status)
			if err := store.SaveSpec(context.Background(), spec); err != nil {
				t.Fatalf("SaveSpec returned error: %v", err)
			}
			call := fake.onlyQueryRowCall(t, "UpsertRepoSetupSpec")
			lastValidated, ok := call.args[18].(pgtype.Timestamptz)
			if !ok || lastValidated.Valid {
				t.Fatalf("last_validated_at = %#v, want NULL for %s", call.args[18], status)
			}
		})
	}
}

func TestStoreSaveFailingSpecRequiresFailingStatusAndUsesFailingUpsert(t *testing.T) {
	fake := newBootstrapFakeDB()
	store := newBootstrapStoreForFake(t, fake)
	err := store.SaveFailingSpec(context.Background(), testBootstrapSpec(StatusValidated))
	if err == nil || !strings.Contains(err.Error(), "SaveFailingSpec called with status=validated") {
		t.Fatalf("SaveFailingSpec(validated) error = %v, want status guard", err)
	}
	if len(fake.queryRowCalls) != 0 {
		t.Fatalf("non-failing SaveFailingSpec touched DB: %+v", fake.queryRowCalls)
	}

	fake.queryRow["UpsertFailingRepoSetupSpec"] = bootstrapSetupSpecRow(testRepoSetupSpec(StatusFailing))
	if err := store.SaveFailingSpec(context.Background(), testBootstrapSpec(StatusFailing)); err != nil {
		t.Fatalf("SaveFailingSpec(failing) returned error: %v", err)
	}
	call := fake.onlyQueryRowCall(t, "UpsertFailingRepoSetupSpec")
	assertBootstrapArg(t, call.args, 0, int64(11))
	assertBootstrapArg(t, call.args, 17, string(StatusFailing))
	assertBootstrapArg(t, call.args, 18, stringPtr("boot ok"))
	if fake.queryRowCallCount("UpsertRepoSetupSpec") != 0 {
		t.Fatal("SaveFailingSpec should use the failing upsert, not the success-path upsert")
	}
}

func TestStoreGetSpecMapsMissingRowsToErrNotFound(t *testing.T) {
	fake := newBootstrapFakeDB()
	fake.queryRow["GetRepoSetupSpec"] = bootstrapErrRow{err: pgx.ErrNoRows}
	store := newBootstrapStoreForFake(t, fake)

	_, err := store.GetSpec(context.Background(), 11, 22, "apps/web")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSpec error = %v, want ErrNotFound", err)
	}
}

func TestRowToSpecDecodesOptionalFieldsAndValidationCapability(t *testing.T) {
	stop := "npm run stop"
	log := "bootstrap log"
	row := testRepoSetupSpec(StatusPartial)
	row.StopScript = &stop
	row.BootstrapLog = &log
	row.ValidationCapability = mustBootstrapJSON(t, ValidationCapability{
		CanRunUI:         true,
		DefaultURL:       "http://localhost:3000",
		EvidenceRequired: []string{"screenshot"},
	})

	got, err := rowToSpec(row)
	if err != nil {
		t.Fatalf("rowToSpec returned error: %v", err)
	}
	if got.StopScript != stop || got.BootstrapLog != log {
		t.Fatalf("optional text fields = stop %q log %q", got.StopScript, got.BootstrapLog)
	}
	if got.ValidationCapability.DefaultURL != "http://localhost:3000" ||
		len(got.ValidationCapability.EvidenceRequired) != 1 ||
		got.ValidationCapability.EvidenceRequired[0] != "screenshot" {
		t.Fatalf("ValidationCapability = %+v", got.ValidationCapability)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "web" {
		t.Fatalf("Services = %+v", got.Services)
	}
	if len(got.RequiredSecrets) != 1 || got.RequiredSecrets[0].Name != "API_KEY" {
		t.Fatalf("RequiredSecrets = %+v", got.RequiredSecrets)
	}
}

func TestStoreSecretsEncryptValuesAndSkipPlaceholders(t *testing.T) {
	fake := newBootstrapFakeDB()
	fake.exec["UpsertRepoSecretValue"] = bootstrapExecResult{rows: 1}
	store := newBootstrapStoreForFake(t, fake)

	if err := store.SetSecret(context.Background(), 11, 22, "apps/web", "API_KEY", "sk_test"); err != nil {
		t.Fatalf("SetSecret returned error: %v", err)
	}
	setCall := fake.onlyExecCall(t, "UpsertRepoSecretValue")
	assertBootstrapArg(t, setCall.args, 0, int64(11))
	assertBootstrapArg(t, setCall.args, 1, int64(22))
	assertBootstrapArg(t, setCall.args, 2, "apps/web")
	assertBootstrapArg(t, setCall.args, 3, "API_KEY")
	encrypted, ok := setCall.args[4].([]byte)
	if !ok || len(encrypted) == 0 {
		t.Fatalf("encrypted secret arg = %#v, want ciphertext", setCall.args[4])
	}
	plain, err := store.cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt captured secret: %v", err)
	}
	if plain != "sk_test" {
		t.Fatalf("decrypted secret = %q, want sk_test", plain)
	}

	fake.query["ListRepoSecretValues"] = &bootstrapRows{rows: []bootstrapScanRow{
		bootstrapSecretValueRow(testRepoSecretValue("API_KEY", encrypted)),
		bootstrapSecretValueRow(testRepoSecretValue("EMPTY_PLACEHOLDER", nil)),
	}}
	values, err := store.GetSecrets(context.Background(), 11, 22, "apps/web")
	if err != nil {
		t.Fatalf("GetSecrets returned error: %v", err)
	}
	if len(values) != 1 || values["API_KEY"] != "sk_test" {
		t.Fatalf("GetSecrets = %+v, want only decrypted filled secret", values)
	}

	fake.query["ListRepoSecretValues"] = &bootstrapRows{rows: []bootstrapScanRow{
		bootstrapSecretValueRow(testRepoSecretValue("API_KEY", encrypted)),
		bootstrapSecretValueRow(testRepoSecretValue("EMPTY_PLACEHOLDER", nil)),
	}}
	summaries, err := store.ListSecrets(context.Background(), 11, 22, "apps/web")
	if err != nil {
		t.Fatalf("ListSecrets returned error: %v", err)
	}
	if len(summaries) != 2 || !summaries[0].Filled || summaries[1].Filled {
		t.Fatalf("ListSecrets = %+v, want filled flag without decrypting", summaries)
	}
}

func TestStoreDeclareRequiredSecretPreservesExistingValues(t *testing.T) {
	fake := newBootstrapFakeDB()
	fake.exec["InsertRepoSecretValueIfAbsent"] = bootstrapExecResult{rows: 0}
	store := newBootstrapStoreForFake(t, fake)

	if err := store.DeclareRequiredSecret(context.Background(), 11, 22, "apps/web", "API_KEY"); err != nil {
		t.Fatalf("DeclareRequiredSecret returned error: %v", err)
	}
	call := fake.onlyExecCall(t, "InsertRepoSecretValueIfAbsent")
	assertBootstrapArg(t, call.args, 0, int64(11))
	assertBootstrapArg(t, call.args, 1, int64(22))
	assertBootstrapArg(t, call.args, 2, "apps/web")
	assertBootstrapArg(t, call.args, 3, "API_KEY")
	if fake.execCallCount("UpsertRepoSecretValue") != 0 {
		t.Fatal("declaring a required secret should not upsert over an existing value")
	}
}

func newBootstrapStoreForFake(t *testing.T, fake *bootstrapFakeDB) *Store {
	t.Helper()
	cipher, err := secrets.New("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	return New(&db.Store{Queries: sqlc.New(fake)}, cipher)
}

type bootstrapFakeDB struct {
	queryRow map[string]pgx.Row
	query    map[string]pgx.Rows
	exec     map[string]bootstrapExecResult

	queryRowCalls []bootstrapDBCall
	queryCalls    []bootstrapDBCall
	execCalls     []bootstrapDBCall
}

type bootstrapDBCall struct {
	sql  string
	args []any
}

type bootstrapExecResult struct {
	rows int64
	err  error
}

func newBootstrapFakeDB() *bootstrapFakeDB {
	return &bootstrapFakeDB{
		queryRow: map[string]pgx.Row{},
		query:    map[string]pgx.Rows{},
		exec:     map[string]bootstrapExecResult{},
	}
}

func (f *bootstrapFakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls = append(f.execCalls, bootstrapDBCall{sql: sql, args: args})
	for fragment, result := range f.exec {
		if strings.Contains(sql, fragment) {
			if result.err != nil {
				return pgconn.CommandTag{}, result.err
			}
			return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", result.rows)), nil
		}
	}
	return pgconn.CommandTag{}, fmt.Errorf("unexpected exec: %s", sql)
}

func (f *bootstrapFakeDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	f.queryCalls = append(f.queryCalls, bootstrapDBCall{sql: sql, args: args})
	for fragment, rows := range f.query {
		if strings.Contains(sql, fragment) {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("unexpected query: %s", sql)
}

func (f *bootstrapFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.queryRowCalls = append(f.queryRowCalls, bootstrapDBCall{sql: sql, args: args})
	for fragment, row := range f.queryRow {
		if strings.Contains(sql, fragment) {
			return row
		}
	}
	return bootstrapErrRow{err: fmt.Errorf("unexpected query row: %s", sql)}
}

func (f *bootstrapFakeDB) queryRowCallCount(fragment string) int {
	count := 0
	for _, call := range f.queryRowCalls {
		if strings.Contains(call.sql, fragment) {
			count++
		}
	}
	return count
}

func (f *bootstrapFakeDB) execCallCount(fragment string) int {
	count := 0
	for _, call := range f.execCalls {
		if strings.Contains(call.sql, fragment) {
			count++
		}
	}
	return count
}

func (f *bootstrapFakeDB) onlyQueryRowCall(t *testing.T, fragment string) bootstrapDBCall {
	t.Helper()
	var matches []bootstrapDBCall
	for _, call := range f.queryRowCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("query row calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

func (f *bootstrapFakeDB) onlyExecCall(t *testing.T, fragment string) bootstrapDBCall {
	t.Helper()
	var matches []bootstrapDBCall
	for _, call := range f.execCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("exec calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

type bootstrapErrRow struct {
	err error
}

func (r bootstrapErrRow) Scan(...any) error {
	return r.err
}

type bootstrapScanRow struct {
	values []any
}

func (r bootstrapScanRow) Scan(dest ...any) error {
	return bootstrapAssignScan(dest, r.values)
}

type bootstrapRows struct {
	rows   []bootstrapScanRow
	idx    int
	closed bool
}

func (r *bootstrapRows) Close()                                       { r.closed = true }
func (r *bootstrapRows) Err() error                                   { return nil }
func (r *bootstrapRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *bootstrapRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *bootstrapRows) Values() ([]any, error)                       { return nil, nil }
func (r *bootstrapRows) RawValues() [][]byte                          { return nil }
func (r *bootstrapRows) Conn() *pgx.Conn                              { return nil }

func (r *bootstrapRows) Next() bool {
	if r.idx >= len(r.rows) {
		r.Close()
		return false
	}
	r.idx++
	return true
}

func (r *bootstrapRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.rows) {
		return errors.New("scan called before Next or after EOF")
	}
	return r.rows[r.idx-1].Scan(dest...)
}

func bootstrapAssignScan(dest, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(values))
	}
	for i := range dest {
		if err := bootstrapAssignScanValue(dest[i], values[i]); err != nil {
			return fmt.Errorf("scan dest %d: %w", i, err)
		}
	}
	return nil
}

func bootstrapAssignScanValue(dest, value any) error {
	switch d := dest.(type) {
	case *pgtype.UUID:
		v, ok := value.(pgtype.UUID)
		if !ok {
			return fmt.Errorf("got %T, want pgtype.UUID", value)
		}
		*d = v
	case *int64:
		v, ok := value.(int64)
		if !ok {
			return fmt.Errorf("got %T, want int64", value)
		}
		*d = v
	case *int32:
		v, ok := value.(int32)
		if !ok {
			return fmt.Errorf("got %T, want int32", value)
		}
		*d = v
	case *string:
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("got %T, want string", value)
		}
		*d = v
	case **string:
		v, ok := value.(*string)
		if !ok && value != nil {
			return fmt.Errorf("got %T, want *string", value)
		}
		*d = v
	case *[]byte:
		v, ok := value.([]byte)
		if !ok && value != nil {
			return fmt.Errorf("got %T, want []byte", value)
		}
		*d = v
	case *pgtype.Timestamptz:
		v, ok := value.(pgtype.Timestamptz)
		if !ok {
			return fmt.Errorf("got %T, want pgtype.Timestamptz", value)
		}
		*d = v
	default:
		return fmt.Errorf("unsupported scan destination %T", dest)
	}
	return nil
}

func testBootstrapSpec(status ValidationStatus) *Spec {
	return &Spec{
		InstallationID:       11,
		RepoID:               22,
		Path:                 "apps/web",
		SpecVersion:          3,
		BootstrapGeneration:  CurrentBootstrapGeneration,
		Kind:                 "node",
		SetupScript:          "npm install",
		StartScript:          "npm start",
		HealthCheck:          "curl http://localhost:3000",
		StopScript:           "npm run stop",
		LessonsMD:            "lessons",
		Services:             []Service{{Name: "web", Port: 3000, URL: "http://localhost:3000", Kind: "ui"}},
		RequiredSecrets:      []Secret{{Name: "API_KEY", UserSupplied: true, Hint: "from vendor"}},
		DeferredCapabilities: []string{"oauth"},
		SuggestedRepoChanges: []string{"add smoke test"},
		ValidationCapability: ValidationCapability{DefaultURL: "http://localhost:3000"},
		SourceFingerprint:    "fingerprint",
		ValidationStatus:     status,
		SuccessCount:         2,
		FailureCount:         1,
		BootstrapLog:         "boot ok",
	}
}

func testRepoSetupSpec(status ValidationStatus) sqlc.RepoSetupSpec {
	now := pgtype.Timestamptz{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Valid: true}
	stop := "npm run stop"
	log := "boot ok"
	spec := testBootstrapSpec(status)
	return sqlc.RepoSetupSpec{
		InstallationID:       spec.InstallationID,
		RepoID:               spec.RepoID,
		Path:                 spec.Path,
		SpecVersion:          spec.SpecVersion,
		BootstrapGeneration:  spec.BootstrapGeneration,
		Kind:                 spec.Kind,
		SetupScript:          spec.SetupScript,
		StartScript:          spec.StartScript,
		HealthCheck:          spec.HealthCheck,
		StopScript:           &stop,
		Services:             mustBootstrapJSON(nil, spec.Services),
		RequiredSecrets:      mustBootstrapJSON(nil, spec.RequiredSecrets),
		DeferredCapabilities: mustBootstrapJSON(nil, spec.DeferredCapabilities),
		SuggestedRepoChanges: mustBootstrapJSON(nil, spec.SuggestedRepoChanges),
		SourceFingerprint:    spec.SourceFingerprint,
		ValidationStatus:     string(status),
		LastValidatedAt:      now,
		SuccessCount:         spec.SuccessCount,
		FailureCount:         spec.FailureCount,
		BootstrapLog:         &log,
		CreatedAt:            now,
		UpdatedAt:            now,
		LessonsMd:            spec.LessonsMD,
		ValidationCapability: mustBootstrapJSON(nil, spec.ValidationCapability),
	}
}

func bootstrapSetupSpecRow(row sqlc.RepoSetupSpec) bootstrapScanRow {
	return bootstrapScanRow{values: []any{
		row.ID,
		row.InstallationID,
		row.RepoID,
		row.Path,
		row.SpecVersion,
		row.Kind,
		row.SetupScript,
		row.StartScript,
		row.HealthCheck,
		row.StopScript,
		row.Services,
		row.RequiredSecrets,
		row.DeferredCapabilities,
		row.SuggestedRepoChanges,
		row.SourceFingerprint,
		row.ValidationStatus,
		row.LastValidatedAt,
		row.SuccessCount,
		row.FailureCount,
		row.BootstrapLog,
		row.CreatedAt,
		row.UpdatedAt,
		row.LessonsMd,
		row.ValidationCapability,
		row.BootstrapGeneration,
	}}
}

func testRepoSecretValue(name string, value []byte) sqlc.RepoSecretValue {
	now := pgtype.Timestamptz{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Valid: true}
	return sqlc.RepoSecretValue{
		InstallationID: 11,
		RepoID:         22,
		Path:           "apps/web",
		Name:           name,
		ValueEncrypted: value,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func bootstrapSecretValueRow(row sqlc.RepoSecretValue) bootstrapScanRow {
	return bootstrapScanRow{values: []any{
		row.ID,
		row.InstallationID,
		row.RepoID,
		row.Path,
		row.Name,
		row.ValueEncrypted,
		row.CreatedAt,
		row.UpdatedAt,
	}}
}

func assertBootstrapArg(t *testing.T, args []any, idx int, want any) {
	t.Helper()
	if len(args) <= idx {
		t.Fatalf("arg[%d] missing from %v", idx, args)
	}
	if got := args[idx]; !bootstrapArgEqual(got, want) {
		t.Fatalf("arg[%d] = %#v (%T), want %#v (%T)", idx, got, got, want, want)
	}
}

func bootstrapArgEqual(got, want any) bool {
	gotString, gotOK := got.(*string)
	wantString, wantOK := want.(*string)
	if gotOK || wantOK {
		if gotString == nil || wantString == nil {
			return gotString == wantString
		}
		return *gotString == *wantString
	}
	return got == want
}

func assertBootstrapJSON(t *testing.T, args []any, idx int, want any) {
	t.Helper()
	if len(args) <= idx {
		t.Fatalf("arg[%d] missing from %v", idx, args)
	}
	got, ok := args[idx].([]byte)
	if !ok {
		t.Fatalf("arg[%d] = %#v (%T), want JSON bytes", idx, args[idx], args[idx])
	}
	wantBytes := mustBootstrapJSON(t, want)
	if string(got) != string(wantBytes) {
		t.Fatalf("arg[%d] JSON = %s, want %s", idx, got, wantBytes)
	}
}

func mustBootstrapJSON(t *testing.T, v any) []byte {
	if t != nil {
		t.Helper()
	}
	data, err := json.Marshal(v)
	if err != nil {
		if t == nil {
			panic(err)
		}
		t.Fatalf("json.Marshal: %v", err)
	}
	return data
}

func stringPtr(s string) *string {
	return &s
}
