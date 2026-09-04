package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

func (a *Adapter) JoinAgent(ctx context.Context, req app.JoinRequest, fresh domain.Agent, now time.Time) (app.JoinResponse, error) {
	return withMutation(ctx, a, req.Mutation, "join", now, func(tx *sql.Tx) (app.JoinResponse, error) {
		if req.ExternalRef != nil {
			var id string
			e := tx.QueryRowContext(ctx, "SELECT agent_id FROM agent_external_refs WHERE namespace=? AND external_key=?", req.ExternalRef.Namespace, req.ExternalRef.Key).Scan(&id)
			if e == nil {
				existing, e := resolveAgent(ctx, tx, id, now)
				if e != nil {
					return app.JoinResponse{}, e
				}
				if existing.RetiredAt != nil {
					return app.JoinResponse{}, fmt.Errorf("%w: external reference belongs to retired agent", app.ErrConflict)
				}
				_, e = tx.ExecContext(ctx, "UPDATE agents SET last_seen_at=?,updated_at=?,display_name=CASE WHEN ?='' THEN display_name ELSE ? END,purpose=CASE WHEN ?='' THEN purpose ELSE ? END,harness=CASE WHEN ?='' THEN harness ELSE ? END,project=CASE WHEN ?='' THEN project ELSE ? END,session_ref=CASE WHEN ?='' THEN session_ref ELSE ? END WHERE id=?", micros(now), micros(now), req.DisplayName, req.DisplayName, req.Purpose, req.Purpose, req.Harness, req.Harness, req.Project, req.Project, req.SessionRef, req.SessionRef, id)
				if e != nil {
					return app.JoinResponse{}, e
				}
				existing, e = resolveAgent(ctx, tx, id, now)
				return app.JoinResponse{Agent: existing, Rejoined: true}, e
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return app.JoinResponse{}, e
			}
		}
		var collision int
		if e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM agent_aliases WHERE handle=? COLLATE NOCASE AND expires_at>?)", fresh.Handle, micros(now)).Scan(&collision); e != nil {
			return app.JoinResponse{}, e
		}
		if collision != 0 {
			return app.JoinResponse{}, fmt.Errorf("%w: handle %q is a live alias", app.ErrConflict, fresh.Handle)
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO agents("+agentCols+") VALUES(?,?,?,?,?,?,?,?,?,?,NULL)", fresh.ID, fresh.Handle, fresh.DisplayName, fresh.Purpose, fresh.Harness, fresh.Project, fresh.SessionRef, micros(now), micros(now), micros(now))
		if e != nil {
			return app.JoinResponse{}, e
		}
		if req.ExternalRef != nil {
			if _, e = tx.ExecContext(ctx, "INSERT INTO agent_external_refs(namespace,external_key,agent_id) VALUES(?,?,?)", req.ExternalRef.Namespace, req.ExternalRef.Key, fresh.ID); e != nil {
				return app.JoinResponse{}, e
			}
		}
		return app.JoinResponse{Agent: fresh}, nil
	})
}

func (a *Adapter) GetAgent(ctx context.Context, ref string, touch bool, now time.Time) (domain.Agent, error) {
	if strings.TrimSpace(ref) == "" {
		return domain.Agent{}, fmt.Errorf("%w: agent is required", domain.ErrInvalid)
	}
	if !touch {
		return resolveAgent(ctx, a.read, ref, now)
	}
	v, e := a.submit(ctx, func(conn *sql.Conn) (any, error) {
		tx, e := conn.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer func() { _ = tx.Rollback() }()
		ag, e := resolveAgent(ctx, tx, ref, now)
		if e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE agents SET last_seen_at=? WHERE id=?", micros(now), ag.ID); e != nil {
			return nil, e
		}
		ag.LastSeenAt = now
		if e = tx.Commit(); e != nil {
			return nil, e
		}
		return ag, nil
	})
	if e != nil {
		return domain.Agent{}, e
	}
	return v.(domain.Agent), nil
}

