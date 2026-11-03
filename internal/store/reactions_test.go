package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

func reactionMessage(t *testing.T, sys *testSystem, kind string, expiry app.Expiry) domain.Message {
	t.Helper()
	message, err := sys.service.DirectSend(context.Background(), app.DirectSendRequest{Author: "alice", Recipient: "bob", Title: "update", Body: "ready", Kind: kind, Expiry: expiry})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestReactionsConcurrentIdempotenceRemovalAndReceipts(t *testing.T) {
	sys := newTestSystem(t)
	defer func() { _ = sys.service.Close() }()
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	message := reactionMessage(t, sys, "ready", app.Expiry{})
	req := app.ReactRequest{Agent: "bob", Message: string(message.ID), Reaction: "👍"}
	var wg sync.WaitGroup
	results := make(chan app.ReactResponse, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := sys.service.React(ctx, req)
			if err != nil {
				errs <- err
			} else {
				results <- got
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	changed := 0
	for result := range results {
		if result.Changed {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("changed=%d", changed)
	}
	sys.clock.Advance(time.Second)
	again, err := sys.service.React(ctx, req)
	if err != nil || again.Changed {
		t.Fatalf("repeat=%+v %v", again, err)
	}
	peek, err := sys.service.Peek(ctx, app.PeekRequest{Message: string(message.ID)})
	if err != nil || len(peek.Reactions) != 1 || peek.Reactions[0].AgentID != bob.ID || !peek.Reactions[0].CreatedAt.Equal(again.Reaction.CreatedAt) {
		t.Fatalf("peek=%+v %v", peek, err)
	}
	box, err := sys.service.Inbox(ctx, app.MessageListRequest{Agent: "alice", Full: true})
	if err != nil || len(box.Items) != 0 || len(box.ReactionEvents) != 0 {
		t.Fatalf("default author inbox=%+v %v", box, err)
	}
	box, err = sys.service.Inbox(ctx, app.MessageListRequest{Agent: "alice", Full: true, IncludeReactions: true})
	if err != nil || len(box.Items) != 0 || len(box.ReactionEvents) != 1 || box.ReactionEvents[0].AuthorID != alice.ID || box.ReactionEvents[0].Removed {
		t.Fatalf("author events=%+v %v", box, err)
	}
	other, err := sys.service.Inbox(ctx, app.MessageListRequest{Agent: "bob", IncludeReactions: true})
	if err != nil || len(other.Items) != 1 || len(other.ReactionEvents) != 0 {
		t.Fatalf("recipient events=%+v %v", other, err)
	}
	var messages, advances, events int
	for query, dest := range map[string]*int{"SELECT count(*) FROM messages": &messages, "SELECT count(*) FROM subscription_read_advances": &advances, "SELECT count(*) FROM reaction_events": &events} {
		if err = sys.adapter.read.QueryRowContext(ctx, query).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if messages != 1 || advances != 0 || events != 1 {
		t.Fatalf("messages=%d advances=%d events=%d", messages, advances, events)
	}
	req.Remove = true
	removed, err := sys.service.React(ctx, req)
	if err != nil || !removed.Changed || !removed.Removed || !removed.Reaction.CreatedAt.Equal(again.Reaction.CreatedAt) {
		t.Fatalf("remove=%+v %v", removed, err)
	}
	removed, err = sys.service.React(ctx, req)
	if err != nil || removed.Changed {
		t.Fatalf("remove repeat=%+v %v", removed, err)
	}
	next, err := sys.adapter.ReactionEvents(ctx, app.ReactionEventsRequest{After: box.ReactionsAfter, AgentID: alice.ID, Limit: 1}, sys.clock.Now())
	if err != nil || len(next.Items) != 1 || !next.Items[0].Removed || next.Items[0].Type != "reaction" {
		t.Fatalf("remove events=%+v %v", next, err)
	}
	peek, err = sys.service.Peek(ctx, app.PeekRequest{Message: string(message.ID)})
	if err != nil || peek.Reactions == nil || len(peek.Reactions) != 0 {
		t.Fatalf("removed peek=%+v %v", peek, err)
	}
	snapshot, err := sys.service.Snapshot(ctx)
	if err != nil || len(snapshot.ReactionEvents) != 2 || len(snapshot.Messages) != 1 {
		t.Fatalf("snapshot=%+v %v", snapshot, err)
	}
}

func TestReactionEventsFiltersPaginationSelfAndHead(t *testing.T) {
	sys := newTestSystem(t)
	defer func() { _ = sys.service.Close() }()
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	cara := join(t, sys.service, "cara", nil)
	ready := reactionMessage(t, sys, "ready", app.Expiry{})
	blocked := reactionMessage(t, sys, "blocked", app.Expiry{})
	for _, input := range []struct {
		message      domain.Message
		agent, label string
	}{{ready, "alice", "self"}, {blocked, "bob", "noise"}, {ready, "cara", "agreed"}, {ready, "bob", "👍"}} {
		if _, err := sys.service.React(ctx, app.ReactRequest{Message: string(input.message.ID), Agent: input.agent, Reaction: input.label}); err != nil {
			t.Fatal(err)
		}
	}
	req := app.ReactionEventsRequest{AgentID: alice.ID, Kind: "ready", ThreadRootID: ready.ID, Topic: string(ready.TopicID), Limit: 1}
	first, err := sys.adapter.ReactionEvents(ctx, req, sys.clock.Now())
	if err != nil || len(first.Items) != 1 || first.Items[0].Reaction.AgentID != cara.ID {
		t.Fatalf("first=%+v %v", first, err)
	}
	req.After = first.After
	second, err := sys.adapter.ReactionEvents(ctx, req, sys.clock.Now())
	if err != nil || len(second.Items) != 1 || second.Items[0].Reaction.AgentID != bob.ID {
		t.Fatalf("second=%+v %v", second, err)
	}
	req.After = second.After
	empty, err := sys.adapter.ReactionEvents(ctx, req, sys.clock.Now())
	if err != nil || len(empty.Items) != 0 || empty.After != second.After {
		t.Fatalf("empty=%+v %v", empty, err)
	}
	req.After = ""
	req.IncludeSelf = true
	req.FromID = alice.ID
	self, err := sys.adapter.ReactionEvents(ctx, req, sys.clock.Now())
	if err != nil || len(self.Items) != 1 || self.Items[0].Reaction.Reaction != "self" {
		t.Fatalf("self=%+v %v", self, err)
	}
	head, err := sys.adapter.ReactionEvents(ctx, app.ReactionEventsRequest{Head: true}, sys.clock.Now())
	if err != nil || len(head.Items) != 0 {
		t.Fatalf("head=%+v %v", head, err)
	}
	next, err := sys.adapter.ReactionEvents(ctx, app.ReactionEventsRequest{After: head.After, Limit: 10}, sys.clock.Now())
	if err != nil || len(next.Items) != 0 {
		t.Fatalf("head next=%+v %v", next, err)
	}
	for _, bad := range []string{"nonsense", encodeCursor("observe", "1"), encodeCursor("reaction", "-1")} {
		if _, err := sys.adapter.ReactionEvents(ctx, app.ReactionEventsRequest{After: bad, Limit: 10}, sys.clock.Now()); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("bad cursor %q: %v", bad, err)
		}
	}
}

func TestReactionExpiryRetainedAncestorsAndPurgeCursor(t *testing.T) {
	sys := newTestSystem(t)
	defer func() { _ = sys.service.Close() }()
	ctx := context.Background()
	join(t, sys.service, "alice", nil)
	join(t, sys.service, "bob", nil)
	message := reactionMessage(t, sys, "ready", app.Expiry{After: time.Second})
	if _, err := sys.service.React(ctx, app.ReactRequest{Agent: "bob", Message: string(message.ID), Reaction: "seen"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sys.service.Reply(ctx, app.ReplyRequest{Author: "bob", Parent: string(message.ID), Body: "reply", Expiry: app.Expiry{After: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	before, err := sys.adapter.ReactionEvents(ctx, app.ReactionEventsRequest{Head: true}, sys.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	sys.clock.Advance(2 * time.Second)
	if _, err := sys.service.React(ctx, app.ReactRequest{Agent: "bob", Message: string(message.ID), Reaction: "late"}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("expired react=%v", err)
	}
	peek, err := sys.service.Peek(ctx, app.PeekRequest{Message: string(message.ID)})
	if err != nil || len(peek.Reactions) != 0 {
		t.Fatalf("expired retained peek=%+v %v", peek, err)
	}
	for _, latest := range []bool{false, true} {
		thread, err := sys.service.Thread(ctx, app.ThreadRequest{Message: string(message.ID), Latest: latest})
		if err != nil || len(thread.Items) != 2 || len(thread.Items[0].Reactions) != 0 {
			t.Fatalf("expired retained thread=%+v %v", thread, err)
		}
	}
	hidden, err := sys.adapter.ReactionEvents(ctx, app.ReactionEventsRequest{Limit: 10}, sys.clock.Now())
	if err != nil || len(hidden.Items) != 0 {
		t.Fatalf("expired events=%+v %v", hidden, err)
	}
	sys.clock.Advance(2 * time.Hour)
	if _, err := sys.service.Purge(ctx, app.PurgeRequest{}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"reaction_events", "message_reactions"} {
		var count int
		if err := sys.adapter.read.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d %v", table, count, err)
		}
	}
	after, err := sys.adapter.ReactionEvents(ctx, app.ReactionEventsRequest{Head: true}, sys.clock.Now())
	if err != nil || after.After != before.After {
		t.Fatalf("purge head before=%+v after=%+v %v", before, after, err)
	}
	nextMessage := reactionMessage(t, sys, "ready", app.Expiry{})
	if _, err := sys.service.React(ctx, app.ReactRequest{Agent: "bob", Message: string(nextMessage.ID), Reaction: "new"}); err != nil {
		t.Fatal(err)
	}
	next, err := sys.adapter.ReactionEvents(ctx, app.ReactionEventsRequest{After: before.After, Limit: 10}, sys.clock.Now())
	if err != nil || len(next.Items) != 1 || next.Items[0].MessageID != nextMessage.ID {
		t.Fatalf("post purge next=%+v %v", next, err)
	}
}

func TestReactionsOnlySatisfyOptedInWaits(t *testing.T) {
	sys := newTestSystem(t)
	defer func() { _ = sys.service.Close() }()
	ctx := context.Background()
	join(t, sys.service, "alice", nil)
	join(t, sys.service, "bob", nil)
	message := reactionMessage(t, sys, "ready", app.Expiry{})
	if _, err := sys.service.React(ctx, app.ReactRequest{Agent: "bob", Message: string(message.ID), Reaction: "👍"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", Timeout: 15 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("default wait woke for reaction: %v", err)
	}
	got, err := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", From: "bob", Thread: string(message.ID), Kind: "ready", IncludeReactions: true, Timeout: time.Second})
	if err != nil || len(got.Items) != 0 || len(got.ReactionEvents) != 1 {
		t.Fatalf("opted in wait=%+v %v", got, err)
	}
	if _, err := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", IncludeReactions: true, ReactionsAfter: got.ReactionsAfter, Timeout: 15 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resumed wait replayed reaction: %v", err)
	}
}

func TestReactionRequestReplayAndRestartPreserveStream(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	join(t, sys.service, "alice", nil)
	join(t, sys.service, "bob", nil)
	message := reactionMessage(t, sys, "ready", app.Expiry{})
	req := app.ReactRequest{Mutation: app.Mutation{ClientID: "client", RequestID: "add-reaction"}, Agent: "bob", Message: string(message.ID), Reaction: "ack"}
	original, err := sys.service.React(ctx, req)
	if err != nil || !original.Changed {
		t.Fatalf("add=%+v %v", original, err)
	}
	if err := sys.service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sys.adapter.Close(); err != nil {
		t.Fatal(err)
	}
	adapter, err := Open(ctx, Options{Path: sys.path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	service := app.NewService(adapter, sys.clock)
	defer func() { _ = service.Close() }()
	peek, err := service.Peek(ctx, app.PeekRequest{Message: string(message.ID)})
	if err != nil || len(peek.Reactions) != 1 || peek.Reactions[0].Reaction != "ack" {
		t.Fatalf("after restart=%+v %v", peek, err)
	}
	replay, err := service.React(ctx, req)
	if err != nil || replay != original {
		t.Fatalf("replay=%+v original=%+v %v", replay, original, err)
	}
	if _, err := service.React(ctx, app.ReactRequest{Agent: "bob", Message: string(message.ID), Reaction: "ack", Remove: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.React(ctx, req); err != nil {
		t.Fatal(err)
	}
	peek, err = service.Peek(ctx, app.PeekRequest{Message: string(message.ID)})
	if err != nil || len(peek.Reactions) != 0 {
		t.Fatalf("replayed old add resurrected reaction: %+v %v", peek, err)
	}
	events, err := adapter.ReactionEvents(ctx, app.ReactionEventsRequest{Limit: 10}, sys.clock.Now())
	if err != nil || len(events.Items) != 2 || !events.Items[1].Removed {
		t.Fatalf("replayed events=%+v %v", events, err)
	}
}

func awaitReactionSubscribers(t *testing.T, service *app.Service, count int) {
	t.Helper()
	notifier := service.MessageEvents().(*app.Notifier)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for notifier.Subscribers() < count {
		select {
		case <-deadline.C:
			t.Fatalf("waiters did not subscribe: got %d want %d", notifier.Subscribers(), count)
		case <-ticker.C:
		}
	}
}

func TestReactionArrivalWakesOnlyOptedInWaitAndObserve(t *testing.T) {
	sys := newTestSystem(t)
	defer func() { _ = sys.service.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	join(t, sys.service, "alice", nil)
	join(t, sys.service, "bob", nil)
	message := reactionMessage(t, sys, "ready", app.Expiry{})
	type waitResult struct {
		result app.MessageWaitResponse
		err    error
	}
	ordinary := make(chan waitResult, 1)
	optedIn := make(chan waitResult, 1)
	go func() {
		result, err := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", Timeout: 250 * time.Millisecond})
		ordinary <- waitResult{result, err}
	}()
	go func() {
		result, err := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", IncludeReactions: true, Timeout: time.Second})
		optedIn <- waitResult{result, err}
	}()
	awaitReactionSubscribers(t, sys.service, 2)
	if _, err := sys.service.React(ctx, app.ReactRequest{Agent: "bob", Message: string(message.ID), Reaction: "ack"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-optedIn:
		if got.err != nil || len(got.result.Items) != 0 || len(got.result.ReactionEvents) != 1 {
			t.Fatalf("opt-in arrival=%+v %v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opt-in waiter missed reaction arrival")
	}
	select {
	case got := <-ordinary:
		t.Fatalf("ordinary waiter returned on reaction: %+v %v", got.result, got.err)
	default:
	}
	select {
	case got := <-ordinary:
		if !errors.Is(got.err, context.DeadlineExceeded) {
			t.Fatalf("ordinary waiter=%+v %v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary waiter did not time out")
	}
	head, err := sys.service.Observe(ctx, app.ObserveRequest{Head: true, IncludeReactions: true})
	if err != nil {
		t.Fatal(err)
	}
	type observeResult struct {
		result app.ObserveResponse
		err    error
	}
	observed := make(chan observeResult, 1)
	go func() {
		result, err := sys.service.Observe(ctx, app.ObserveRequest{Wait: true, IncludeReactions: true, After: head.After, ReactionsAfter: head.ReactionsAfter, Topic: string(message.TopicID), Timeout: time.Second})
		observed <- observeResult{result, err}
	}()
	awaitReactionSubscribers(t, sys.service, 1)
	if _, err := sys.service.React(ctx, app.ReactRequest{Agent: "bob", Message: string(message.ID), Reaction: "ack", Remove: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-observed:
		if got.err != nil || len(got.result.Items) != 0 || len(got.result.ReactionEvents) != 1 || !got.result.ReactionEvents[0].Removed {
			t.Fatalf("observed arrival=%+v %v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer missed reaction removal")
	}
}
