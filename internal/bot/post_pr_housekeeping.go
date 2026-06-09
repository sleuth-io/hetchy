package bot

import (
	"context"
	"sync"
)

// postPRHousekeeping collects best-effort post-PR maintenance (spec
// reflection, MarkApplied) so callers can deliver the user-visible
// result first and run the maintenance afterwards. Without it the
// "Done!" block waits ~10-30s on sandbox reads that cannot change the
// outcome the user is waiting for.
//
// The holder travels via context because runAgent/runFollowUp sit
// behind the runAgentFn/runFollowUpFn test seams — threading a new
// return value through those signatures would ripple into every fake.
// When no holder is present (a caller that has not opted in), the
// producer runs the maintenance inline, preserving the old ordering.
type postPRHousekeeping struct {
	mu  sync.Mutex
	fns []func(context.Context)
}

func (h *postPRHousekeeping) add(fn func(context.Context)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fns = append(h.fns, fn)
}

// run executes and clears the deferred tasks. Safe on a nil holder and
// idempotent: a second call finds an empty list.
func (h *postPRHousekeeping) run(ctx context.Context) {
	if h == nil {
		return
	}
	h.mu.Lock()
	fns := h.fns
	h.fns = nil
	h.mu.Unlock()
	for _, fn := range fns {
		fn(ctx)
	}
}

type postPRHousekeepingCtxKey struct{}

func contextWithPostPRHousekeeping(ctx context.Context) (context.Context, *postPRHousekeeping) {
	h := &postPRHousekeeping{}
	return context.WithValue(ctx, postPRHousekeepingCtxKey{}, h), h
}

func postPRHousekeepingFromContext(ctx context.Context) *postPRHousekeeping {
	h, _ := ctx.Value(postPRHousekeepingCtxKey{}).(*postPRHousekeeping)
	return h
}

// deferOrRunPostPRHousekeeping registers fn on the context's holder
// when one is present, otherwise runs it inline.
func deferOrRunPostPRHousekeeping(ctx context.Context, fn func(context.Context)) {
	if h := postPRHousekeepingFromContext(ctx); h != nil {
		h.add(fn)
		return
	}
	fn(ctx)
}
