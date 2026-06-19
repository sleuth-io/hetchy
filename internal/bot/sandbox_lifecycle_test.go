package bot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

func TestFreshRunCreatesSandboxWithAutoArchiveInterval(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.cfg = Config{Snapshot: "snap"}
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "sleuth-io/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	var gotAutoArchive int
	b.createFn = func(_ context.Context, params any) (*daytona.Sandbox, error) {
		p, ok := params.(types.SnapshotParams)
		if !ok {
			t.Fatalf("create params type = %T, want types.SnapshotParams", params)
		}
		if p.AutoArchiveInterval == nil {
			t.Fatal("AutoArchiveInterval is nil")
		}
		gotAutoArchive = *p.AutoArchiveInterval
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(context.Context, *daytona.Sandbox, repoCtx, orgcfg.Config, agents.Profile, string, string, string, chatTaskOptions, ClaudeModel, blocks.Emitter) (string, error) {
		return "https://github.com/sleuth-io/hetchy/pull/2", nil
	}
	b.deleteSandboxSessionFn = func(*daytona.Sandbox, string) {}
	b.stopAndArchiveFn = func(context.Context, *daytona.Sandbox) {}

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "sleuth-io", DefaultGitHubRepo: "hetchy"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, newCaptureEmitter())

	if gotAutoArchive != 60 {
		t.Fatalf("AutoArchiveInterval = %d, want 60", gotAutoArchive)
	}
}

func TestSuccessfulCleanupStopsWithAutoArchive(t *testing.T) {
	b := &Bot{log: discardLogger(), cfg: Config{DaytonaAutoArchiveMinutes: 60}}
	var calls []string
	b.setAutoArchiveIntervalFn = func(_ context.Context, _ *daytona.Sandbox, minutes *int) error {
		calls = append(calls, "set")
		if minutes == nil || *minutes != 60 {
			t.Fatalf("auto archive minutes = %v, want 60", minutes)
		}
		return nil
	}
	b.stopSandboxFn = func(_ context.Context, _ *daytona.Sandbox) error {
		calls = append(calls, "stop")
		return nil
	}
	b.archiveSandboxFn = func(context.Context, *daytona.Sandbox) error {
		calls = append(calls, "archive")
		return nil
	}

	b.stopAndArchiveSandbox(context.Background(), &daytona.Sandbox{ID: "sandbox-1"})

	if want := []string{"set", "stop"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestSuccessfulCleanupArchivesImmediatelyWhenAutoArchiveSetFails(t *testing.T) {
	b := &Bot{log: discardLogger(), cfg: Config{DaytonaAutoArchiveMinutes: 60}}
	var calls []string
	b.setAutoArchiveIntervalFn = func(context.Context, *daytona.Sandbox, *int) error {
		calls = append(calls, "set")
		return errors.New("set failed")
	}
	b.stopSandboxFn = func(context.Context, *daytona.Sandbox) error {
		calls = append(calls, "stop")
		return nil
	}
	b.archiveSandboxFn = func(context.Context, *daytona.Sandbox) error {
		calls = append(calls, "archive")
		return nil
	}

	b.stopAndArchiveSandbox(context.Background(), &daytona.Sandbox{ID: "sandbox-1"})

	if want := []string{"set", "stop", "archive"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestSuccessfulCleanupArchivesImmediatelyWhenStopFails(t *testing.T) {
	b := &Bot{log: discardLogger(), cfg: Config{DaytonaAutoArchiveMinutes: 60}}
	var calls []string
	b.setAutoArchiveIntervalFn = func(context.Context, *daytona.Sandbox, *int) error {
		calls = append(calls, "set")
		return nil
	}
	b.stopSandboxFn = func(context.Context, *daytona.Sandbox) error {
		calls = append(calls, "stop")
		return errors.New("stop failed")
	}
	b.archiveSandboxFn = func(context.Context, *daytona.Sandbox) error {
		calls = append(calls, "archive")
		return nil
	}

	b.stopAndArchiveSandbox(context.Background(), &daytona.Sandbox{ID: "sandbox-1"})

	if want := []string{"set", "stop", "stop", "archive"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}