func (a *Adapter) UpdateAgent(ctx context.Context, req app.UpdateAgentRequest, now time.Time) (domain.Agent, error) {
	return withMutation(ctx, a, req.Mutation, "update_agent", now, func(tx *sql.Tx) (domain.Agent, error) {
		ag, e := resolveAgent(ctx, tx, req.Agent, now)
		if e != nil {
			return ag, e
		}
		if ag.RetiredAt != nil {
			return ag, fmt.Errorf("%w: agent is retired", app.ErrConflict)
		}
		if req.Handle != nil && !strings.EqualFold(*req.Handle, ag.Handle) {
			var collision int
			e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM agents WHERE handle=? COLLATE NOCASE AND id<>? UNION ALL SELECT 1 FROM agent_aliases WHERE handle=? COLLATE NOCASE AND expires_at>?)", *req.Handle, ag.ID, *req.Handle, micros(now)).Scan(&collision)
			if e != nil {
				return ag, e
			}
			if collision != 0 {
				return ag, fmt.Errorf("%w: handle %q is unavailable", app.ErrConflict, *req.Handle)
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO agent_aliases(handle,agent_id,expires_at) VALUES(?,?,?) ON CONFLICT(handle) DO UPDATE SET agent_id=excluded.agent_id,expires_at=excluded.expires_at", ag.Handle, ag.ID, micros(now.Add(domain.AliasLifetime))); e != nil {
				return ag, e
			}
			ag.Handle = *req.Handle
		}
		if req.DisplayName != nil {
			ag.DisplayName = *req.DisplayName
		}
		if req.Purpose != nil {
			ag.Purpose = *req.Purpose
		}
		if req.Harness != nil {
			ag.Harness = *req.Harness
		}
		if req.Project != nil {
			ag.Project = *req.Project
		}
		if req.SessionRef != nil {
			ag.SessionRef = *req.SessionRef
		}
		ag.UpdatedAt = now
		if e = ag.Validate(); e != nil {
			return ag, e
		}
		_, e = tx.ExecContext(ctx, "UPDATE agents SET handle=?,display_name=?,purpose=?,harness=?,project=?,session_ref=?,updated_at=? WHERE id=?", ag.Handle, ag.DisplayName, ag.Purpose, ag.Harness, ag.Project, ag.SessionRef, micros(now), ag.ID)
		return ag, e
	})
}

func (a *Adapter) RetireAgent(ctx context.Context, req app.RetireAgentRequest, now time.Time) (domain.Agent, error) {
	return withMutation(ctx, a, req.Mutation, "retire_agent", now, func(tx *sql.Tx) (domain.Agent, error) {
		ag, e := resolveAgent(ctx, tx, req.Agent, now)
		if e != nil {
			return ag, e
		}
		if ag.RetiredAt == nil {
			ag.RetiredAt = &now
			ag.UpdatedAt = now
			if _, e = tx.ExecContext(ctx, "UPDATE agents SET retired_at=?,updated_at=? WHERE id=?", micros(now), micros(now), ag.ID); e != nil {
				return ag, e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE topics SET archived_at=?,updated_at=? WHERE kind='direct' AND archived_at IS NULL AND id IN (SELECT topic_id FROM subscriptions WHERE agent_id=?)", micros(now), micros(now), ag.ID); e != nil {
				return ag, e
			}
		}
		return ag, nil
	})
}

