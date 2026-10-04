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

func TestMessageKindsFilterBeforePaginationAndWait(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "coord")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)
	var messages []domain.Message
	for _, kind := range []string{"ready", "status", "blocked", "", "ready", "status"} {
		sys.clock.Advance(time.Second)
		m, err := sys.service.Publish(ctx, app.PublishRequest{Author: "bob", Topic: "coord", Title: kind + " title", Body: "body", Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	req := app.MessageListRequest{Agent: "alice", Kind: "ready,blocked", PageRequest: app.PageRequest{Limit: 1}}
	var got []domain.MessageID
	for {
		page, err := sys.service.Inbox(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Items {
			got = append(got, m.ID)
		}
		if page.NextCursor == "" {
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(got) != 3 || got[0] != messages[4].ID || got[1] != messages[2].ID || got[2] != messages[0].ID {
		t.Fatalf("paged kinds=%v", got)
	}
	waitReq := app.MessageWaitRequest{Agent: "alice", Kind: "ready,blocked", From: "bob", Limit: 1, Timeout: time.Second}
	for _, index := range []int{0, 2, 4} {
		result, err := sys.service.WaitForMessages(ctx, waitReq)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 1 || result.Items[0].ID != messages[index].ID {
			t.Fatalf("wait=%#v", result)
		}
		waitReq.After = result.After
	}
	waitReq.Timeout = 10 * time.Millisecond
	if _, err := sys.service.WaitForMessages(ctx, waitReq); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unmatched chatter woke wait: %v", err)
	}
	// A kind-filtered thread summary must expose the matching reply even when its root is status chatter.
	reply, err := sys.service.Reply(ctx, app.ReplyRequest{Author: "bob", Parent: string(messages[1].ID), Body: "review", Kind: "verdict"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := sys.service.Inbox(ctx, app.MessageListRequest{Agent: "alice", Kind: "verdict", ThreadsOnly: true})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != reply.ID || reply.Title != "" {
		t.Fatalf("thread summary=%#v, %v", page, err)
	}
	direct, err := sys.service.DirectSend(ctx, app.DirectSendRequest{Author: "bob", Recipient: "alice", Title: "custom", Body: "body", Kind: "custom.result"})
	if err != nil || direct.Kind != "custom.result" {
		t.Fatalf("direct=%#v %v", direct, err)
	}
	for _, bad := range []string{"READY", "ready,blocked", " ready", "-ready"} {
		if _, err := sys.service.Publish(ctx, app.PublishRequest{Author: "bob", Topic: "coord", Title: "bad", Body: "body", Kind: bad}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("kind %q: %v", bad, err)
		}
	}
	for _, bad := range []string{"ready,", "ready,,blocked", "READY"} {
		if _, err := sys.service.Inbox(ctx, app.MessageListRequest{Agent: "alice", Kind: bad}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("filter %q: %v", bad, err)
		}
		if _, err := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", Kind: bad}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("wait filter %q: %v", bad, err)
		}
	}
}

func TestMessageKindMigrationPreservesLegacyRows(t *testing.T) {
	path := legacyDatabase(t)
	db, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO topics(id,name,name_key,kind,next_sequence,created_at,updated_at) VALUES('top_aaaaaaaaaaaaaaaaaaaaaaaaaa','legacy','legacy','public',2,0,0)")
	if err == nil {
		_, err = db.Exec("INSERT INTO messages(id,topic_id,sequence,author_id,author_context_json,title,body,thread_root_id,created_at) VALUES('msg_aaaaaaaaaaaaaaaaaaaaaaaaaa','top_aaaaaaaaaaaaaaaaaaaaaaaaaa',1,'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa','{}','old','body','msg_aaaaaaaaaaaaaaaaaaaaaaaaaa',0)")
	}
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := Open(context.Background(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	m, err := resolveMessage(context.Background(), adapter.read, "msg_aaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || m.Kind != "" || m.Title != "old" {
		t.Fatalf("legacy message=%#v %v", m, err)
	}
}

func TestWaitForKindIgnoresNewChatterUntilMatch(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "coord")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)
	result := make(chan app.MessageWaitResponse, 1)
	failure := make(chan error, 1)
	go func() {
		v, err := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", Kind: "ready", Timeout: time.Second})
		if err != nil {
			failure <- err
			return
		}
		result <- v
	}()
	_, err := sys.service.Publish(ctx, app.PublishRequest{Author: "bob", Topic: "coord", Title: "progress", Body: "body", Kind: "status"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-result:
		t.Fatalf("chatter woke wait: %#v", v)
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(20 * time.Millisecond):
	}
	ready, err := sys.service.Publish(ctx, app.PublishRequest{Author: "bob", Topic: "coord", Title: "ready", Body: "body", Kind: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-result:
		if len(v.Items) != 1 || v.Items[0].ID != ready.ID || v.Filter.Kind != "ready" {
			t.Fatalf("wait=%#v", v)
		}
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("matching arrival did not wake wait")
	}
}
