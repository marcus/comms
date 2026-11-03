package app

import (
	"context"
	"errors"
	"testing"

	"github.com/marcus/comms/internal/domain"
)

func TestReactionValidationBeforeStore(t *testing.T) {
	service := NewServiceWithStores(nil, nil, nil, nil, nil)
	id, _ := domain.NewMessageID()
	for _, req := range []ReactRequest{
		{Message: string(id), Reaction: "👍"},
		{Agent: "@alice", Reaction: "👍"},
		{Agent: "@alice", Message: "malformed", Reaction: "👍"},
		{Agent: "@alice", Message: string(id), Reaction: " "},
		{Mutation: Mutation{ClientID: "partial"}, Agent: "@alice", Message: string(id), Reaction: "👍"},
	} {
		if _, err := service.React(context.Background(), req); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("req=%+v err=%v", req, err)
		}
	}
}
