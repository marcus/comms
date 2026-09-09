package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/marcus/comms/internal/domain"
)

const (
	// retrievalFlushInterval bounds how long a retrieval waits before it is
	// durable. Comms Web polls receipts every four seconds, so a second is
	// comfortably inside one poll.
	retrievalFlushInterval = time.Second
	// retrievalFlushThreshold forces an early flush so a busy fleet does not
	// hold a growing map for a whole tick.
	retrievalFlushThreshold = 200
	// retrievalSuppressWindow collapses an agent re-reading the same message at
	// the same or a shallower depth. A deeper read always registers.
	retrievalSuppressWindow = 30 * time.Second
)

// RetrievalStore is the persistence this recorder needs. It is satisfied by
// MessageStore and stated separately so the recorder can be tested without one.
type RetrievalStore interface {
	RecordRetrievals(context.Context, []RetrievalEvent) error
}

// RetrievalStats is the recorder's operational counters, surfaced by doctor so
// that shed bookkeeping is visible rather than silent.
type RetrievalStats struct {
	Pending    int   `json:"pending"`
	Dropped    int64 `json:"dropped"`
	Overloaded int64 `json:"overloaded"`
}

type retrievalKey struct {
	message domain.MessageID
	agent   string
}

type retrievalEntry struct {
	depth RetrievalDepth
	at    time.Time
}

// RetrievalRecorder collects "this message reached this agent" observations off
// the read path and writes them in small batches.
//
// It is deliberately lossy under pressure: a read must never fail, slow down,
// or block on the single writer because of its own bookkeeping. Every method is
// safe on a nil receiver, so a Service assembled without a store simply records
// nothing.
type RetrievalRecorder struct {
	store RetrievalStore
	clock domain.Clock

	mu         sync.Mutex
	pending    map[retrievalKey]retrievalEntry
	recent     map[retrievalKey]retrievalEntry
	retry      []RetrievalEvent
	dropped    int64
	overloaded int64
	stopped    bool

	wake      chan struct{}
	closing   chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func NewRetrievalRecorder(store RetrievalStore, clock domain.Clock) *RetrievalRecorder {
	if store == nil {
		return nil
	}
	if clock == nil {
		clock = domain.UTCClock{}
	}
	r := &RetrievalRecorder{
		store:   store,
		clock:   clock,
		pending: map[retrievalKey]retrievalEntry{},
		recent:  map[retrievalKey]retrievalEntry{},
		wake:    make(chan struct{}, 1),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
	go r.run()
	return r
}

// Observe records that messages reached agent at depth. It never blocks on the
// store and never reports an error to its caller.
//
// A message the reader wrote is skipped here when the reference is the author's
// stable ID; the store skips it definitively, because a handle or alias cannot
// be compared without resolving it.
func (r *RetrievalRecorder) Observe(agent string, depth RetrievalDepth, messages []domain.Message) {
	if r == nil || agent == "" || len(messages) == 0 {
		return
	}
	now := r.clock.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	for _, message := range messages {
		if agent == string(message.AuthorID) {
			continue
		}
		key := retrievalKey{message: message.ID, agent: agent}
		if recent, seen := r.recent[key]; seen && now.Sub(recent.at) < retrievalSuppressWindow && !depth.Deeper(recent.depth) {
			continue
		}
		r.recent[key] = retrievalEntry{depth: depth, at: now}
		entry, queued := r.pending[key]
		if !queued || depth.Deeper(entry.depth) {
			entry.depth = depth
		}
		if now.After(entry.at) {
			entry.at = now
		}
		r.pending[key] = entry
	}
	if len(r.pending) >= retrievalFlushThreshold {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// Flush writes everything pending synchronously. Tests and shutdown use it; the
// ordinary path is the background ticker.
func (r *RetrievalRecorder) Flush() {
	if r == nil {
		return
	}
	r.flush(context.Background())
}

// Close drains the recorder and stops its goroutine. It is idempotent.
func (r *RetrievalRecorder) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		r.mu.Unlock()
		close(r.closing)
		<-r.done
	})
	return nil
}

func (r *RetrievalRecorder) Stats() RetrievalStats {
	if r == nil {
		return RetrievalStats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return RetrievalStats{Pending: len(r.pending) + len(r.retry), Dropped: r.dropped, Overloaded: r.overloaded}
}

func (r *RetrievalRecorder) run() {
	ticker := time.NewTicker(retrievalFlushInterval)
	defer ticker.Stop()
	defer close(r.done)
	for {
		select {
		case <-ticker.C:
			r.flush(context.Background())
		case <-r.wake:
			r.flush(context.Background())
		case <-r.closing:
			r.flush(context.Background())
			return
		}
	}
}

func (r *RetrievalRecorder) flush(ctx context.Context) {
	r.mu.Lock()
	// A batch already carried over from an overloaded writer gets exactly one
	// more attempt; if it fails again the events are dropped and counted.
	retried := len(r.retry) != 0
	batch := r.retry
	r.retry = nil
	for key, entry := range r.pending {
		batch = append(batch, RetrievalEvent{MessageID: key.message, Agent: key.agent, Depth: entry.depth, At: entry.at})
		delete(r.pending, key)
	}
	r.pruneRecentLocked()
	r.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	err := r.store.RecordRetrievals(ctx, batch)
	if err == nil {
		return
	}
	r.mu.Lock()
	switch {
	case errors.Is(err, ErrOverloaded) && !retried && !r.stopped:
		r.overloaded++
		r.retry = batch
	default:
		r.dropped += int64(len(batch))
	}
	r.mu.Unlock()
}

func (r *RetrievalRecorder) pruneRecentLocked() {
	now := r.clock.Now()
	for key, entry := range r.recent {
		if now.Sub(entry.at) >= retrievalSuppressWindow {
			delete(r.recent, key)
		}
	}
}
