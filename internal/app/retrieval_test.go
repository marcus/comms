package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/marcus/comms/internal/domain"
)

type movableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *movableClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *movableClock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type retrievalStoreFake struct {
	mu       sync.Mutex
	batches  [][]RetrievalEvent
	failWith []error
}

func (f *retrievalStoreFake) RecordRetrievals(_ context.Context, events []RetrievalEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.failWith) != 0 {
		err := f.failWith[0]
		f.failWith = f.failWith[1:]
		if err != nil {
			return err
		}
	}
	f.batches = append(f.batches, append([]RetrievalEvent(nil), events...))
	return nil
}

func (f *retrievalStoreFake) recorded() []RetrievalEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []RetrievalEvent
	for _, batch := range f.batches {
		all = append(all, batch...)
	}
	return all
}

func message(id domain.MessageID, author domain.AgentID) domain.Message {
	return domain.Message{ID: id, AuthorID: author}
}

func TestRecorderCoalescesEscalatesAndSkipsTheAuthor(t *testing.T) {
	fake := &retrievalStoreFake{}
	clock := &movableClock{now: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)}
	recorder := NewRetrievalRecorder(fake, clock)
	t.Cleanup(func() { _ = recorder.Close() })

	messages := []domain.Message{message("msg_one", "agt_author"), message("msg_two", "agt_reader")}
	recorder.Observe("agt_reader", RetrievalPreview, messages)
	recorder.Observe("agt_reader", RetrievalFull, messages)
	recorder.Flush()

	events := fake.recorded()
	if len(events) != 1 {
		t.Fatalf("events=%#v", events)
	}
	if events[0].MessageID != "msg_one" || events[0].Agent != "agt_reader" || events[0].Depth != RetrievalFull {
		t.Fatalf("coalesced event=%#v", events[0])
	}
}

func TestRecorderSuppressesRepeatsButNotDeeperReadsOrLaterWindows(t *testing.T) {
	fake := &retrievalStoreFake{}
	clock := &movableClock{now: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)}
	recorder := NewRetrievalRecorder(fake, clock)
	t.Cleanup(func() { _ = recorder.Close() })
	one := []domain.Message{message("msg_one", "agt_author")}

	recorder.Observe("agt_reader", RetrievalPreview, one)
	recorder.Flush()
	clock.Advance(time.Second)
	recorder.Observe("agt_reader", RetrievalPreview, one)
	recorder.Flush()
	if got := len(fake.recorded()); got != 1 {
		t.Fatalf("repeat preview inside the window produced %d events", got)
	}
	// A deeper read inside the window always registers.
	recorder.Observe("agt_reader", RetrievalFull, one)
	recorder.Flush()
	events := fake.recorded()
	if len(events) != 2 || events[1].Depth != RetrievalFull {
		t.Fatalf("escalation inside the window=%#v", events)
	}
	// A preview after a full does not re-fire.
	recorder.Observe("agt_reader", RetrievalPreview, one)
	recorder.Flush()
	if got := len(fake.recorded()); got != 2 {
		t.Fatalf("preview after full produced %d events", got)
	}
	// Past the window the same depth counts as a fresh visit.
	clock.Advance(retrievalSuppressWindow)
	recorder.Observe("agt_reader", RetrievalPreview, one)
	recorder.Flush()
	if got := len(fake.recorded()); got != 3 {
		t.Fatalf("repeat after the window produced %d events", got)
	}
}

func TestRecorderRetriesAnOverloadedWriterOnceThenDropsAndCounts(t *testing.T) {
	fake := &retrievalStoreFake{failWith: []error{ErrOverloaded}}
	clock := &movableClock{now: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)}
	recorder := NewRetrievalRecorder(fake, clock)
	t.Cleanup(func() { _ = recorder.Close() })

	recorder.Observe("agt_reader", RetrievalFull, []domain.Message{message("msg_one", "agt_author")})
	recorder.Flush()
	if stats := recorder.Stats(); stats.Overloaded != 1 || stats.Pending != 1 || stats.Dropped != 0 {
		t.Fatalf("after overload stats=%#v", stats)
	}
	recorder.Flush()
	if got := len(fake.recorded()); got != 1 {
		t.Fatalf("retry recorded %d events", got)
	}
	if stats := recorder.Stats(); stats.Pending != 0 || stats.Dropped != 0 {
		t.Fatalf("after successful retry stats=%#v", stats)
	}

	// A second consecutive failure drops the batch rather than growing forever.
	fake.mu.Lock()
	fake.failWith = []error{ErrOverloaded, ErrOverloaded}
	fake.mu.Unlock()
	clock.Advance(retrievalSuppressWindow)
	recorder.Observe("agt_reader", RetrievalFull, []domain.Message{message("msg_one", "agt_author")})
	recorder.Flush()
	recorder.Flush()
	stats := recorder.Stats()
	if stats.Dropped != 1 || stats.Pending != 0 || stats.Overloaded != 2 {
		t.Fatalf("after repeated overload stats=%#v", stats)
	}
	// A permanent store error is dropped immediately, not retried.
	fake.mu.Lock()
	fake.failWith = []error{errors.New("disk on fire")}
	fake.mu.Unlock()
	clock.Advance(retrievalSuppressWindow)
	recorder.Observe("agt_reader", RetrievalFull, []domain.Message{message("msg_one", "agt_author")})
	recorder.Flush()
	if stats = recorder.Stats(); stats.Dropped != 2 || stats.Pending != 0 {
		t.Fatalf("after hard failure stats=%#v", stats)
	}
}

func TestRecorderFlushesOnCloseAndIgnoresLaterObservations(t *testing.T) {
	fake := &retrievalStoreFake{}
	clock := &movableClock{now: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)}
	recorder := NewRetrievalRecorder(fake, clock)
	recorder.Observe("agt_reader", RetrievalPreview, []domain.Message{message("msg_one", "agt_author")})
	if e := recorder.Close(); e != nil {
		t.Fatal(e)
	}
	if e := recorder.Close(); e != nil {
		t.Fatalf("second close=%v", e)
	}
	if got := len(fake.recorded()); got != 1 {
		t.Fatalf("close drained %d events", got)
	}
	recorder.Observe("agt_reader", RetrievalFull, []domain.Message{message("msg_two", "agt_author")})
	recorder.Flush()
	if got := len(fake.recorded()); got != 1 {
		t.Fatalf("observation after close recorded %d events", got)
	}
}

func TestNilRecorderIsSafe(t *testing.T) {
	var recorder *RetrievalRecorder
	recorder.Observe("agt_reader", RetrievalFull, []domain.Message{message("msg_one", "agt_author")})
	recorder.Flush()
	if e := recorder.Close(); e != nil {
		t.Fatal(e)
	}
	if stats := recorder.Stats(); stats != (RetrievalStats{}) {
		t.Fatalf("stats=%#v", stats)
	}
	if NewRetrievalRecorder(nil, nil) != nil {
		t.Fatal("a recorder without a store must be nil")
	}
}
