package bot

import (
	"encoding/json"

	"github.com/hetchyhq/hetchy/internal/runstore"
)

type runActivityEvent struct {
	name    string
	payload sseEvent
}

type liveRunActivitySummary struct {
	events []runActivityEvent
}

const liveRunActivityEventCap = 5000

func (s *liveRunActivitySummary) Record(name string, payload sseEvent) {
	s.events = append(s.events, runActivityEvent{name: name, payload: payload})
	if len(s.events) > liveRunActivityEventCap {
		s.events = s.events[len(s.events)-liveRunActivityEventCap:]
	}
}

func (s *liveRunActivitySummary) Activity(run runstore.Run) string {
	return appDataActivityFromEvents(run, s.events)
}

func (s *liveRunActivitySummary) CurrentStep(run runstore.Run) string {
	return appDataCurrentStepFromEvents(run, s.events)
}

func runActivityEventsFromStore(events []runstore.Event) []runActivityEvent {
	out := make([]runActivityEvent, 0, len(events))
	for _, event := range events {
		var payload sseEvent
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		out = append(out, runActivityEvent{name: event.Event, payload: payload})
	}
	return out
}

func liveRunActivityFallback() runstore.Run {
	return runstore.Run{
		State:       runstore.StateRunning,
		SandboxID:   "live",
		CommandStep: "run-script",
	}
}
