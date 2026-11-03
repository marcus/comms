package app

import (
	"context"

	"github.com/marcus/comms/internal/domain"
)

// ReactRequest annotates a message without sending a reply or advancing receipts.
type ReactRequest struct {
	Mutation
	Agent    string `json:"agent"`
	Message  string `json:"message"`
	Reaction string `json:"reaction"`
	Remove   bool   `json:"remove,omitempty"`
}

type ReactResponse struct {
	MessageID domain.MessageID `json:"message_id"`
	Reaction  domain.Reaction  `json:"reaction"`
	Removed   bool             `json:"removed"`
	Changed   bool             `json:"changed"`
}

// InboxResponse preserves the message page and adds an opt-in event stream.
type InboxResponse struct {
	Page[domain.Message]
	ReactionEvents []domain.ReactionEvent `json:"reaction_events,omitempty"`
	ReactionsAfter string                 `json:"reactions_after,omitempty"`
}

// ReactionEventsRequest uses an independent forward cursor. AgentID limits
// delivery to the original message author; an empty AgentID is operator view.
type ReactionEventsRequest struct {
	After        string
	Limit        int
	AgentID      domain.AgentID
	FromID       domain.AgentID
	ThreadRootID domain.MessageID
	Topic        string
	Kind         string
	IncludeSelf  bool
	Head         bool
}

type ReactionEventsResponse struct {
	Items []domain.ReactionEvent
	After string
}

func (s *Service) React(ctx context.Context, req ReactRequest) (ReactResponse, error) {
	if err := req.Validate(); err != nil {
		return ReactResponse{}, err
	}
	if req.Agent == "" || req.Message == "" {
		return ReactResponse{}, requiredErr("agent and message")
	}
	if _, err := domain.ParseMessageID(req.Message); err != nil {
		return ReactResponse{}, err
	}
	if err := domain.ValidateReaction(req.Reaction); err != nil {
		return ReactResponse{}, err
	}
	result, err := s.messageStore.React(ctx, req, s.clock.Now())
	if err == nil && result.Changed {
		s.messageEvents.Notify()
	}
	return result, err
}

func (s *Service) observeBatch(ctx context.Context, req ObserveRequest) (ObserveResponse, error) {
	result, err := s.messageStore.Observe(ctx, req, s.clock.Now())
	if err != nil || !req.IncludeReactions {
		return result, err
	}
	events, err := s.messageStore.ReactionEvents(ctx, ReactionEventsRequest{After: req.ReactionsAfter, Limit: req.Limit, Topic: req.Topic, Head: req.Head}, s.clock.Now())
	if err != nil {
		return ObserveResponse{}, err
	}
	result.ReactionEvents, result.ReactionsAfter = events.Items, events.After
	return result, nil
}
