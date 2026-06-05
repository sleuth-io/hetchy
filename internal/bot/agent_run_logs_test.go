package bot

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

func runEventForTest(t *testing.T, event string, payload sseEvent) runstore.Event {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return runstore.Event{Event: event, Data: data}
}

func TestHetchyFrameRouterFiltersNoiseAndSentinels(t *testing.T) {
	var got []string
	var cursors []int64
	r := newHetchyFrameRouter("run_abc", func(line string) {
		got = append(got, line)
	}, func(cursor int64) {
		cursors = append(cursors, cursor)
	})

	for _, line := range []string{
		"daytona noise before",
		"__HETCHY_RUN_BEGIN run_abc__",
		"[hetchy] setup",
		`{"type":"result","result":"https://github.com/acme/repo/pull/1"}`,
		"__HETCHY_RUN_END run_abc 0__",
		"daytona noise after",
	} {
		r.Line(line)
	}

	want := []string{
		"[hetchy] setup",
		`{"type":"result","result":"https://github.com/acme/repo/pull/1"}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered lines = %#v, want %#v", got, want)
	}
	if !r.SeenBegin() || !r.SeenEnd() || r.ExitCode() != 0 {
		t.Fatalf("frame state = begin:%v end:%v exit:%d", r.SeenBegin(), r.SeenEnd(), r.ExitCode())
	}
	if len(cursors) == 0 || cursors[len(cursors)-1] == 0 {
		t.Fatalf("cursor was not advanced: %#v", cursors)
	}
}

func TestReplayHetchyFramedLogSuppressesConsumedPrefix(t *testing.T) {
	const runID = "run_abc"
	logText := "noise\n" +
		"__HETCHY_RUN_BEGIN run_abc__\n" +
		"[hetchy] first\n" +
		"[hetchy] second\n" +
		"__HETCHY_RUN_END run_abc 0__\n" +
		"tail\n"
	cursor := int64(len("noise\n" +
		"__HETCHY_RUN_BEGIN run_abc__\n" +
		"[hetchy] first\n"))

	gate := &testSuppressionGate{}
	var got []string
	res := replayHetchyFramedLog(runID, logText, cursor, gate, func(line string) {
		if !gate.suppressed {
			got = append(got, line)
		}
	}, nil)

	if !res.SeenBegin || !res.SeenEnd || res.ExitCode != 0 {
		t.Fatalf("frame result = %+v", res)
	}
	if want := []string{"[hetchy] second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed lines = %#v, want %#v", got, want)
	}
}

func TestReplayHetchyFramedLogStripsDaytonaControlPrefixes(t *testing.T) {
	const runID = "run_abc"
	logText := "\x01\x01__HETCHY_RUN_BEGIN run_abc__\n" +
		"\x01\x01\x01[hetchy] running claude\n" +
		"\x01{\"type\":\"result\",\"result\":\"https://github.com/acme/repo/pull/1\"}\n" +
		"\x01__HETCHY_RUN_END run_abc 0__\n"

	var got []string
	res := replayHetchyFramedLog(runID, logText, 0, &testSuppressionGate{}, func(line string) {
		got = append(got, line)
	}, nil)

	if !res.SeenBegin || !res.SeenEnd {
		t.Fatalf("frame result = %+v, want begin and end", res)
	}
	want := []string{
		"[hetchy] running claude",
		`{"type":"result","result":"https://github.com/acme/repo/pull/1"}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed lines = %#v, want %#v", got, want)
	}
}

func TestReplayHetchyFramedLogCursorBoundaryDoesNotDoubleEmit(t *testing.T) {
	const runID = "run_abc"
	logText := "__HETCHY_RUN_BEGIN run_abc__\n" +
		"[hetchy] persisted\n" +
		"[hetchy] new\n" +
		"__HETCHY_RUN_END run_abc 0__\n"
	cursorAfterPersistedLine := int64(len("__HETCHY_RUN_BEGIN run_abc__\n" +
		"[hetchy] persisted\n"))

	gate := &testSuppressionGate{}
	var got []string
	replayHetchyFramedLog(runID, logText, cursorAfterPersistedLine, gate, func(line string) {
		if !gate.suppressed {
			got = append(got, line)
		}
	}, nil)

	if want := []string{"[hetchy] new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed lines = %#v, want %#v", got, want)
	}
}

