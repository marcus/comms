package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marcus/comms/internal/app"
)

func TestRetiredHandleReusePreservesIdentityAndRouting(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	old := join(t, sys.service, "scout", nil)
	bob := join(t, sys.service, "bob", nil)
	msg, err := sys.service.DirectSend(ctx, app.DirectSendRequest{Author: "scout", Recipient: "bob", Title: "old", Body: "old identity"})
	if err != nil {
		t.Fatal(err)
	}
	newName := "renamed-scout"
	if _, err = sys.service.UpdateAgent(ctx, app.UpdateAgentRequest{Agent: string(old.ID), Handle: &newName}); err != nil {
		t.Fatal(err)
	}
	if _, err = sys.service.RetireAgent(ctx, app.RetireAgentRequest{Agent: string(old.ID)}); err != nil {
		t.Fatal(err)
	}
	// Retirement frees both the current name and temporary rename aliases.
	replacement := join(t, sys.service, "RENAMED-SCOUT", nil)
	aliasReplacement := join(t, sys.service, "scout", nil)
	if replacement.ID == old.ID || aliasReplacement.ID == old.ID {
		t.Fatal("retirement resurrected old identity")
	}
	found, err := sys.service.GetAgent(ctx, "@renamed-scout", false)
	if err != nil || found.ID != replacement.ID {
		t.Fatalf("active lookup=%+v %v", found, err)
	}
	retired, err := sys.service.GetAgent(ctx, string(old.ID), false)
	if err != nil || retired.RetiredAt == nil || retired.Handle != "renamed-scout" {
		t.Fatalf("retired=%+v %v", retired, err)
	}
	historical, err := sys.service.Peek(ctx, app.PeekRequest{Message: string(msg.ID)})
	if err != nil || historical.AuthorID != old.ID || historical.AuthorHandle != "renamed-scout" {
		t.Fatalf("history=%+v %v", historical, err)
	}
	receipts, err := sys.service.Receipts(ctx, string(msg.ID))
	if err != nil || len(receipts.Subscribers) != 1 || receipts.Subscribers[0].Agent.ID != bob.ID {
		t.Fatalf("receipts=%+v %v", receipts, err)
	}
	inbox, err := sys.service.Inbox(ctx, app.MessageListRequest{Agent: string(replacement.ID), Full: true})
	if err != nil || len(inbox.Items) != 0 {
		t.Fatalf("new identity inherited inbox: %+v %v", inbox, err)
	}
	if _, err = sys.service.Join(ctx, app.JoinRequest{Handle: "renamed-scout"}); !errors.Is(err, app.ErrConflict) || !strings.Contains(err.Error(), "active agent") {
		t.Fatalf("active collision=%v", err)
	}
	if _, err = sys.service.RetireAgent(ctx, app.RetireAgentRequest{Agent: string(replacement.ID)}); err != nil {
		t.Fatal(err)
	}
	if _, err = sys.service.GetAgent(ctx, "renamed-scout", false); !errors.Is(err, app.ErrConflict) || !strings.Contains(err.Error(), "stable agent ID") {
		t.Fatalf("ambiguous retired lookup=%v", err)
	}
	third := join(t, sys.service, "renamed-scout", nil)
	if third.ID == old.ID || third.ID == replacement.ID {
		t.Fatal("reused retired stable ID")
	}
}
