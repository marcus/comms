package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

const messageCols = "id,topic_id,sequence,author_id,author_context_json,title,body,in_reply_to,thread_root_id,created_at,expires_at,metadata_json"

func scanMessage(s interface{ Scan(...any) error }) (domain.Message, error) {
	var m domain.Message
	var authorJSON string
	var parent, metadata sql.NullString
	var created int64
	var expires sql.NullInt64
	e := s.Scan(&m.ID, &m.TopicID, &m.Sequence, &m.AuthorID, &authorJSON, &m.Title, &m.Body, &parent, &m.ThreadRootID, &created, &expires, &metadata)
	if e != nil {
		return m, e
	}
	if e = json.Unmarshal([]byte(authorJSON), &m.AuthorContext); e != nil {
		return m, e
	}
	if parent.Valid {
		v := domain.MessageID(parent.String)
		m.InReplyTo = &v
	}
	if metadata.Valid {
		m.Metadata = json.RawMessage(metadata.String)
	}
	m.CreatedAt = timeFrom(created)
	m.ExpiresAt = nullableTime(expires)
	return m, nil
}
func resolveMessage(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ref string) (domain.Message, error) {
	m, e := scanMessage(q.QueryRowContext(ctx, "SELECT "+messageCols+" FROM messages WHERE id=?", ref))
	if errors.Is(e, sql.ErrNoRows) {
		return m, fmt.Errorf("%w: message %q", app.ErrNotFound, ref)
	}
	return m, e
}

func (a *Adapter) Publish(ctx context.Context, p app.PreparedMessage) (domain.Message, error) {
	return withMutation(ctx, a, p.Mutation, "publish", p.Now, func(tx *sql.Tx) (domain.Message, error) {
		author, e := resolveAgent(ctx, tx, p.Author, p.Now)
		if e != nil {
			return domain.Message{}, e
		}
		topic, e := resolveTopic(ctx, tx, p.Topic)
		if e != nil {
			return domain.Message{}, e
		}
		if author.RetiredAt != nil || topic.ArchivedAt != nil {
			return domain.Message{}, fmt.Errorf("%w: author retired or topic archived", app.ErrConflict)
		}
		var active int
		if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM subscriptions WHERE agent_id=? AND topic_id=? AND unfollowed_at IS NULL)", author.ID, topic.ID).Scan(&active); e != nil {
			return domain.Message{}, e
		}
		if active == 0 {
			return domain.Message{}, fmt.Errorf("%w: author does not follow topic", app.ErrConflict)
		}
		return insertMessage(ctx, tx, p, author, topic, nil)
	})
}

func (a *Adapter) DirectSend(ctx context.Context, p app.PreparedMessage) (domain.Message, error) {
	return withMutation(ctx, a, p.Mutation, "direct_send", p.Now, func(tx *sql.Tx) (domain.Message, error) {
		author, e := resolveAgent(ctx, tx, p.Author, p.Now)
		if e != nil {
			return domain.Message{}, e
		}
		recipient, e := resolveAgent(ctx, tx, p.Recipient, p.Now)
		if e != nil {
			return domain.Message{}, e
		}
		if author.ID == recipient.ID {
			return domain.Message{}, fmt.Errorf("%w: direct recipient must be another agent", domain.ErrInvalid)
		}
		if author.RetiredAt != nil || recipient.RetiredAt != nil {
			return domain.Message{}, fmt.Errorf("%w: direct participant is retired", app.ErrConflict)
		}
		ref := domain.DirectExternalRef(author.ID, recipient.ID)
		var topicID string
		e = tx.QueryRowContext(ctx, "SELECT topic_id FROM topic_external_refs WHERE namespace=? AND external_key=?", ref.Namespace, ref.Key).Scan(&topicID)
		var topic domain.Topic
		switch {
		case errors.Is(e, sql.ErrNoRows):
			id, e := domain.NewTopicID()
			if e != nil {
				return domain.Message{}, e
			}
			name := "direct:" + ref.Key
			topic = domain.Topic{ID: id, Name: name, Kind: domain.TopicDirect, NextSequence: 1, CreatedAt: p.Now, UpdatedAt: p.Now}
			if _, e = tx.ExecContext(ctx, "INSERT INTO topics("+topicCols+") VALUES(?,?,?,?,1,?,?,NULL)", topic.ID, topic.Name, topic.Kind, "", micros(p.Now), micros(p.Now)); e != nil {
				return domain.Message{}, e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO topic_external_refs(namespace,external_key,topic_id) VALUES(?,?,?)", ref.Namespace, ref.Key, topic.ID); e != nil {
				return domain.Message{}, e
			}
			for _, id := range []domain.AgentID{author.ID, recipient.ID} {
				if _, e = tx.ExecContext(ctx, "INSERT INTO subscriptions("+subscriptionCols+") VALUES(?,?,?,NULL,0,NULL,?)", id, topic.ID, micros(p.Now), micros(p.Now)); e != nil {
					return domain.Message{}, e
				}
			}
		case e != nil:
			return domain.Message{}, e
		default:
			topic, e = resolveTopic(ctx, tx, topicID)
			if e != nil {
				return domain.Message{}, e
			}
			if topic.ArchivedAt != nil {
				return domain.Message{}, fmt.Errorf("%w: direct topic is archived", app.ErrConflict)
			}
			var members int
			if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM subscriptions WHERE topic_id=? AND unfollowed_at IS NULL AND agent_id IN (?,?)", topic.ID, author.ID, recipient.ID).Scan(&members); e != nil {
				return domain.Message{}, e
			}
			if members != 2 {
				return domain.Message{}, fmt.Errorf("%w: direct topic membership is inconsistent", app.ErrConflict)
			}
		}
		return insertMessage(ctx, tx, p, author, topic, nil)
	})
}

