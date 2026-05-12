package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fanout maintains one Postgres LISTEN connection per process and
// dispatches notifications to local subscribers. A subscriber is a per-
// SSE-handler goroutine that wants events for one (orgID, threadID); it
// holds a *Subscription with a channel that buffers new events until the
// handler writes them to the client.
//
// The single LISTEN connection is significantly cheaper than one per
// subscriber: a busy replica with hundreds of attached tabs would
// otherwise pin hundreds of pgx connections in LISTEN mode and starve
// the rest of the pool.
type Fanout struct {
	log   *slog.Logger
	pool  *pgxpool.Pool
	store *Store

	mu   sync.Mutex
	subs map[string]map[*Subscription]struct{} // key: orgID + "|" + threadID

	closeOnce sync.Once
	closeCh   chan struct{}
	doneCh    chan struct{}
}

// Subscription is one consumer of the local fanout. The Ch channel
// receives every event with seq > the cursor the subscription was
// registered at. Buffer is 256 events; if the handler stalls and the
// buffer fills, the oldest event is dropped — the subscriber can recover
// by calling Replay with its last-seen seq.
type Subscription struct {
	orgID    string
	threadID string

	mu     sync.Mutex
	cursor int64
	closed bool
	ch     chan Event

	fanout *Fanout
}

// Ch returns the channel handlers read from.
func (s *Subscription) Ch() <-chan Event { return s.ch }

// Cursor returns the last seq this subscription has delivered. SSE
// handlers persist this as Last-Event-Id on the wire so a reconnect can
// resume from the same point.
func (s *Subscription) Cursor() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

// Close unregisters the subscription. Safe to call multiple times.
func (s *Subscription) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.fanout.unsubscribe(s)
}

// NewFanout boots a Fanout, opening the LISTEN connection in a background
// goroutine. It returns immediately; callers should call Close on shutdown.
func NewFanout(log *slog.Logger, pool *pgxpool.Pool, store *Store) *Fanout {
	f := &Fanout{
		log:     log,
		pool:    pool,
		store:   store,
		subs:    make(map[string]map[*Subscription]struct{}),
		closeCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go f.run()
	return f
}

// Subscribe registers a new subscriber starting at sinceSeq. The handler
// should first drain any backlog via the Store's Replay (which is
// independent of the LISTEN path) up to the live edge, then read from
// sub.Ch() for live updates. Events with seq <= sinceSeq are filtered out
// inside the fanout so the handler never sees duplicates from the
// replay-then-tail seam.
func (f *Fanout) Subscribe(orgID, threadID string, sinceSeq int64) *Subscription {
	sub := &Subscription{
		orgID:    orgID,
		threadID: threadID,
		cursor:   sinceSeq,
		ch:       make(chan Event, 256),
		fanout:   f,
	}
	key := keyFor(orgID, threadID)
	f.mu.Lock()
	if f.subs[key] == nil {
		f.subs[key] = make(map[*Subscription]struct{})
	}
	f.subs[key][sub] = struct{}{}
	f.mu.Unlock()
	return sub
}

func (f *Fanout) unsubscribe(sub *Subscription) {
	key := keyFor(sub.orgID, sub.threadID)
	f.mu.Lock()
	defer f.mu.Unlock()
	bucket := f.subs[key]
	if bucket == nil {
		return
	}
	delete(bucket, sub)
	if len(bucket) == 0 {
		delete(f.subs, key)
	}
	close(sub.ch)
}

// Close stops the LISTEN goroutine and closes every active subscription.
func (f *Fanout) Close() {
	f.closeOnce.Do(func() { close(f.closeCh) })
	<-f.doneCh
}

// hasSubscribers reports whether any subscription exists for the
// (orgID, threadID) pair. Used to skip a Replay round-trip when nobody
// on this replica cares about the notification.
func (f *Fanout) hasSubscribers(orgID, threadID string) bool {
	key := keyFor(orgID, threadID)
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs[key]) > 0
}

