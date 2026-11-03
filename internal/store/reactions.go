package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

func (a *Adapter) React(ctx context.Context, req app.ReactRequest, now time.Time) (_ app.ReactResponse, storageErr error) {
	defer func() { storageErr = translate(storageErr) }()
	return withMutation(ctx, a, req.Mutation, "react", now, func(tx *sql.Tx) (app.ReactResponse, error) {
		var out app.ReactResponse
		agent, err := resolveAgent(ctx, tx, req.Agent, now)
		if err != nil {
			return out, err
		}
		if agent.RetiredAt != nil {
			return out, fmt.Errorf("%w: agent is retired", app.ErrConflict)
		}
		message, err := resolveMessage(ctx, tx, req.Message)
		if err != nil {
			return out, err
		}
		if message.ExpiresAt != nil && !message.ExpiresAt.After(now) {
			return out, fmt.Errorf("%w: message %q", app.ErrNotFound, req.Message)
		}
		out.MessageID = message.ID
		out.Removed = req.Remove
		out.Reaction = domain.Reaction{Reaction: req.Reaction, AgentID: agent.ID, CreatedAt: timeFrom(micros(now))}
		var created int64
		err = tx.QueryRowContext(ctx, "SELECT created_at FROM message_reactions WHERE message_id=? AND agent_id=? AND reaction=?", message.ID, agent.ID, req.Reaction).Scan(&created)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if exists {
			out.Reaction.CreatedAt = timeFrom(created)
		}
		if exists == !req.Remove {
			return out, nil
		}
		if req.Remove {
			_, err = tx.ExecContext(ctx, "DELETE FROM message_reactions WHERE message_id=? AND agent_id=? AND reaction=?", message.ID, agent.ID, req.Reaction)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO message_reactions(message_id,agent_id,reaction,created_at) VALUES(?,?,?,?)", message.ID, agent.ID, req.Reaction, micros(now))
		}
		if err != nil {
			return out, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO reaction_events(message_id,agent_id,reaction,reaction_created_at,removed,created_at) VALUES(?,?,?,?,?,?)", message.ID, agent.ID, req.Reaction, micros(out.Reaction.CreatedAt), req.Remove, micros(now))
		out.Changed = err == nil
		return out, err
	})
}

func reactionCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	parts, err := decodeCursor(cursor, 2)
	if err != nil {
		return 0, err
	}
	ordinal, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || parts[0] != "reaction" || ordinal < 0 {
		return 0, fmt.Errorf("%w: invalid reaction cursor", domain.ErrInvalid)
	}
	return ordinal, nil
}

const reactionEventCols = "e.id,e.message_id,m.topic_id,m.thread_root_id,m.author_id,e.agent_id,e.reaction,e.reaction_created_at,e.removed,e.created_at"

func scanReactionEvent(s interface{ Scan(...any) error }) (domain.ReactionEvent, error) {
	var event domain.ReactionEvent
	var reactionAt, eventAt int64
	err := s.Scan(&event.ID, &event.MessageID, &event.TopicID, &event.ThreadRootID, &event.AuthorID, &event.Reaction.AgentID, &event.Reaction.Reaction, &reactionAt, &event.Removed, &eventAt)
	event.Type = "reaction"
	event.Reaction.CreatedAt = timeFrom(reactionAt)
	event.CreatedAt = timeFrom(eventAt)
	return event, err
}

func (a *Adapter) ReactionEvents(ctx context.Context, req app.ReactionEventsRequest, now time.Time) (_ app.ReactionEventsResponse, storageErr error) {
	defer func() { storageErr = translate(storageErr) }()
	ordinal, err := reactionCursor(req.After)
	if err != nil {
		return app.ReactionEventsResponse{}, err
	}
	out := app.ReactionEventsResponse{Items: []domain.ReactionEvent{}, After: encodeCursor("reaction", strconv.FormatInt(ordinal, 10))}
	if req.Head {
		err := a.read.QueryRowContext(ctx, "SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='reaction_events'),0)").Scan(&ordinal)
		out.After = encodeCursor("reaction", strconv.FormatInt(ordinal, 10))
		return out, err
	}
	var topicID domain.TopicID
	if req.Topic != "" {
		topic, err := resolveTopic(ctx, a.read, req.Topic)
		if err != nil {
			return out, err
		}
		topicID = topic.ID
	}
	query := "SELECT " + reactionEventCols + " FROM reaction_events e JOIN messages m ON m.id=e.message_id WHERE e.id>? AND (m.expires_at IS NULL OR m.expires_at>?) AND (?='' OR m.author_id=?) AND (?='' OR e.agent_id=?) AND (?='' OR m.thread_root_id=?) AND (?='' OR m.topic_id=?) AND (?='' OR ?=1 OR e.agent_id<>?) AND " + kindPredicate("m") + " ORDER BY e.id LIMIT ?"
	rows, err := a.read.QueryContext(ctx, query, ordinal, micros(now), req.AgentID, req.AgentID, req.FromID, req.FromID, req.ThreadRootID, req.ThreadRootID, topicID, topicID, req.AgentID, req.IncludeSelf, req.AgentID, req.Kind, req.Kind, req.Limit)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		event, err := scanReactionEvent(rows)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, event)
		out.After = encodeCursor("reaction", strconv.FormatInt(event.ID, 10))
	}
	return out, rows.Err()
}

func hideExpiredReactions(message *domain.Message, now time.Time) {
	if message.ExpiresAt != nil && !message.ExpiresAt.After(now) {
		message.Reactions = []domain.Reaction{}
	}
}