func (a *Adapter) Reply(ctx context.Context, p app.PreparedMessage) (domain.Message, error) {
	return withMutation(ctx, a, p.Mutation, "reply", p.Now, func(tx *sql.Tx) (domain.Message, error) {
		author, e := resolveAgent(ctx, tx, p.Author, p.Now)
		if e != nil {
			return domain.Message{}, e
		}
		parent, e := resolveMessage(ctx, tx, p.Parent)
		if e != nil {
			return domain.Message{}, e
		}
		topic, e := resolveTopic(ctx, tx, string(parent.TopicID))
		if e != nil {
			return domain.Message{}, e
		}
		if author.RetiredAt != nil || topic.ArchivedAt != nil {
			return domain.Message{}, fmt.Errorf("%w: author retired or topic archived", app.ErrConflict)
		}
		var active int
		if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM subscriptions WHERE agent_id=? AND topic_id=? AND unfollowed_at IS NULL)", author.ID, topic.ID).Scan(&active); e != nil {
			return domain.Message{}, e
		}
		if active == 0 {
			return domain.Message{}, fmt.Errorf("%w: author does not follow topic", app.ErrConflict)
		}
		return insertMessage(ctx, tx, p, author, topic, &parent)
	})
}

func insertMessage(ctx context.Context, tx *sql.Tx, p app.PreparedMessage, author domain.Agent, topic domain.Topic, parent *domain.Message) (domain.Message, error) {
	var sequence int64
	if e := tx.QueryRowContext(ctx, "UPDATE topics SET next_sequence=next_sequence+1,updated_at=? WHERE id=? RETURNING next_sequence-1", micros(p.Now), topic.ID).Scan(&sequence); e != nil {
		return domain.Message{}, e
	}
	ctxJSON, e := json.Marshal(domain.AuthorContext{Harness: author.Harness, Project: author.Project, SessionRef: author.SessionRef})
	if e != nil {
		return domain.Message{}, e
	}
	m := domain.Message{ID: p.ID, TopicID: topic.ID, Sequence: sequence, AuthorID: author.ID, AuthorContext: domain.AuthorContext{Harness: author.Harness, Project: author.Project, SessionRef: author.SessionRef}, Title: p.Title, Body: p.Body, ThreadRootID: p.ID, CreatedAt: p.Now, ExpiresAt: p.ExpiresAt, Metadata: p.Metadata}
	var parentID any
	if parent != nil {
		v := parent.ID
		m.InReplyTo = &v
		m.ThreadRootID = parent.ThreadRootID
		parentID = parent.ID
	}
	if e = m.Validate(parent != nil); e != nil {
		return domain.Message{}, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO messages("+messageCols+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", m.ID, m.TopicID, m.Sequence, m.AuthorID, string(ctxJSON), m.Title, m.Body, parentID, m.ThreadRootID, micros(m.CreatedAt), nullableMicros(m.ExpiresAt), rawJSON(m.Metadata)); e != nil {
		return domain.Message{}, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO messages_fts(message_id,title,body) VALUES(?,?,?)", m.ID, m.Title, m.Body); e != nil {
		return domain.Message{}, e
	}
	return m, nil
}

func pageMessages(ctx context.Context, db *sql.DB, query string, args []any, limit int, descending bool) (app.Page[domain.Message], error) {
	rows, e := db.QueryContext(ctx, query, args...)
	if e != nil {
		return app.Page[domain.Message]{}, e
	}
	defer func() { _ = rows.Close() }()
	out := app.Page[domain.Message]{Items: []domain.Message{}}
	for rows.Next() {
		m, e := scanMessage(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, m)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > limit {
		last := out.Items[limit-1]
		out.Items = out.Items[:limit]
		if descending {
			out.NextCursor = encodeCursor(strconv.FormatInt(micros(last.CreatedAt), 10), string(last.ID))
		} else {
			out.NextCursor = encodeCursor(strconv.FormatInt(last.Sequence, 10), string(last.ID))
		}
	}
	return out, nil
}
func descendingCursor(v string) (int64, string, error) {
	p, e := decodeCursor(v, 2)
	if e != nil {
		return 0, "", e
	}
	if p[0] == "" {
		return 0, "", nil
	}
	n, e := strconv.ParseInt(p[0], 10, 64)
	if e != nil {
		return 0, "", fmt.Errorf("%w: malformed cursor", domain.ErrInvalid)
	}
	return n, p[1], nil
}
func ascendingCursor(v string) (int64, string, error) { return descendingCursor(v) }

func (a *Adapter) Inbox(ctx context.Context, req app.MessageListRequest, now time.Time) (app.Page[domain.Message], error) {
	ag, e := resolveAgent(ctx, a.read, req.Agent, now)
	if e != nil {
		return app.Page[domain.Message]{}, e
	}
	stamp, id, e := descendingCursor(req.Cursor)
	if e != nil {
		return app.Page[domain.Message]{}, e
	}
	query := "SELECT " + prefixedMessageCols("m") + " FROM messages m JOIN subscriptions s ON s.topic_id=m.topic_id AND s.agent_id=? AND s.unfollowed_at IS NULL WHERE (m.expires_at IS NULL OR m.expires_at>?) AND (?=0 OR m.sequence>s.read_through_sequence) AND (?=0 OR m.in_reply_to IS NULL) AND (?=0 OR m.created_at<? OR (m.created_at=? AND m.id<?)) ORDER BY m.created_at DESC,m.id DESC LIMIT ?"
	return pageMessages(ctx, a.read, query, []any{ag.ID, micros(now), req.UnreadOnly, req.ThreadsOnly, stamp, stamp, stamp, id, req.Limit + 1}, req.Limit, true)
}
func (a *Adapter) TopicMessages(ctx context.Context, req app.MessageListRequest, now time.Time) (app.Page[domain.Message], error) {
	t, e := resolveTopic(ctx, a.read, req.Topic)
	if e != nil {
		return app.Page[domain.Message]{}, e
	}
	seq, id, e := ascendingCursor(req.Cursor)
	if e != nil {
		return app.Page[domain.Message]{}, e
	}
	query := "SELECT " + messageCols + " FROM messages WHERE topic_id=? AND (expires_at IS NULL OR expires_at>?) AND (?=0 OR sequence>? OR (sequence=? AND id>?)) ORDER BY sequence,id LIMIT ?"
	return pageMessages(ctx, a.read, query, []any{t.ID, micros(now), seq, seq, seq, id, req.Limit + 1}, req.Limit, false)
}
func (a *Adapter) Thread(ctx context.Context, req app.ThreadRequest, now time.Time) (app.Page[domain.Message], error) {
	m, e := resolveMessage(ctx, a.read, req.Message)
	if e != nil {
		return app.Page[domain.Message]{}, e
	}
	seq, id, e := ascendingCursor(req.Cursor)
	if e != nil {
		return app.Page[domain.Message]{}, e
	}
	query := "SELECT " + messageCols + " FROM messages WHERE thread_root_id=? AND EXISTS(SELECT 1 FROM messages live WHERE live.thread_root_id=? AND (live.expires_at IS NULL OR live.expires_at>?)) AND (?=0 OR sequence>? OR (sequence=? AND id>?)) ORDER BY sequence,id LIMIT ?"
	return pageMessages(ctx, a.read, query, []any{m.ThreadRootID, m.ThreadRootID, micros(now), seq, seq, seq, id, req.Limit + 1}, req.Limit, false)
}
func (a *Adapter) Peek(ctx context.Context, ref string, now time.Time) (domain.Message, error) {
	m, e := resolveMessage(ctx, a.read, ref)
	if e != nil {
		return m, e
	}
	if m.ExpiresAt != nil && !m.ExpiresAt.After(now) {
		var live int
		e = a.read.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE thread_root_id=? AND (expires_at IS NULL OR expires_at>?))", m.ThreadRootID, micros(now)).Scan(&live)
		if e != nil {
			return m, e
		}
		if live == 0 {
			return domain.Message{}, fmt.Errorf("%w: message %q", app.ErrNotFound, ref)
		}
	}
	return m, nil
}

func (a *Adapter) ReadThrough(ctx context.Context, req app.ReadThroughRequest, now time.Time) (app.ReadThroughResponse, error) {
	return withMutation(ctx, a, req.Mutation, "read_through", now, func(tx *sql.Tx) (app.ReadThroughResponse, error) {
		ag, e := resolveAgent(ctx, tx, req.Agent, now)
		if e != nil {
			return app.ReadThroughResponse{}, e
		}
		m, e := resolveMessage(ctx, tx, req.Message)
		if e != nil {
			return app.ReadThroughResponse{}, e
		}
		sub, e := scanSubscription(tx.QueryRowContext(ctx, "SELECT "+subscriptionCols+" FROM subscriptions WHERE agent_id=? AND topic_id=? AND unfollowed_at IS NULL", ag.ID, m.TopicID))
		if errors.Is(e, sql.ErrNoRows) {
			return app.ReadThroughResponse{}, fmt.Errorf("%w: active subscription", app.ErrNotFound)
		}
		if e != nil {
			return app.ReadThroughResponse{}, e
		}
		out := app.ReadThroughResponse{Subscription: sub, PreviousSequence: sub.ReadThroughSequence, NewSequence: sub.ReadThroughSequence}
		if m.Sequence <= sub.ReadThroughSequence {
			return out, nil
		}
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM messages WHERE topic_id=? AND sequence>? AND sequence<=? AND (expires_at IS NULL OR expires_at>?)", m.TopicID, sub.ReadThroughSequence, m.Sequence, micros(now)).Scan(&out.NewlyAcknowledged); e != nil {
			return out, e
		}
		sub.ReadThroughSequence = m.Sequence
		sub.ReadThroughAt = &now
		sub.UpdatedAt = now
		out.Subscription = sub
		out.NewSequence = m.Sequence
		if _, e = tx.ExecContext(ctx, "UPDATE subscriptions SET read_through_sequence=?,read_through_at=?,updated_at=? WHERE agent_id=? AND topic_id=?", m.Sequence, micros(now), micros(now), ag.ID, m.TopicID); e != nil {
			return out, e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO subscription_read_advances(agent_id,topic_id,through_sequence,read_at) VALUES(?,?,?,?)", ag.ID, m.TopicID, m.Sequence, micros(now)); e != nil {
			return out, e
		}
		return out, nil
	})
}

