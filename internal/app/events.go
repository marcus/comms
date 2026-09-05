package app

import "sync"

// EventSource hands a bounded waiter a coalesced change signal it can select
// on. Signals carry no payload: a waiter re-evaluates its own predicate
// against the authoritative store whenever it is woken.
type EventSource interface {
	// Subscribe registers a subscriber and returns its signal channel plus a
	// release function. Callers must subscribe before their first predicate
	// check so a change committed between the check and the wait cannot be
	// missed, and must always release.
	Subscribe() (<-chan struct{}, func())
}

// Notifier is the in-process EventSource used by Service. Sends never block:
// a subscriber that has not drained its channel already has an outstanding
// wake-up, and one wake-up is enough to force a fresh predicate check.
type Notifier struct {
	mu          sync.Mutex
	next        uint64
	subscribers map[uint64]chan struct{}
}

// Subscribe implements EventSource.
func (n *Notifier) Subscribe() (<-chan struct{}, func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.subscribers == nil {
		n.subscribers = make(map[uint64]chan struct{})
	}
	id := n.next
	n.next++
	signals := make(chan struct{}, 1)
	n.subscribers[id] = signals
	return signals, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		delete(n.subscribers, id)
	}
}

// Notify wakes every current subscriber. It is called after a mutation has
// been committed, never before.
func (n *Notifier) Notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, signals := range n.subscribers {
		select {
		case signals <- struct{}{}:
		default:
		}
	}
}

// Subscribers reports the number of live subscriptions. It exists so tests can
// prove that completed, timed-out, and cancelled waits release their slot.
func (n *Notifier) Subscribers() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.subscribers)
}
