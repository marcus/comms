package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

func TestGroupConversationRoutingVisibilityAndIdentity(t *testing.T) {
	sys := newTestSystem(t)
	s := sys.service
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	a := join(t, s, "alice", nil)
	b := join(t, s, "bob", nil)
	c := join(t, s, "cara", nil)
	join(t, s, "outsider", nil)
	req := app.DirectSendRequest{Mutation: app.Mutation{ClientID: "client", RequestID: "first-group"}, Author: "alice", Recipients: []string{"@bob", string(c.ID)}, Title: "private coordination", Body: "group body"}
	first, err := s.DirectSend(ctx, req)
	if err != nil || first.Topic.Kind != domain.TopicGroup {
		t.Fatalf("first=%+v %v", first, err)
	}
	again, err := s.DirectSend(ctx, req)
	if err != nil || again.ID != first.ID {
		t.Fatalf("idempotent=%+v %v", again, err)
	}
	reordered, err := s.DirectSend(ctx, app.DirectSendRequest{Author: "cara", Recipients: []string{"bob", "alice"}, Title: "same set", Body: "second"})
	if err != nil || reordered.TopicID != first.TopicID || reordered.Sequence != 2 {
		t.Fatalf("reordered=%+v %v", reordered, err)
	}
	direct, err := s.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipient: "bob", Title: "pair", Body: "unchanged"})
	if err != nil || direct.Topic.Kind != domain.TopicDirect || direct.TopicID == first.TopicID {
		t.Fatalf("direct=%+v %v", direct, err)
	}
	details, err := s.Topic(ctx, string(first.TopicID))
	if err != nil || len(details.Members) != 3 {
		t.Fatalf("members=%+v %v", details, err)
	}
	for _, who := range []string{"alice", "bob", "cara", "outsider"} {
		box, err := s.Inbox(ctx, app.MessageListRequest{Agent: who, Full: true})
		if err != nil {
			t.Fatal(err)
		}
		var hasFirst bool
		for _, m := range box.Items {
			if m.ID == first.ID {
				hasFirst = true
			}
		}
		if hasFirst != (who == "bob" || who == "cara") {
			t.Fatalf("%s inbox=%+v", who, box)
		}
		topics, err := s.Topics(ctx, who, app.TopicListRequest{IncludeDirect: true})
		if err != nil {
			t.Fatal(err)
		}
		var hasGroup bool
		for _, topic := range topics.Items {
			if topic.ID == first.TopicID {
				hasGroup = true
			}
		}
		if hasGroup != (who != "outsider") {
			t.Fatalf("%s topics=%+v", who, topics)
		}
	}
	public, err := s.Topics(ctx, "bob", app.TopicListRequest{})
	if err != nil || len(public.Items) != 0 {
		t.Fatalf("default discovery=%+v %v", public, err)
	}
	for name, operation := range map[string]func() error{
		"follow": func() error {
			_, e := s.Follow(ctx, app.FollowRequest{Agent: "outsider", Topic: string(first.TopicID)})
			return e
		},
		"unfollow": func() error {
			_, e := s.Unfollow(ctx, app.UnfollowRequest{Agent: "bob", Topic: string(first.TopicID)})
			return e
		},
		"publish": func() error {
			_, e := s.Publish(ctx, app.PublishRequest{Author: "outsider", Topic: string(first.TopicID), Title: "intrude", Body: "no"})
			return e
		},
		"reply": func() error {
			_, e := s.Reply(ctx, app.ReplyRequest{Author: "outsider", Parent: string(first.ID), Body: "no"})
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, app.ErrConflict) {
				t.Fatal(err)
			}
		})
	}
	// Deliberate inspection is operator-visible even when an identity is supplied.
	if _, err = s.Peek(ctx, app.PeekRequest{Agent: "outsider", Message: string(first.ID)}); err != nil {
		t.Fatal(err)
	}
	found, err := s.Search(ctx, app.SearchRequest{Agent: "outsider", Query: "coordination"})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("operator search=%+v %v", found, err)
	}
	if _, err = s.ReadThrough(ctx, app.ReadThroughRequest{Agent: "bob", Message: string(first.ID)}); err != nil {
		t.Fatal(err)
	}
	receipts, err := s.Receipts(ctx, string(first.ID))
	if err != nil || len(receipts.Subscribers) != 2 {
		t.Fatalf("receipts=%+v %v", receipts, err)
	}
	newHandle := "bob-renamed"
	if _, err = s.UpdateAgent(ctx, app.UpdateAgentRequest{Agent: "bob", Handle: &newHandle}); err != nil {
		t.Fatal(err)
	}
	renamed, err := s.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipients: []string{newHandle, "cara"}, Title: "renamed", Body: "same IDs"})
	if err != nil || renamed.TopicID != first.TopicID {
		t.Fatalf("rename=%+v %v", renamed, err)
	}
	if _, err = s.RetireAgent(ctx, app.RetireAgentRequest{Agent: string(b.ID)}); err != nil {
		t.Fatal(err)
	}
	closed, err := s.Topic(ctx, string(first.TopicID))
	if err != nil || closed.ArchivedAt == nil || len(closed.Members) != 3 {
		t.Fatalf("closed=%+v %v", closed, err)
	}
	if _, err = s.Reply(ctx, app.ReplyRequest{Author: "alice", Parent: string(first.ID), Body: "closed"}); !errors.Is(err, app.ErrConflict) {
		t.Fatal(err)
	}
	replacement := join(t, s, newHandle, nil)
	newGroup, err := s.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipients: []string{newHandle, "cara"}, Title: "fresh", Body: "new identity"})
	if err != nil || newGroup.TopicID == first.TopicID || replacement.ID == b.ID {
		t.Fatalf("reuse=%+v %v", newGroup, err)
	}
	old, err := s.Peek(ctx, app.PeekRequest{Message: string(first.ID)})
	if err != nil || old.AuthorID != a.ID {
		t.Fatal(old, err)
	}
}