func (a *Adapter) Receipts(ctx context.Context, ref string, now time.Time) ([]app.Receipt, error) {
	m, e := resolveMessage(ctx, a.read, ref)
	if e != nil {
		return nil, e
	}
	rows, e := a.read.QueryContext(ctx, "SELECT "+prefixedAgentCols("a")+",s.read_through_sequence,(SELECT MIN(r.read_at) FROM subscription_read_advances r WHERE r.agent_id=s.agent_id AND r.topic_id=s.topic_id AND r.through_sequence>=?) FROM subscriptions s JOIN agents a ON a.id=s.agent_id WHERE s.topic_id=? AND s.agent_id<>? AND (s.unfollowed_at IS NULL OR s.read_through_sequence>=?) ORDER BY lower(a.handle),a.id", m.Sequence, m.TopicID, m.AuthorID, m.Sequence)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	out := []app.Receipt{}
	for rows.Next() {
		ag := scanAgentWithTrailing(rows)
		if ag.err != nil {
			return nil, ag.err
		}
		state := "unread"
		var at *time.Time
		if ag.read.Valid && ag.sequence >= m.Sequence {
			state = "read"
			at = nullableTime(ag.read)
		}
		out = append(out, app.Receipt{Agent: ag.agent, State: state, ReadAt: at})
	}
	return out, rows.Err()
}