func TestReplayHetchyFramedLogStateContinuesFromSuffix(t *testing.T) {
	const runID = "run_abc"
	prefix := "__HETCHY_RUN_BEGIN run_abc__\n" +
		"[hetchy] first\n"
	suffix := "[hetchy] second\n"

	gate := &testSuppressionGate{}
	var got []string
	res, state := replayHetchyFramedLogState(runID, prefix, 0, 0, replayFrameState{}, gate, func(line string) {
		if !gate.suppressed {
			got = append(got, line)
		}
	}, nil)
	if !res.SeenBegin || !state.inFrame {
		t.Fatalf("first pass state = result:%+v state:%+v, want seen begin and in frame", res, state)
	}

	res, state = replayHetchyFramedLogState(runID, suffix, int64(len(prefix)), int64(len(prefix)), state, gate, func(line string) {
		if !gate.suppressed {
			got = append(got, line)
		}
	}, nil)
	if !res.SeenBegin || !state.inFrame {
		t.Fatalf("second pass state = result:%+v state:%+v, want frame preserved", res, state)
	}
	if want := []string{"[hetchy] first", "[hetchy] second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed lines = %#v, want %#v", got, want)
	}
}

func TestReplayHetchyFramedLogAdvancesCursorForSuppressedLines(t *testing.T) {
	const runID = "run_abc"
	logText := "__HETCHY_RUN_BEGIN run_abc__\n" +
		"[hetchy] first\n" +
		"[hetchy] second\n"
	suppressThrough := int64(len("__HETCHY_RUN_BEGIN run_abc__\n" +
		"[hetchy] first\n"))

	gate := &testSuppressionGate{}
	var cursors []int64
	replayHetchyFramedLog(runID, logText, suppressThrough, gate, func(string) {}, func(cursor int64) {
		cursors = append(cursors, cursor)
	})

	if len(cursors) != 3 {
		t.Fatalf("cursor callbacks = %#v, want begin + two framed lines", cursors)
	}
	if cursors[len(cursors)-1] != int64(len(logText)) {
		t.Fatalf("last cursor = %d, want %d", cursors[len(cursors)-1], len(logText))
	}
}

func TestBlocksFromRunEventsProjectsSSEBlocks(t *testing.T) {
	events := []runstore.Event{
		runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindSetup, Title: "Sandbox setup"}),
		runEventForTest(t, "block_append", sseEvent{ID: "p1", Delta: "Cloning\n"}),
		runEventForTest(t, "heartbeat", sseEvent{Title: "Still working"}),
		runEventForTest(t, "block_done", sseEvent{ID: "p1", Status: blocks.StatusDone, Summary: "ready"}),
	}

	got := blocksFromRunEvents(events)
	if len(got) != 1 {
		t.Fatalf("blocks = %#v, want one projected block", got)
	}
	if got[0].ID != "p1" || got[0].Kind != blocks.KindSetup || got[0].Body != "Cloning\n" || got[0].Status != blocks.StatusDone || got[0].Summary != "ready" {
		t.Fatalf("projected block = %+v", got[0])
	}
}

func TestRecoveredAgentRunEmitterReusesOnlyCommandBlockIDs(t *testing.T) {
	preCommand := runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindNotify, Title: "Starting"})
	preCommand.Seq = 1
	commandSetup := runEventForTest(t, "block_start", sseEvent{ID: "p2", Kind: blocks.KindSetup, Title: "Sandbox setup"})
	commandSetup.Seq = 4

	em := newRecoveredAgentRunEmitter(nil, runstore.Run{ID: "run_abc", CommandStartSeq: 4}, "worker", nil, []runstore.Event{
		preCommand,
		commandSetup,
	})

	if got := em.Start(blocks.KindSetup, "Sandbox setup", nil); got != "p2" {
		t.Fatalf("first recovered command block id = %q, want p2", got)
	}
	if got := em.Start(blocks.KindToolUse, "Running bash", nil); got != "p3" {
		t.Fatalf("next recovered block id = %q, want p3", got)
	}
}

func TestInitialRecoveryFrameStateResumesWhenCommandEventsExist(t *testing.T) {
	events := []runstore.Event{
		{Seq: 1, Event: "block_start"},
		{Seq: 7, Event: "block_append"},
	}
	state := initialRecoveryFrameState(runstore.Run{
		ID:              "run_abc",
		CommandStartSeq: 7,
		LogCursor:       123,
	}, events)

	if !state.seenBegin || !state.inFrame {
		t.Fatalf("state = %+v, want already inside frame", state)
	}
}