func (f *Fanout) run() {
	defer close(f.doneCh)
	backoff := time.Second
	for {
		select {
		case <-f.closeCh:
			return
		default:
		}
		if err := f.listenOnce(); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			f.log.Warn("events fanout: listen loop exited", "error", err, "backoff", backoff)
			select {
			case <-f.closeCh:
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

// listenOnce acquires a connection from the pool, issues LISTEN, and
// blocks on WaitForNotification until either Close fires or the
// connection drops. Returning nil is treated as "loop again from a clean
// state"; a non-nil error triggers backoff.
func (f *Fanout) listenOnce() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-f.closeCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire listen conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+NotifyChannel); err != nil {
		return fmt.Errorf("listen %s: %w", NotifyChannel, err)
	}
	f.log.Info("events fanout: listening", "channel", NotifyChannel)

	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("wait notification: %w", err)
		}
		f.handleNotify(ctx, n)
	}
}

func (f *Fanout) handleNotify(ctx context.Context, n *pgconn.Notification) {
	orgID, threadID, seq, ok := parseNotifyPayload(n.Payload)
	if !ok {
		f.log.Warn("events fanout: malformed notify payload", "payload", n.Payload)
		return
	}
	_ = seq // the seq is just a wake-up hint; the cursor on each sub
	// determines what we actually deliver.
	if !f.hasSubscribers(orgID, threadID) {
		return
	}
	// Find the smallest cursor across local subscribers and read
	// everything new in one Replay call, then deliver to each subscriber
	// filtered by its own cursor. Avoids one DB round-trip per
	// subscriber when several tabs are watching the same chat.
	minCursor := f.minCursor(orgID, threadID)
	rep, err := f.store.Replay(ctx, orgID, threadID, minCursor)
	if err != nil {
		f.log.Warn("events fanout: replay failed",
			"org", orgID, "thread", threadID, "since", minCursor, "error", err)
		return
	}
	if len(rep) == 0 {
		return
	}
	f.deliver(orgID, threadID, rep)
}

func (f *Fanout) minCursor(orgID, threadID string) int64 {
	key := keyFor(orgID, threadID)
	f.mu.Lock()
	defer f.mu.Unlock()
	var min int64 = -1
	for sub := range f.subs[key] {
		c := sub.Cursor()
		if min < 0 || c < min {
			min = c
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

func (f *Fanout) deliver(orgID, threadID string, events []Event) {
	key := keyFor(orgID, threadID)
	f.mu.Lock()
	subs := make([]*Subscription, 0, len(f.subs[key]))
	for sub := range f.subs[key] {
		subs = append(subs, sub)
	}
	f.mu.Unlock()

	for _, sub := range subs {
		sub.mu.Lock()
		closed := sub.closed
		cursor := sub.cursor
		sub.mu.Unlock()
		if closed {
			continue
		}
		overflow := false
		for _, ev := range events {
			if ev.Seq <= cursor {
				continue
			}
			select {
			case sub.ch <- ev:
				sub.mu.Lock()
				if ev.Seq > sub.cursor {
					sub.cursor = ev.Seq
				}
				sub.mu.Unlock()
				cursor = ev.Seq
			default:
				// Slow client: the 256-event buffer is full. We
				// previously dropped the oldest event and advanced
				// the cursor, but that caused permanent event loss
				// because a reconnect with Last-Event-Id reads from
				// the advanced cursor and skips the dropped event.
				//
				// Cleaner: terminate the subscription. The browser's
				// EventSource auto-reconnects, the new
				// chatStreamHandler does a fresh Replay from the
				// last-delivered seq (which the SSE handler tracks
				// independently), and no event is lost.
				overflow = true
			}
			if overflow {
				break
			}
		}
		if overflow {
			f.log.Warn("events fanout: subscriber buffer overflowed; terminating subscription so the client reconnects",
				"org", orgID, "thread", threadID, "cursor", cursor)
			f.unsubscribe(sub)
		}
	}
}

func keyFor(orgID, threadID string) string {
	return orgID + "|" + threadID
}

// parseNotifyPayload splits "<orgID>|<threadID>:<seq>". The colon-
// delimited seq is on the end so org IDs containing colons (UUIDs don't,
// but defensive) parse correctly.
func parseNotifyPayload(p string) (orgID, threadID string, seq int64, ok bool) {
	orgID, rest, found := strings.Cut(p, "|")
	if !found {
		return "", "", 0, false
	}
	colon := strings.LastIndexByte(rest, ':')
	if colon < 0 {
		return "", "", 0, false
	}
	threadID = rest[:colon]
	n, err := strconv.ParseInt(rest[colon+1:], 10, 64)
	if err != nil {
		return "", "", 0, false
	}
	return orgID, threadID, n, true
}
