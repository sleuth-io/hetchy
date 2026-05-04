package blocks

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

type teeEmitter struct {
	emitters []Emitter
	// idMap[canonicalID] = [id from emitter[0], id from emitter[1], ...]
	idMap map[string][]string
}

func (t *teeEmitter) Start(kind Kind, title string, meta map[string]any) string {
	ids := make([]string, len(t.emitters))
	for i, e := range t.emitters {
		ids[i] = e.Start(kind, title, meta)
	}
	t.idMap[ids[0]] = ids
	return ids[0]
}

func (t *teeEmitter) Append(id, delta string) {
	ids := t.idMap[id]
	for i, e := range t.emitters {
		e.Append(ids[i], delta)
	}
}

func (t *teeEmitter) Done(id, summary string) {
	ids := t.idMap[id]
	for i, e := range t.emitters {
		e.Done(ids[i], summary)
	}
	delete(t.idMap, id)
}

func (t *teeEmitter) Fail(id, summary string) {
	ids := t.idMap[id]
	for i, e := range t.emitters {
		e.Fail(ids[i], summary)
	}
	delete(t.idMap, id)
}

func (t *teeEmitter) Notify(title, body string) {
	for _, e := range t.emitters {
		e.Notify(title, body)
	}
}

func (t *teeEmitter) Result(title, body string) {
	for _, e := range t.emitters {
		e.Result(title, body)
	}
}

func (t *teeEmitter) Error(title, body string) {
	for _, e := range t.emitters {
		e.Error(title, body)
	}
}
