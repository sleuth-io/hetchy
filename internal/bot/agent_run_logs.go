package bot

import (
	"fmt"
	"strconv"
	"strings"
)

func hetchyRunBeginSentinel(runID string) string {
	return "__HETCHY_RUN_BEGIN " + runID + "__"
}

func hetchyRunEndPrefix(runID string) string {
	return "__HETCHY_RUN_END " + runID + " "
}

func framedAgentCommand(runID, scriptPath string) string {
	begin := hetchyRunBeginSentinel(runID)
	endPrefix := hetchyRunEndPrefix(runID)
	inner := "printf " + shellQuote(begin+"\n") +
		"; bash " + shellQuote(scriptPath) + " 2>&1" +
		"; code=$?" +
		"; printf " + shellQuote(endPrefix+"%s__\n") + ` "$code"` +
		`; exit "$code"`
	return "bash -c " + shellQuote(inner)
}

type hetchyFrameRouter struct {
	runID    string
	onLine   func(string)
	onCursor func(int64)

	cursor    int64
	inFrame   bool
	seenBegin bool
	seenEnd   bool
	exitCode  int
}

func newHetchyFrameRouter(runID string, onLine func(string), onCursor func(int64)) *hetchyFrameRouter {
	return &hetchyFrameRouter{runID: runID, onLine: onLine, onCursor: onCursor}
}

func (r *hetchyFrameRouter) Line(line string) {
	// shLines strips the newline before calling us. Daytona's snapshot
	// cursor is byte-oriented, so this streaming cursor is an approximation
	// at line granularity; recovery treats it as a lower bound.
	r.cursor += int64(len(line) + 1)
	switch {
	case line == hetchyRunBeginSentinel(r.runID):
		r.inFrame = true
		r.seenBegin = true
		r.markCursor()
		return
	case strings.HasPrefix(line, hetchyRunEndPrefix(r.runID)) && strings.HasSuffix(line, "__"):
		r.inFrame = false
		r.seenEnd = true
		r.exitCode, _ = parseHetchyRunExitCode(r.runID, line)
		r.markCursor()
		return
	case !r.inFrame:
		r.markCursor()
		return
	default:
		r.onLine(line)
		r.markCursor()
	}
}

func (r *hetchyFrameRouter) SeenBegin() bool { return r.seenBegin }
func (r *hetchyFrameRouter) SeenEnd() bool   { return r.seenEnd }
func (r *hetchyFrameRouter) ExitCode() int   { return r.exitCode }

func (r *hetchyFrameRouter) markCursor() {
	if r.onCursor != nil {
		r.onCursor(r.cursor)
	}
}

func parseHetchyRunExitCode(runID, line string) (int, bool) {
	rest, ok := strings.CutPrefix(line, hetchyRunEndPrefix(runID))
	if !ok || !strings.HasSuffix(rest, "__") {
		return 0, false
	}
	raw := strings.TrimSuffix(rest, "__")
	code, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return code, true
}

type replayFrameResult struct {
	SeenBegin bool
	SeenEnd   bool
	ExitCode  int
	Cursor    int64
}

func replayHetchyFramedLog(runID, logText string, suppressThrough int64, gate interface{ SetSuppressed(bool) }, onLine func(string), onCursor func(int64)) replayFrameResult {
	result := replayFrameResult{}
	inFrame := false
	cursor := int64(0)
	for len(logText) > 0 {
		next := strings.IndexByte(logText, '\n')
		var raw string
		if next < 0 {
			raw = logText
			logText = ""
		} else {
			raw = logText[:next]
			logText = logText[next+1:]
		}
		lineBytes := int64(len(raw))
		if next >= 0 {
			lineBytes++
		}
		lineStart := cursor
		cursor += lineBytes
		result.Cursor = cursor

		if raw == hetchyRunBeginSentinel(runID) {
			inFrame = true
			result.SeenBegin = true
			if onCursor != nil {
				onCursor(cursor)
			}
			continue
		}
		if strings.HasPrefix(raw, hetchyRunEndPrefix(runID)) && strings.HasSuffix(raw, "__") {
			inFrame = false
			result.SeenEnd = true
			result.ExitCode, _ = parseHetchyRunExitCode(runID, raw)
			if onCursor != nil {
				onCursor(cursor)
			}
			continue
		}
		if !inFrame {
			if onCursor != nil {
				onCursor(cursor)
			}
			continue
		}
		if gate != nil {
			gate.SetSuppressed(lineStart < suppressThrough)
		}
		onLine(raw)
		if onCursor != nil {
			onCursor(cursor)
		}
	}
	if gate != nil {
		gate.SetSuppressed(false)
	}
	return result
}

func errMissingHetchyFrame(runID string) error {
	return fmt.Errorf("agent run %s log did not contain a complete Hetchy frame", runID)
}