type agentTrailing struct {
	agent    domain.Agent
	sequence int64
	read     sql.NullInt64
	err      error
}

func scanAgentWithTrailing(s interface{ Scan(...any) error }) agentTrailing {
	var x agentTrailing
	var created, updated, seen int64
	var retired sql.NullInt64
	x.err = s.Scan(&x.agent.ID, &x.agent.Handle, &x.agent.DisplayName, &x.agent.Purpose, &x.agent.Harness, &x.agent.Project, &x.agent.SessionRef, &created, &updated, &seen, &retired, &x.sequence, &x.read)
	x.agent.CreatedAt = timeFrom(created)
	x.agent.UpdatedAt = timeFrom(updated)
	x.agent.LastSeenAt = timeFrom(seen)
	x.agent.RetiredAt = nullableTime(retired)
	return x
}

func (a *Adapter) Search(ctx context.Context, req app.SearchRequest, now time.Time) (app.Page[domain.Message], error) {
	stamp, id, e := descendingCursor(req.Cursor)
	if e != nil {
		return app.Page[domain.Message]{}, e
	}
	var fromID, topicID string
	if req.From != "" {
		ag, e := resolveAgent(ctx, a.read, req.From, now)
		if e != nil {
			return app.Page[domain.Message]{}, e
		}
		fromID = string(ag.ID)
	}
	if req.Topic != "" {
		t, e := resolveTopic(ctx, a.read, req.Topic)
		if e != nil {
			return app.Page[domain.Message]{}, e
		}
		topicID = string(t.ID)
	}
	query := "SELECT " + prefixedMessageCols("m") + " FROM messages_fts f JOIN messages m ON m.id=f.message_id WHERE messages_fts MATCH ? AND (m.expires_at IS NULL OR m.expires_at>?) AND (?='' OR m.author_id=?) AND (?='' OR m.topic_id=?) AND (?=0 OR m.created_at<? OR (m.created_at=? AND m.id<?)) ORDER BY m.created_at DESC,m.id DESC LIMIT ?"
	return pageMessages(ctx, a.read, query, []any{req.Query, micros(now), fromID, fromID, topicID, topicID, stamp, stamp, stamp, id, req.Limit + 1}, req.Limit, true)
}
func (a *Adapter) Observe(ctx context.Context, req app.ObserveRequest, now time.Time) (app.Page[domain.Message], error) {
	stamp, id, e := descendingCursor(req.Cursor)
	if e != nil {
		return app.Page[domain.Message]{}, e
	}
	var topicID string
	if req.Topic != "" {
		t, e := resolveTopic(ctx, a.read, req.Topic)
		if e != nil {
			return app.Page[domain.Message]{}, e
		}
		topicID = string(t.ID)
	}
	query := "SELECT " + messageCols + " FROM messages WHERE (expires_at IS NULL OR expires_at>?) AND (?='' OR topic_id=?) AND (?=0 OR created_at<? OR (created_at=? AND id<?)) ORDER BY created_at DESC,id DESC LIMIT ?"
	return pageMessages(ctx, a.read, query, []any{micros(now), topicID, topicID, stamp, stamp, stamp, id, req.Limit + 1}, req.Limit, true)
}

func prefixedMessageCols(alias string) string {
	parts := strings.Split(messageCols, ",")
	for i := range parts {
		parts[i] = alias + "." + parts[i]
	}
	return strings.Join(parts, ",")
}
func prefixedAgentCols(alias string) string {
	parts := strings.Split(agentCols, ",")
	for i := range parts {
		parts[i] = alias + "." + parts[i]
	}
	return strings.Join(parts, ",")
}