func TestInitialRecoveryFrameStateRequiresCommandEvents(t *testing.T) {
	state := initialRecoveryFrameState(runstore.Run{
		ID:              "run_abc",
		CommandStartSeq: 7,
		LogCursor:       123,
	}, []runstore.Event{{Seq: 6, Event: "block_append"}})

	if state.seenBegin || state.inFrame {
		t.Fatalf("state = %+v, want empty frame state", state)
	}
}

func TestReplayableCommandEventCountStopsAtTerminalBlock(t *testing.T) {
	events := []runstore.Event{
		runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindNotify, Title: "Starting"}),
		runEventForTest(t, "block_start", sseEvent{ID: "p2", Kind: blocks.KindSetup, Title: "Sandbox setup"}),
		runEventForTest(t, "block_append", sseEvent{ID: "p2", Delta: "Running setup\n"}),
		runEventForTest(t, "heartbeat", sseEvent{Title: "Still working"}),
		runEventForTest(t, "block_start", sseEvent{ID: "p3", Kind: blocks.KindError, Title: "Agent failed"}),
		runEventForTest(t, "block_append", sseEvent{ID: "p3", Delta: "terminal"}),
	}
	for i := range events {
		events[i].Seq = int64(i + 1)
	}

	if got := replayableCommandEventCount(events, 2); got != 2 {
		t.Fatalf("replayableCommandEventCount = %d, want 2", got)
	}
	maxID, replayIDs := recoveredAgentRunEmitterIDs(events, 2)
	if maxID != 3 {
		t.Fatalf("maxID = %d, want 3", maxID)
	}
	if want := []string{"p2"}; !reflect.DeepEqual(replayIDs, want) {
		t.Fatalf("replayIDs = %#v, want %#v", replayIDs, want)
	}
}

func TestRecoveredTerminalEmitterStartsAfterExistingBlockIDs(t *testing.T) {
	preCommand := runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindNotify, Title: "Starting"})
	preCommand.Seq = 1
	commandSetup := runEventForTest(t, "block_start", sseEvent{ID: "p2", Kind: blocks.KindSetup, Title: "Sandbox setup"})
	commandSetup.Seq = 4

	em := newAgentRunEmitterAfterEvents(nil, "run_abc", "worker", nil, []runstore.Event{
		preCommand,
		commandSetup,
	})
	if got := em.Start(blocks.KindError, "Agent failed", nil); got != "p3" {
		t.Fatalf("terminal block id = %q, want p3", got)
	}
}

func TestAgentRunEmitterBeginBatchDetectsUnflushedBatch(t *testing.T) {
	em := newAgentRunEmitter(nil, "run_abc", "worker", nil)
	em.BeginBatch()
	em.Notify("Starting", "first batch")
	em.BeginBatch()

	if err := em.Err(); !errors.Is(err, errAgentRunDurability) {
		t.Fatalf("err = %v, want errAgentRunDurability", err)
	}
	if err := em.FlushBatch(1); !errors.Is(err, errAgentRunDurability) {
		t.Fatalf("flush err = %v, want errAgentRunDurability", err)
	}
}

func TestFramedAgentCommandContainsSentinels(t *testing.T) {
	cmd := framedAgentCommand("run_test", "/tmp/agent.sh")
	begin := hetchyRunBeginSentinel("run_test")
	endPrefix := hetchyRunEndPrefix("run_test")
	if !strings.Contains(cmd, begin) {
		t.Errorf("framedAgentCommand missing begin sentinel %q: %s", begin, cmd)
	}
	if !strings.Contains(cmd, endPrefix) {
		t.Errorf("framedAgentCommand missing end prefix %q: %s", endPrefix, cmd)
	}
	if !strings.Contains(cmd, "/tmp/agent.sh") {
		t.Errorf("framedAgentCommand missing script path: %s", cmd)
	}
	if !strings.HasPrefix(cmd, "bash -c ") {
		t.Errorf("framedAgentCommand should start with bash -c: %s", cmd)
	}
}

type testSuppressionGate struct {
	suppressed bool
}

func (g *testSuppressionGate) SetSuppressed(v bool) {
	g.suppressed = v
}