func TestGroupRecipientValidationIsAtomic(t *testing.T) {
	sys := newTestSystem(t)
	s := sys.service
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	join(t, s, "alice", nil)
	b := join(t, s, "bob", nil)
	join(t, s, "retired", nil)
	if _, err := s.RetireAgent(ctx, app.RetireAgentRequest{Agent: "retired"}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		recipient  string
		recipients []string
		want       error
	}{
		{"both", "bob", []string{"bob"}, domain.ErrInvalid},
		{"neither", "", nil, domain.ErrInvalid},
		{"empty", "", []string{}, domain.ErrInvalid},
		{"blank", "", []string{""}, domain.ErrInvalid},
		{"self", "", []string{"alice", "bob"}, domain.ErrInvalid},
		{"duplicate-identity", "", []string{"bob", string(b.ID)}, domain.ErrInvalid},
		{"missing", "", []string{"bob", "absent"}, app.ErrNotFound},
		{"retired", "", []string{"bob", "retired"}, app.ErrConflict},
		{"too-many", "", make([]string, app.MaxConversationParticipants), domain.ErrInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := s.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipient: test.recipient, Recipients: test.recipients, Title: "invalid", Body: "no partial state"})
			if !errors.Is(err, test.want) {
				t.Fatal(err)
			}
		})
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil || len(snapshot.Topics) != 0 || len(snapshot.Messages) != 0 || len(snapshot.Subscriptions) != 0 {
		t.Fatalf("partial write=%+v %v", snapshot, err)
	}
	if _, err = s.EnsureTopic(ctx, app.EnsureTopicRequest{Name: "reserved", ExternalRef: app.ExternalRef{Namespace: "group", Key: "test"}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestGroupObserveHeadWaitAndContinuation(t *testing.T) {
	sys := newTestSystem(t)
	s := sys.service
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	join(t, s, "alice", nil)
	b := join(t, s, "bob", nil)
	c := join(t, s, "cara", nil)
	head, err := s.Observe(ctx, app.ObserveRequest{Head: true})
	if err != nil || len(head.Items) != 0 {
		t.Fatal(head, err)
	}
	type result struct {
		batch app.ObserveResponse
		err   error
	}
	done := make(chan result, 1)
	go func() {
		batch, e := s.Observe(ctx, app.ObserveRequest{Wait: true, After: head.After, Timeout: time.Second})
		done <- result{batch, e}
	}()
	notifier := s.MessageEvents().(*app.Notifier)
	deadline := time.Now().Add(time.Second)
	for notifier.Subscribers() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("observer not registered")
		}
		time.Sleep(time.Millisecond)
	}
	message, err := s.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipients: []string{"bob", "cara"}, Title: "wake group", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || len(got.batch.Items) != 1 || got.batch.Items[0].ID != message.ID {
		t.Fatal(got)
	}
	ids := got.batch.Items[0].RecipientIDs
	slices.Sort(ids)
	expect := []domain.AgentID{b.ID, c.ID}
	slices.Sort(expect)
	if !slices.Equal(ids, expect) {
		t.Fatalf("recipients=%v expected=%v", ids, expect)
	}
	if _, err = s.Publish(ctx, app.PublishRequest{Author: "bob", Topic: string(message.TopicID), Title: "next", Body: "group publish"}); err != nil {
		t.Fatal(err)
	}
	next, err := s.Observe(ctx, app.ObserveRequest{After: got.batch.After})
	if err != nil || len(next.Items) != 1 || next.Items[0].Title != "next" {
		t.Fatal(next, err)
	}
	wait, err := s.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "cara", Timeout: time.Second})
	if err != nil || len(wait.Items) != 2 {
		t.Fatal(wait, err)
	}
	box, err := s.Inbox(ctx, app.MessageListRequest{Agent: "cara", UnreadOnly: true})
	if err != nil || len(box.Items) != 2 {
		t.Fatal("observation acknowledged messages", box, err)
	}
}