func (a *Adapter) ListAgents(ctx context.Context, req app.AgentListRequest, now time.Time) (app.Page[domain.Agent], error) {
	p, e := decodeCursor(req.Cursor, 2)
	if e != nil {
		return app.Page[domain.Agent]{}, e
	}
	query := "SELECT " + agentCols + " FROM agents WHERE (? OR retired_at IS NULL) AND (?='' OR lower(handle)>lower(?) OR (lower(handle)=lower(?) AND id>?)) ORDER BY lower(handle),id LIMIT ?"
	rows, e := a.read.QueryContext(ctx, query, req.IncludeRetired, p[0], p[0], p[0], p[1], req.Limit+1)
	if e != nil {
		return app.Page[domain.Agent]{}, e
	}
	defer func() { _ = rows.Close() }()
	out := app.Page[domain.Agent]{Items: []domain.Agent{}}
	for rows.Next() {
		ag, e := scanAgent(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, ag)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > req.Limit {
		last := out.Items[req.Limit-1]
		out.Items = out.Items[:req.Limit]
		out.NextCursor = encodeCursor(strings.ToLower(last.Handle), string(last.ID))
	}
	return out, nil
}

func (a *Adapter) CreateTopic(ctx context.Context, req app.CreateTopicRequest, t domain.Topic, now time.Time) (domain.Topic, error) {
	return withMutation(ctx, a, req.Mutation, "create_topic", now, func(tx *sql.Tx) (domain.Topic, error) {
		_, e := tx.ExecContext(ctx, "INSERT INTO topics("+topicCols+") VALUES(?,?,?,?,?,?,?,NULL)", t.ID, t.Name, t.Kind, t.Description, t.NextSequence, micros(now), micros(now))
		return t, e
	})
}

func (a *Adapter) EnsureTopic(ctx context.Context, req app.EnsureTopicRequest, fresh domain.Topic, now time.Time) (app.EnsureTopicResponse, error) {
	return withMutation(ctx, a, req.Mutation, "ensure_topic", now, func(tx *sql.Tx) (app.EnsureTopicResponse, error) {
		var id string
		e := tx.QueryRowContext(ctx, "SELECT topic_id FROM topic_external_refs WHERE namespace=? AND external_key=?", req.ExternalRef.Namespace, req.ExternalRef.Key).Scan(&id)
		if e == nil {
			t, e := resolveTopic(ctx, tx, id)
			return app.EnsureTopicResponse{Topic: t}, e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return app.EnsureTopicResponse{}, e
		}
		base := fresh.Name
		for suffix := 1; ; suffix++ {
			var used int
			e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM topics WHERE name=? COLLATE NOCASE)", fresh.Name).Scan(&used)
			if e != nil {
				return app.EnsureTopicResponse{}, e
			}
			if used == 0 {
				break
			}
			tail := "-" + strconv.Itoa(suffix+1)
			max := domain.MaxTopicName - len(tail)
			if max < 1 {
				return app.EnsureTopicResponse{}, fmt.Errorf("%w: topic name collision cannot be suffixed", app.ErrConflict)
			}
			name := base
			if len(name) > max {
				name = name[:max]
			}
			fresh.Name = name + tail
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO topics("+topicCols+") VALUES(?,?,?,?,?,?,?,NULL)", fresh.ID, fresh.Name, fresh.Kind, fresh.Description, 1, micros(now), micros(now)); e != nil {
			return app.EnsureTopicResponse{}, e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO topic_external_refs(namespace,external_key,topic_id) VALUES(?,?,?)", req.ExternalRef.Namespace, req.ExternalRef.Key, fresh.ID); e != nil {
			return app.EnsureTopicResponse{}, e
		}
		return app.EnsureTopicResponse{Topic: fresh, Created: true}, nil
	})
}

func (a *Adapter) UpdateTopic(ctx context.Context, req app.UpdateTopicRequest, now time.Time) (domain.Topic, error) {
	return withMutation(ctx, a, req.Mutation, "update_topic", now, func(tx *sql.Tx) (domain.Topic, error) {
		t, e := resolveTopic(ctx, tx, req.Topic)
		if e != nil {
			return t, e
		}
		if req.Name != nil {
			t.Name = *req.Name
		}
		if req.Description != nil {
			t.Description = *req.Description
		}
		t.UpdatedAt = now
		if e = t.Validate(); e != nil {
			return t, e
		}
		_, e = tx.ExecContext(ctx, "UPDATE topics SET name=?,description=?,updated_at=? WHERE id=?", t.Name, t.Description, micros(now), t.ID)
		return t, e
	})
}
func (a *Adapter) ArchiveTopic(ctx context.Context, req app.ArchiveTopicRequest, now time.Time) (domain.Topic, error) {
	return withMutation(ctx, a, req.Mutation, "archive_topic", now, func(tx *sql.Tx) (domain.Topic, error) {
		t, e := resolveTopic(ctx, tx, req.Topic)
		if e != nil {
			return t, e
		}
		if t.ArchivedAt == nil {
			t.ArchivedAt = &now
			t.UpdatedAt = now
			_, e = tx.ExecContext(ctx, "UPDATE topics SET archived_at=?,updated_at=? WHERE id=?", micros(now), micros(now), t.ID)
		}
		return t, e
	})
}

