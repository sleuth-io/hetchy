package blocks

import "time"

// Tee fans every Emitter call out to all wrapped emitters. The first
// emitter's Start id is the canonical one returned to the caller; the
// other emitters' Start return values are mapped onto it so subsequent
// Append/Done/Fail calls can be re-routed correctly.
//
// In practice the bot uses Tee(recorder, transport): the recorder's id
// is canonical (it's the first emitter), and the transport (web SSE or
// Slack) tracks blocks by whatever id it sees passed to Append/Done.
func Tee(emitters ...Emitter) Emitter {
	if len(emitters) == 1 {
		return emitters[0]
	}
	return &teeEmitter{
		emitters: emitters,
		idMap:    map[string][]string{},
	}
}

// withStartAt / withDoneAt / withFailAt are optional capabilities a
// wrapped emitter can implement to receive a timestamp the tee
// sampled once, rather than sampling time.Now() itself. This keeps
// the recorder's persisted StartedAt/EndedAt aligned with the live
// SSE emitter's wire-level timestamps — without it, the two read
// time.Now() microseconds apart and can render different HH:MM
// values across a wall-clock minute boundary.
type withStartAt interface {
	StartAt(kind Kind, title string, meta map[string]any, startedAt time.Time) string
}
type withDoneAt interface {
	DoneAt(id, summary string, endedAt time.Time)
}
type withFailAt interface {
	FailAt(id, summary string, endedAt time.Time)
}
type withHeartbeat interface {
	Heartbeat(title, body, elapsed string)
}

type teeEmitter struct {
	emitters []Emitter
	// idMap[canonicalID] = [id from emitter[0], id from emitter[1], ...]
	idMap map[string][]string
}

func (t *teeEmitter) Start(kind Kind, title string, meta map[string]any) string {
	now := time.Now().UTC()
	ids := make([]string, len(t.emitters))
	for i, e := range t.emitters {
		if sa, ok := e.(withStartAt); ok {
			ids[i] = sa.StartAt(kind, title, meta, now)
		} else {
			ids[i] = e.Start(kind, title, meta)
		}
	}
	t.idMap[ids[0]] = ids
	return ids[0]
}

func (t *teeEmitter) Append(id, delta string) {
	// A double-close (Done followed by another Append/Done) deletes
	// the id from idMap. Without the nil check, the next call would
	// index into a nil slice and panic — taking down the whole
	// request goroutine. Silently ignore unknown ids instead.
	ids, ok := t.idMap[id]
	if !ok {
		return
	}
	for i, e := range t.emitters {
		e.Append(ids[i], delta)
	}
}

func (t *teeEmitter) Done(id, summary string) {
	ids, ok := t.idMap[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	for i, e := range t.emitters {
		if da, ok := e.(withDoneAt); ok {
			da.DoneAt(ids[i], summary, now)
		} else {
			e.Done(ids[i], summary)
		}
	}
	delete(t.idMap, id)
}

func (t *teeEmitter) Fail(id, summary string) {
	ids, ok := t.idMap[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	for i, e := range t.emitters {
		if fa, ok := e.(withFailAt); ok {
			fa.FailAt(ids[i], summary, now)
		} else {
			e.Fail(ids[i], summary)
		}
	}
	delete(t.idMap, id)
}

func (t *teeEmitter) Notify(title, body string) {
	t.oneShotAt(KindNotify, title, body, StatusDone)
}

func (t *teeEmitter) Heartbeat(title, body, elapsed string) {
	for _, e := range t.emitters {
		if hb, ok := e.(withHeartbeat); ok {
			hb.Heartbeat(title, body, elapsed)
		}
	}
}

func (t *teeEmitter) Result(title, body string) {
	t.oneShotAt(KindResult, title, body, StatusDone)
}

func (t *teeEmitter) Error(title, body string) {
	t.oneShotAt(KindError, title, body, StatusError)
}

// oneShotAt mirrors the Start/Done/Fail unification: sample a single
// `now` and pass it to every wrapped emitter, so all of them stamp
// the same StartedAt/EndedAt for a one-shot block. Without this, each
// wrapped emitter's own Notify/Result/Error helper would internally
// call Start (one time.Now sample) and Done/Fail (another), and a
// pair of wrapped emitters could end up with four independent reads
// — producing different HH:MM chips between the live SSE stream and
// the post-reload replay at minute boundaries.
func (t *teeEmitter) oneShotAt(kind Kind, title, body string, status Status) {
	now := time.Now().UTC()
	ids := make([]string, len(t.emitters))
	for i, e := range t.emitters {
		if sa, ok := e.(withStartAt); ok {
			ids[i] = sa.StartAt(kind, title, nil, now)
		} else {
			ids[i] = e.Start(kind, title, nil)
		}
	}
	if body != "" {
		for i, e := range t.emitters {
			e.Append(ids[i], body)
		}
	}
	for i, e := range t.emitters {
		if status == StatusError {
			if fa, ok := e.(withFailAt); ok {
				fa.FailAt(ids[i], "", now)
			} else {
				e.Fail(ids[i], "")
			}
		} else {
			if da, ok := e.(withDoneAt); ok {
				da.DoneAt(ids[i], "", now)
			} else {
				e.Done(ids[i], "")
			}
		}
	}
}
