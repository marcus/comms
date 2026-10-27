package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

func TestObserveForwardRoutingAndContinuation(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	carol := join(t, sys.service, "carol", nil)
	topic := createTopic(t, sys.service, "bridge")
	for _, agent := range []domain.Agent{alice, bob, carol} {
		follow(t, sys.service, agent, topic)
	}
	public, err := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "bridge", Title: "public", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := sys.service.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipient: "bob", Title: "direct", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	// Both commits have the same clock stamp. Commit order must still page safely.
	req := app.ObserveRequest{Wait: true, PageRequest: app.PageRequest{Limit: 1}}
	first, err := sys.service.Observe(ctx, req)
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != public.ID || len(first.Items[0].RecipientIDs) != 2 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	req.After = first.After
	second, err := sys.service.Observe(ctx, req)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != direct.ID || len(second.Items[0].RecipientIDs) != 1 || second.Items[0].RecipientIDs[0] != bob.ID {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	after := second.After
	empty, err := sys.service.Observe(ctx, app.ObserveRequest{After: after})
	if err != nil || len(empty.Items) != 0 || empty.After != after {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	// Read-time routing excludes unfollowed and retired subscribers.
	if _, err = sys.service.Unfollow(ctx, app.UnfollowRequest{Agent: "carol", Topic: "bridge"}); err != nil {
		t.Fatal(err)
	}
	history, err := sys.service.Observe(ctx, app.ObserveRequest{Wait: true, Topic: "bridge"})
	if err != nil || len(history.Items) != 1 || len(history.Items[0].RecipientIDs) != 1 {
		t.Fatalf("routing=%+v err=%v", history, err)
	}
	if _, err = sys.service.RetireAgent(ctx, app.RetireAgentRequest{Agent: "bob"}); err != nil {
		t.Fatal(err)
	}
	history, err = sys.service.Observe(ctx, app.ObserveRequest{Wait: true, Topic: "bridge"})
	if err != nil || len(history.Items[0].RecipientIDs) != 0 {
		t.Fatalf("retired routing=%+v err=%v", history, err)
	}
	// Regress the wall clock and purge all retained messages. The persistent
	// insertion high-water mark must still place the next commit after the token.
	sys.clock.Advance(8 * 24 * time.Hour)
	if _, err = sys.service.Purge(ctx, app.PurgeRequest{}); err != nil {
		t.Fatal(err)
	}
	sys.clock.Advance(-9 * 24 * time.Hour)
	newest, err := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "bridge", Title: "new", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	history, err = sys.service.Observe(ctx, app.ObserveRequest{After: after})
	if err != nil || len(history.Items) != 1 || history.Items[0].ID != newest.ID {
		t.Fatalf("after purge=%+v err=%v", history, err)
	}
}

func TestObserveWaitWakesAndReleases(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "bridge")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)
	notifier := sys.service.MessageEvents().(*app.Notifier)
	done := make(chan error, 1)
	go func() {
		result, err := sys.service.Observe(ctx, app.ObserveRequest{Wait: true, Timeout: time.Second})
		if err == nil && len(result.Items) != 1 {
			err = errors.New("missing wakeup result")
		}
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for notifier.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if notifier.Subscribers() != 1 {
		t.Fatal("wait never subscribed")
	}
	_, err := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "bridge", Title: "wake", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if notifier.Subscribers() != 0 {
		t.Fatal("leaked successful waiter")
	}
	subs, err := sys.service.Subscriptions(ctx, app.SubscriptionListRequest{Agent: "bob"})
	if err != nil || subs.Items[0].ReadThroughSequence != 0 {
		t.Fatalf("read cursor changed: %+v %v", subs, err)
	}
	observed, err := sys.service.Observe(ctx, app.ObserveRequest{Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, cancelled := range []bool{false, true} {
		waitCtx, cancel := context.WithCancel(ctx)
		if cancelled {
			cancel()
		}
		_, err = sys.service.Observe(waitCtx, app.ObserveRequest{Wait: true, After: observed.After, Timeout: 5 * time.Millisecond})
		cancel()
		want := context.DeadlineExceeded
		if cancelled {
			want = context.Canceled
		}
		if !errors.Is(err, want) {
			t.Fatalf("cancelled=%v err=%v", cancelled, err)
		}
		if notifier.Subscribers() != 0 {
			t.Fatal("leaked ended waiter")
		}
	}
	for _, req := range []app.ObserveRequest{{Wait: true, PageRequest: app.PageRequest{Cursor: "bad"}}, {After: "bad"}, {Wait: true, Timeout: -time.Second}, {Wait: true, Timeout: 2 * time.Hour}} {
		if _, err := sys.service.Observe(ctx, req); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("req=%+v err=%v", req, err)
		}
	}
}
