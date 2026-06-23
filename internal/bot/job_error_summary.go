package bot

import "strings"

const jobLastErrorSummaryMaxRunes = 180

func jobLastErrorView(raw string) (summary, detail string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	summary = summarizeJobLastError(raw)
	if summary == "" {
		summary = truncate(compactJobErrorLine(raw), jobLastErrorSummaryMaxRunes)
	}
	summary = truncate(summary, jobLastErrorSummaryMaxRunes)
	if jobErrorNeedsDetail(raw, summary) {
		detail = raw
	}
	return summary, detail
}

func summarizeJobLastError(raw string) string {
	lower := strings.ToLower(raw)
	for _, needle := range []string{
		"claude did not produce a transcript within",
		"codex did not produce a transcript within",
	} {
		if frag := jobErrorFragmentContaining(raw, needle); frag != "" {
			return "Agent startup failed: " + ensureJobErrorSentence(humanizeJobErrorFragment(frag))
		}
	}
	if strings.Contains(lower, "agent setup exited before runtime") {
		if summary := jobSetupFailureSummary(raw); summary != "" {
			return summary
		}
		return "Agent setup failed before the run started."
	}
	if rest, ok := cutJobErrorPrefix(raw, "load org config:"); ok {
		return "Could not load organization settings: " + ensureJobErrorSentence(truncate(compactJobErrorLine(rest), 110))
	}
	if rest, ok := cutJobErrorPrefix(raw, "load durable run:"); ok {
		return "Could not read the completed run status: " + ensureJobErrorSentence(truncate(compactJobErrorLine(rest), 110))
	}
	if strings.Contains(lower, "durable run was not created") {
		return "The scheduled job did not create an agent run."
	}
	if rest, ok := cutJobErrorPrefix(raw, "job run ended in state "); ok {
		return "Job run ended in state " + ensureJobErrorSentence(truncate(compactJobErrorLine(rest), 80))
	}
	return truncate(compactJobErrorLine(raw), jobLastErrorSummaryMaxRunes)
}

func jobErrorNeedsDetail(raw, summary string) bool {
	compact := compactJobErrorLine(raw)
	if compact == summary || strings.EqualFold(compact, summary) {
		return false
	}
	return strings.ContainsAny(raw, "\n|") ||
		strings.Contains(compact, ": ") ||
		len([]rune(compact)) > jobLastErrorSummaryMaxRunes
}

func compactJobErrorLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func jobErrorFragmentContaining(raw, needle string) string {
	lower := strings.ToLower(raw)
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return ""
	}
	start := 0
	if i := strings.LastIndex(lower[:idx], "\n"); i >= 0 {
		start = i + 1
	}
	if i := strings.LastIndex(lower[:idx], " | "); i >= 0 && i+3 > start {
		start = i + 3
	}
	end := len(raw)
	if i := strings.Index(lower[idx:], "\n"); i >= 0 {
		end = min(end, idx+i)
	}
	if i := strings.Index(lower[idx:], " | "); i >= 0 {
		end = min(end, idx+i)
	}
	frag := strings.TrimSpace(raw[start:end])
	if i := strings.Index(frag, "__HETCHY_RUN_END"); i >= 0 {
		frag = frag[:i]
	}
	return compactJobErrorLine(frag)
}

func humanizeJobErrorFragment(s string) string {
	s = strings.Trim(s, " \t\r\n:;.-")
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "claude "):
		return "Claude " + s[len("claude "):]
	case strings.HasPrefix(lower, "codex "):
		return "Codex " + s[len("codex "):]
	default:
		return upperFirstASCII(s)
	}
}

func jobSetupFailureSummary(raw string) string {
	step, exitCode := jobErrorStepExit(raw)
	if step == "" {
		return ""
	}
	action := strings.TrimSuffix(friendlyRunCommandStep(step), ".")
	if action == "" {
		action = strings.ReplaceAll(step, "-", " ")
	}
	action = lowerFirstASCII(action)
	if exitCode != "" {
		return "Agent startup failed while " + action + " (exit code " + exitCode + ")."
	}
	return "Agent startup failed while " + action + "."
}

func jobErrorStepExit(raw string) (step, exitCode string) {
	lower := strings.ToLower(raw)
	marker := `step "`
	idx := strings.Index(lower, marker)
	if idx < 0 {
		return "", ""
	}
	start := idx + len(marker)
	end := strings.Index(raw[start:], `"`)
	if end < 0 {
		return "", ""
	}
	step = raw[start : start+end]
	after := strings.TrimSpace(raw[start+end+1:])
	fields := strings.Fields(after)
	if len(fields) >= 2 && strings.EqualFold(fields[0], "exit") {
		exitCode = strings.TrimRight(fields[1], ":,;.")
	}
	return step, exitCode
}

func cutJobErrorPrefix(raw, prefix string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(raw[len(prefix):]), true
}

func ensureJobErrorSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	last := s[len(s)-1]
	if last == '.' || last == '!' || last == '?' {
		return s
	}
	return s + "."
}

func upperFirstASCII(s string) string {
	if s == "" {
		return ""
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}

func lowerFirstASCII(s string) string {
	if s == "" {
		return ""
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}
