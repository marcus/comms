package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

func TestReadThroughAllBoundsAndMonotonicity(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	for _, name := range []string{"one", "two"} {
		topic := createTopic(t, sys.service, name)
		follow(t, sys.service, alice, topic)
		follow(t, sys.service, bob, topic)
		publish(t, sys, "alice", name, "old")
	}
	sys.clock.Advance(time.Second)
	boundary := publish(t, sys, "alice", "one", "boundary")
	cursor := encodeCursor(strconv.FormatInt(micros(boundary.CreatedAt), 10), string(boundary.ID))
	sys.clock.Advance(time.Second)
	later := publish(t, sys, "alice", "two", "later")
	result, e := sys.service.ReadThroughAll(ctx, app.ReadThroughAllRequest{Agent: "bob", All: true, Before: cursor})
	if e != nil || result.NewlyAcknowledged != 3 || len(result.Subscriptions) != 2 {
		t.Fatalf("ack=%#v err=%v", result, e)
	}
	box, e := sys.service.Inbox(ctx, app.MessageListRequest{Agent: "bob", UnreadOnly: true})
	if e != nil || len(box.Items) != 1 || box.Items[0].ID != later.ID {
		t.Fatalf("box=%#v err=%v", box, e)
	}
	again, e := sys.service.ReadThroughAll(ctx, app.ReadThroughAllRequest{Agent: "bob", All: true, Before: cursor})
	if e != nil || again.NewlyAcknowledged != 0 {
		t.Fatalf("repeat=%#v err=%v", again, e)
	}
	result, e = sys.service.ReadThroughAll(ctx, app.ReadThroughAllRequest{Agent: "bob", All: true})
	if e != nil || result.NewlyAcknowledged != 1 {
		t.Fatalf("all=%#v err=%v", result, e)
	}
	again, e = sys.service.ReadThroughAll(ctx, app.ReadThroughAllRequest{Agent: "bob", All: true, Before: cursor})
	if e != nil || again.NewlyAcknowledged != 0 {
		t.Fatalf("older=%#v err=%v", again, e)
	}
	for _, sub := range again.Subscriptions {
		if sub.NewSequence != sub.PreviousSequence {
			t.Fatal("cursor moved backward")
		}
	}
	if _, e = sys.service.ReadThroughAll(ctx, app.ReadThroughAllRequest{Agent: "bob", All: true, Before: "bad"}); !errors.Is(e, domain.ErrInvalid) {
		t.Fatalf("invalid cursor: %v", e)
	}
}

func TestPublishRefusalNamesFollowRecovery(t *testing.T) {
	sys := newTestSystem(t)
	alice := join(t, sys.service, "alice", nil)
	topic := createTopic(t, sys.service, "build")
	_, e := sys.service.Publish(context.Background(), app.PublishRequest{Author: "alice", Topic: "build", Title: "test", Body: "test"})
	if !errors.Is(e, app.ErrConflict) || !strings.Contains(e.Error(), "comms topic follow "+string(topic.ID)) || !strings.Contains(e.Error(), `"build"`) {
		t.Fatalf("refusal=%v", e)
	}
	subs, e := sys.service.Subscriptions(context.Background(), app.SubscriptionListRequest{Agent: string(alice.ID)})
	if e != nil || len(subs.Items) != 0 {
		t.Fatalf("implicit follow=%#v %v", subs, e)
	}
}

func TestReadThroughAllExpiryAndFormerSubscriptions(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	active := createTopic(t, sys.service, "active")
	former := createTopic(t, sys.service, "former")
	for _, topic := range []domain.Topic{active, former} {
		follow(t, sys.service, alice, topic)
		follow(t, sys.service, bob, topic)
	}
	_, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "active", Title: "expired", Body: "expired", Expiry: app.Expiry{After: time.Second}})
	if e != nil {
		t.Fatal(e)
	}
	publish(t, sys, "alice", "former", "unfollowed")
	if _, e = sys.service.Unfollow(ctx, app.UnfollowRequest{Agent: "bob", Topic: "former"}); e != nil {
		t.Fatal(e)
	}
	sys.clock.Advance(2 * time.Second)
	result, e := sys.service.ReadThroughAll(ctx, app.ReadThroughAllRequest{Agent: "bob", All: true})
	if e != nil || result.NewlyAcknowledged != 0 || len(result.Subscriptions) != 1 || result.Subscriptions[0].NewSequence != 1 {
		t.Fatalf("expired ack=%#v %v", result, e)
	}
	subs, e := sys.service.Subscriptions(ctx, app.SubscriptionListRequest{Agent: "bob", IncludeUnfollowed: true})
	if e != nil {
		t.Fatal(e)
	}
	for _, sub := range subs.Items {
		if sub.TopicID == former.ID && sub.ReadThroughSequence != 0 {
			t.Fatalf("former advanced=%#v", sub)
		}
	}
}