func (a *Adapter) ListTopics(ctx context.Context, req app.TopicListRequest, agent string, now time.Time) (app.Page[domain.Topic], error) {
	p, e := decodeCursor(req.Cursor, 2)
	if e != nil {
		return app.Page[domain.Topic]{}, e
	}
	query := "SELECT " + topicCols + " FROM topics t WHERE (? OR archived_at IS NULL) AND (kind='public' OR (? AND EXISTS(SELECT 1 FROM subscriptions s JOIN agents a ON a.id=s.agent_id WHERE s.topic_id=t.id AND a.id=?))) AND (?='' OR lower(name)>lower(?) OR (lower(name)=lower(?) AND id>?)) ORDER BY lower(name),id LIMIT ?"
	rows, e := a.read.QueryContext(ctx, query, req.IncludeArchived, req.IncludeDirect, agent, p[0], p[0], p[0], p[1], req.Limit+1)
	if e != nil {
		return app.Page[domain.Topic]{}, e
	}
	defer func() { _ = rows.Close() }()
	out := app.Page[domain.Topic]{Items: []domain.Topic{}}
	for rows.Next() {
		t, e := scanTopic(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, t)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > req.Limit {
		last := out.Items[req.Limit-1]
		out.Items = out.Items[:req.Limit]
		out.NextCursor = encodeCursor(strings.ToLower(last.Name), string(last.ID))
	}
	return out, nil
}

func scanSubscription(s interface{ Scan(...any) error }) (domain.Subscription, error) {
	var sub domain.Subscription
	var followed, updated int64
	var unfollowed, readAt sql.NullInt64
	e := s.Scan(&sub.AgentID, &sub.TopicID, &followed, &unfollowed, &sub.ReadThroughSequence, &readAt, &updated)
	sub.FollowedAt = timeFrom(followed)
	sub.UnfollowedAt = nullableTime(unfollowed)
	sub.ReadThroughAt = nullableTime(readAt)
	sub.UpdatedAt = timeFrom(updated)
	return sub, e
}

const subscriptionCols = "agent_id,topic_id,followed_at,unfollowed_at,read_through_sequence,read_through_at,updated_at"

func (a *Adapter) Follow(ctx context.Context, req app.FollowRequest, now time.Time) (domain.Subscription, error) {
	return withMutation(ctx, a, req.Mutation, "follow", now, func(tx *sql.Tx) (domain.Subscription, error) {
		ag, e := resolveAgent(ctx, tx, req.Agent, now)
		if e != nil {
			return domain.Subscription{}, e
		}
		t, e := resolveTopic(ctx, tx, req.Topic)
		if e != nil {
			return domain.Subscription{}, e
		}
		if ag.RetiredAt != nil || t.ArchivedAt != nil {
			return domain.Subscription{}, fmt.Errorf("%w: retired agent or archived topic", app.ErrConflict)
		}
		if t.Kind == domain.TopicDirect {
			return domain.Subscription{}, fmt.Errorf("%w: direct subscriptions are managed atomically", app.ErrConflict)
		}
		var existing domain.Subscription
		existing, e = scanSubscription(tx.QueryRowContext(ctx, "SELECT "+subscriptionCols+" FROM subscriptions WHERE agent_id=? AND topic_id=?", ag.ID, t.ID))
		if e == nil {
			if existing.UnfollowedAt != nil {
				_, e = tx.ExecContext(ctx, "UPDATE subscriptions SET followed_at=?,unfollowed_at=NULL,updated_at=? WHERE agent_id=? AND topic_id=?", micros(now), micros(now), ag.ID, t.ID)
				existing.FollowedAt = now
				existing.UnfollowedAt = nil
				existing.UpdatedAt = now
			}
			return existing, e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return domain.Subscription{}, e
		}
		var first sql.NullInt64
		if e = tx.QueryRowContext(ctx, "SELECT MIN(sequence) FROM messages WHERE topic_id=? AND (expires_at IS NULL OR expires_at>?)", t.ID, micros(now)).Scan(&first); e != nil {
			return domain.Subscription{}, e
		}
		cursor := t.NextSequence - 1
		if first.Valid {
			cursor = first.Int64 - 1
		}
		sub := domain.Subscription{AgentID: ag.ID, TopicID: t.ID, FollowedAt: now, ReadThroughSequence: cursor, UpdatedAt: now}
		_, e = tx.ExecContext(ctx, "INSERT INTO subscriptions("+subscriptionCols+") VALUES(?,?,?,NULL,?,NULL,?)", sub.AgentID, sub.TopicID, micros(now), cursor, micros(now))
		return sub, e
	})
}
func (a *Adapter) Unfollow(ctx context.Context, req app.UnfollowRequest, now time.Time) (domain.Subscription, error) {
	return withMutation(ctx, a, req.Mutation, "unfollow", now, func(tx *sql.Tx) (domain.Subscription, error) {
		ag, e := resolveAgent(ctx, tx, req.Agent, now)
		if e != nil {
			return domain.Subscription{}, e
		}
		t, e := resolveTopic(ctx, tx, req.Topic)
		if e != nil {
			return domain.Subscription{}, e
		}
		if t.Kind == domain.TopicDirect {
			return domain.Subscription{}, fmt.Errorf("%w: direct topics cannot be unfollowed", app.ErrConflict)
		}
		sub, e := scanSubscription(tx.QueryRowContext(ctx, "SELECT "+subscriptionCols+" FROM subscriptions WHERE agent_id=? AND topic_id=?", ag.ID, t.ID))
		if errors.Is(e, sql.ErrNoRows) {
			return sub, fmt.Errorf("%w: subscription", app.ErrNotFound)
		}
		if e != nil {
			return sub, e
		}
		if sub.UnfollowedAt == nil {
			sub.UnfollowedAt = &now
			sub.UpdatedAt = now
			_, e = tx.ExecContext(ctx, "UPDATE subscriptions SET unfollowed_at=?,updated_at=? WHERE agent_id=? AND topic_id=?", micros(now), micros(now), ag.ID, t.ID)
		}
		return sub, e
	})
}

func (a *Adapter) ListSubscriptions(ctx context.Context, req app.SubscriptionListRequest, now time.Time) (app.Page[domain.Subscription], error) {
	ag, e := resolveAgent(ctx, a.read, req.Agent, now)
	if e != nil {
		return app.Page[domain.Subscription]{}, e
	}
	p, e := decodeCursor(req.Cursor, 1)
	if e != nil {
		return app.Page[domain.Subscription]{}, e
	}
	rows, e := a.read.QueryContext(ctx, "SELECT "+subscriptionCols+" FROM subscriptions WHERE agent_id=? AND (? OR unfollowed_at IS NULL) AND (?='' OR topic_id>?) ORDER BY topic_id LIMIT ?", ag.ID, req.IncludeUnfollowed, p[0], p[0], req.Limit+1)
	if e != nil {
		return app.Page[domain.Subscription]{}, e
	}
	defer func() { _ = rows.Close() }()
	out := app.Page[domain.Subscription]{Items: []domain.Subscription{}}
	for rows.Next() {
		sub, e := scanSubscription(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, sub)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > req.Limit {
		last := out.Items[req.Limit-1]
		out.Items = out.Items[:req.Limit]
		out.NextCursor = encodeCursor(string(last.TopicID))
	}
	return out, nil
}
