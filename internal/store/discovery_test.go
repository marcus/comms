package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marcus/comms/internal/app"
)

func TestAgentDiscoveryFiltersAndPagination(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	a := join(t, sys.service, "alpha", nil)
	b := join(t, sys.service, "beta", nil)
	project, purpose := "Comms", "Parser owner"
	if _, err := sys.service.UpdateAgent(ctx, app.UpdateAgentRequest{Agent: string(b.ID), Project: &project, Purpose: &purpose}); err != nil {
		t.Fatal(err)
	}
	sys.clock.Advance(time.Second)
	if _, err := sys.service.GetAgent(ctx, string(a.ID), true); err != nil {
		t.Fatal(err)
	}
	page, err := sys.service.Agents(ctx, app.AgentListRequest{PageRequest: app.PageRequest{Limit: 1}})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != a.ID || page.NextCursor == "" {
		t.Fatalf("first page %+v %v", page, err)
	}
	next, err := sys.service.Agents(ctx, app.AgentListRequest{PageRequest: app.PageRequest{Limit: 1, Cursor: page.NextCursor}})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != b.ID || next.NextCursor != "" {
		t.Fatalf("next page %+v %v", next, err)
	}
	filtered, err := sys.service.Agents(ctx, app.AgentListRequest{Search: "PARSER", Project: "comms"})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != b.ID {
		t.Fatalf("filtered %+v %v", filtered, err)
	}
	literal, err := sys.service.Agents(ctx, app.AgentListRequest{Search: "%"})
	if err != nil || len(literal.Items) != 0 {
		t.Fatalf("literal %+v %v", literal, err)
	}
	if _, err = sys.service.RetireAgent(ctx, app.RetireAgentRequest{Agent: string(b.ID)}); err != nil {
		t.Fatal(err)
	}
	filtered, err = sys.service.Agents(ctx, app.AgentListRequest{Search: "beta"})
	if err != nil || len(filtered.Items) != 0 {
		t.Fatalf("retired %+v %v", filtered, err)
	}
}

func TestRecipientSuggestionPreservesNotFound(t *testing.T) {
	sys := newTestSystem(t)
	sender := join(t, sys.service, "sender", nil)
	join(t, sys.service, "ui-u2b", nil)
	_, err := sys.service.DirectSend(context.Background(), app.DirectSendRequest{Author: string(sender.ID), Recipient: "ui-u2c", Title: "hello", Body: "hello"})
	if !errors.Is(err, app.ErrNotFound) || !strings.Contains(err.Error(), "@ui-u2b") {
		t.Fatalf("suggestion: %v", err)
	}
}

func TestAgentActivityConcurrentWaitsAndRestart(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	agent := join(t, sys.service, "watcher", nil)
	before, err := sys.service.GetAgent(ctx, string(agent.ID), false)
	if err != nil || before.Activity == nil || before.Activity.LastWaitAt != nil {
		t.Fatalf("before %+v %v", before, err)
	}
	if _, err = sys.service.Inbox(ctx, app.MessageListRequest{Agent: string(agent.ID)}); err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := sys.service.WaitForMessages(waiting, app.MessageWaitRequest{Agent: string(agent.ID), Timeout: time.Second})
			done <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for {
		current, err := sys.service.GetAgent(ctx, string(agent.ID), false)
		if err != nil {
			t.Fatal(err)
		}
		if current.Activity.OpenWaits == 2 {
			if current.Activity.LastInboxAt == nil || current.Activity.LastWaitAt == nil {
				t.Fatal("missing timestamps")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waits did not open")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	for range 2 {
		<-done
	}
	after, err := sys.service.GetAgent(ctx, string(agent.ID), false)
	if err != nil || after.Activity.OpenWaits != 0 {
		t.Fatalf("after %+v %v", after, err)
	}
	restarted := app.NewService(sys.adapter, sys.clock)
	fresh, err := restarted.GetAgent(ctx, string(agent.ID), false)
	if err != nil || fresh.Activity.LastWaitAt != nil || fresh.Activity.LastInboxAt != nil || fresh.Activity.OpenWaits != 0 {
		t.Fatalf("restart %+v %v", fresh, err)
	}
	_, err = sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: string(agent.ID), Timeout: time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout %v", err)
	}
	timedOut, err := sys.service.GetAgent(ctx, string(agent.ID), false)
	if err != nil || timedOut.Activity.OpenWaits != 0 {
		t.Fatalf("timeout cleanup %+v %v", timedOut, err)
	}
	sender := join(t, sys.service, "sender", nil)
	directSend(t, sys, string(sender.ID), string(agent.ID), "hello")
	if _, err = sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: string(agent.ID), Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	completed, err := sys.service.GetAgent(ctx, string(agent.ID), false)
	if err != nil || completed.Activity.OpenWaits != 0 {
		t.Fatalf("completion cleanup %+v %v", completed, err)
	}
	// Polling observations must not change the durable last-seen timestamp.
	if !after.LastSeenAt.Equal(before.LastSeenAt) {
		t.Fatal("poll changed last seen")
	}
}
