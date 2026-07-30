package bootstrap

import (
	"context"
	"testing"
)

// DeleteSpec and DeleteSecret are the teardown half of the bootstrap store. They
// had no coverage, which matters because a delete that silently targets the
// wrong row is indistinguishable from one that worked — nothing reads back.
func TestStoreDeleteSpecTargetsTheExactTriple(t *testing.T) {
	fake := newBootstrapFakeDB()
	fake.exec["DeleteRepoSetupSpec"] = bootstrapExecResult{rows: 1}
	store := newBootstrapStoreForFake(t, fake)

	if err := store.DeleteSpec(context.Background(), 11, 22, "apps/web"); err != nil {
		t.Fatalf("DeleteSpec: %v", err)
	}

	call := fake.onlyExecCall(t, "DeleteRepoSetupSpec")
	assertBootstrapArg(t, call.args, 0, int64(11))
	assertBootstrapArg(t, call.args, 1, int64(22))
	assertBootstrapArg(t, call.args, 2, "apps/web")
}

// A repo with a single target uses the empty path; it must still scope the
// delete rather than falling back to something broader.
func TestStoreDeleteSpecUsesEmptyPathForSingleTargetRepos(t *testing.T) {
	fake := newBootstrapFakeDB()
	fake.exec["DeleteRepoSetupSpec"] = bootstrapExecResult{rows: 1}
	store := newBootstrapStoreForFake(t, fake)

	if err := store.DeleteSpec(context.Background(), 1, 2, ""); err != nil {
		t.Fatalf("DeleteSpec: %v", err)
	}
	call := fake.onlyExecCall(t, "DeleteRepoSetupSpec")
	assertBootstrapArg(t, call.args, 2, "")
}

func TestStoreDeleteSecretTargetsTheNamedSecret(t *testing.T) {
	fake := newBootstrapFakeDB()
	fake.exec["DeleteRepoSecretValue"] = bootstrapExecResult{rows: 1}
	store := newBootstrapStoreForFake(t, fake)

	if err := store.DeleteSecret(context.Background(), 11, 22, "apps/web", "STRIPE_KEY"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}

	call := fake.onlyExecCall(t, "DeleteRepoSecretValue")
	assertBootstrapArg(t, call.args, 0, int64(11))
	assertBootstrapArg(t, call.args, 1, int64(22))
	assertBootstrapArg(t, call.args, 2, "apps/web")
	assertBootstrapArg(t, call.args, 3, "STRIPE_KEY")
}
