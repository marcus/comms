package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

func TestObserveHeadSkipsHistoryAndPreservesCommittedBoundary(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	empty, err := sys.service.Observe(ctx, app.ObserveRequest{Head: true})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.After == "" {
		t.Fatalf("empty head=%+v err=%v", empty, err)
	}
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "head-test")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)
	old := publish(t, sys, "alice", "head-test", "backlog")
	head, err := sys.service.Observe(ctx, app.ObserveRequest{Head: true, Topic: "head-test"})
	if err != nil || len(head.Items) != 0 || head.Items == nil || head.After == empty.After {
		t.Fatalf("head=%+v err=%v", head, err)
	}
	batch, err := sys.service.Observe(ctx, app.ObserveRequest{After: empty.After})
	if err != nil || len(batch.Items) != 1 || batch.Items[0].ID != old.ID {
		t.Fatalf("empty-store boundary=%+v err=%v", batch, err)
	}
	// A commit between capturing the head and starting a wait must be returned.
	next := publish(t, sys, "alice", "head-test", "new")
	batch, err = sys.service.Observe(ctx, app.ObserveRequest{Wait: true, After: head.After, Topic: "head-test", Timeout: time.Second})
	if err != nil || len(batch.Items) != 1 || batch.Items[0].ID != next.ID {
		t.Fatalf("after head=%+v err=%v", batch, err)
	}
	snapshot, err := sys.service.Snapshot(ctx)
	if err != nil || len(snapshot.Retrievals) != 0 {
		t.Fatalf("observation wrote retrievals: %+v %v", snapshot.Retrievals, err)
	}
	for _, sub := range snapshot.Subscriptions {
		if sub.ReadThroughSequence != 0 {
			t.Fatal("observation acknowledged a message")
		}
	}
	beforePurge, err := sys.service.Observe(ctx, app.ObserveRequest{Head: true})
	if err != nil {
		t.Fatal(err)
	}
	sys.clock.Advance(8 * 24 * time.Hour)
	if _, err = sys.service.Purge(ctx, app.PurgeRequest{}); err != nil {
		t.Fatal(err)
	}
	afterPurge, err := sys.service.Observe(ctx, app.ObserveRequest{Head: true})
	if err != nil || afterPurge.After != beforePurge.After || len(afterPurge.Items) != 0 {
		t.Fatalf("purged head=%+v err=%v", afterPurge, err)
	}
	later := publish(t, sys, "alice", "head-test", "after purge")
	batch, err = sys.service.Observe(ctx, app.ObserveRequest{After: afterPurge.After})
	if err != nil || len(batch.Items) != 1 || batch.Items[0].ID != later.ID {
		t.Fatalf("purged boundary=%+v err=%v", batch, err)
	}
}

func TestObserveHeadSupportsStoresWithoutOrdinalMetadata(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	author := join(t, sys.service, "legacy", nil)
	topic := createTopic(t, sys.service, "legacy")
	follow(t, sys.service, author, topic)
	publish(t, sys, "legacy", "legacy", "before metadata")
	expected, err := sys.service.Observe(ctx, app.ObserveRequest{Head: true})
	if err != nil {
		t.Fatal(err)
	}
	// Emulate a pre-v1.6 store: retained rows exist, but no ordinal metadata.
	_, err = sys.adapter.submit(ctx, func(conn *sql.Conn) (any, error) {
		return conn.ExecContext(ctx, "DELETE FROM store_meta WHERE key='message_ordinal'")
	})
	if err != nil {
		t.Fatal(err)
	}
	head, err := sys.service.Observe(ctx, app.ObserveRequest{Head: true})
	if err != nil || head.After != expected.After {
		t.Fatalf("legacy head=%+v err=%v", head, err)
	}
	next := publish(t, sys, "legacy", "legacy", "after metadata")
	batch, err := sys.service.Observe(ctx, app.ObserveRequest{After: head.After})
	if err != nil || len(batch.Items) != 1 || batch.Items[0].ID != next.ID {
		t.Fatalf("legacy continuation=%+v err=%v", batch, err)
	}
}

func TestObserveHeadRejectsConflictingModes(t *testing.T) {
	sys := newTestSystem(t)
	for _, req := range []app.ObserveRequest{{Head: true, Wait: true}, {Head: true, After: "anything"}, {Head: true, PageRequest: app.PageRequest{Cursor: "anything"}}} {
		if _, err := sys.service.Observe(context.Background(), req); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	if _, err := sys.service.Observe(context.Background(), app.ObserveRequest{Head: true, Topic: "missing"}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing topic: %v", err)
	}
}