func TestGroupMigrationPreservesReferencesAndRollsBack(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "rollback"}[broken], func(t *testing.T) {
			path := schemaThreeFixture(t)
			db, err := sql.Open("sqlite", sqliteDSN(path, false))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			migrations, err := embeddedMigrations()
			if err != nil {
				t.Fatal(err)
			}
			if err = applyMigration(context.Background(), db, migrations[3], 0); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("INSERT INTO topic_external_refs VALUES('custom','original','top_aaaaaaaaaaaaaaaaaaaaaaaaaa'); INSERT INTO subscription_read_advances VALUES('agt_aaaaaaaaaaaaaaaaaaaaaaaaaa','top_aaaaaaaaaaaaaaaaaaaaaaaaaa',1,2)"); err != nil {
				t.Fatal(err)
			}
			if broken {
				if _, err = db.Exec("PRAGMA foreign_keys=OFF; UPDATE messages SET topic_id='missing'; PRAGMA foreign_keys=ON"); err != nil {
					t.Fatal(err)
				}
			}
			err = applyMigration(context.Background(), db, migrations[4], 0)
			if (err != nil) != broken {
				t.Fatalf("migration=%v broken=%v", err, broken)
			}
			var version, enabled, refs int
			if err = db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
				t.Fatal(err)
			}
			want := 5
			if broken {
				want = 4
			}
			if version != want {
				t.Fatal(version)
			}
			if err = db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
				t.Fatal(enabled, err)
			}
			if err = db.QueryRow("SELECT count(*) FROM topics t JOIN subscriptions s ON s.topic_id=t.id JOIN subscription_read_advances r ON r.topic_id=t.id JOIN topic_external_refs e ON e.topic_id=t.id").Scan(&refs); err != nil || refs != 1 {
				t.Fatal(refs, err)
			}
		})
	}
}
