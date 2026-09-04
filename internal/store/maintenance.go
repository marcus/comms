package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

func (a *Adapter) RetentionStatus(ctx context.Context, now time.Time) (app.RetentionStatus, error) {
	var out app.RetentionStatus
	e := a.read.QueryRowContext(ctx, "SELECT COALESCE(sum(CASE WHEN expires_at IS NULL OR expires_at>? THEN 1 ELSE 0 END),0),COALESCE(sum(CASE WHEN expires_at<=? THEN 1 ELSE 0 END),0),COALESCE(sum(CASE WHEN NOT EXISTS(SELECT 1 FROM messages live WHERE live.thread_root_id=messages.thread_root_id AND (live.expires_at IS NULL OR live.expires_at>?)) THEN 1 ELSE 0 END),0) FROM messages", micros(now), micros(now), micros(now)).Scan(&out.LiveMessages, &out.ExpiredMessages, &out.PurgeableMessages)
	if e != nil {
		return out, e
	}
	var run app.PurgeRun
	var started int64
	var completed sql.NullInt64
	e = a.read.QueryRowContext(ctx, "SELECT id,started_at,completed_at,removed_messages,error FROM purge_runs ORDER BY started_at DESC,id DESC LIMIT 1").Scan(&run.ID, &started, &completed, &run.RemovedMessages, &run.Error)
	if e == nil {
		run.StartedAt = timeFrom(started)
		run.CompletedAt = nullableTime(completed)
		out.LastRun = &run
	} else if e != sql.ErrNoRows {
		return out, e
	}
	return out, nil
}

func (a *Adapter) Purge(ctx context.Context, req app.PurgeRequest, id domain.PurgeRunID, now time.Time) (app.PurgeRun, error) {
	return withMutation(ctx, a, req.Mutation, "purge", now, func(tx *sql.Tx) (app.PurgeRun, error) {
		run := app.PurgeRun{ID: id, StartedAt: now}
		var count int64
		e := tx.QueryRowContext(ctx, "SELECT count(*) FROM messages m WHERE NOT EXISTS(SELECT 1 FROM messages live WHERE live.thread_root_id=m.thread_root_id AND (live.expires_at IS NULL OR live.expires_at>?))", micros(now)).Scan(&count)
		if e != nil {
			return run, e
		}
		run.RemovedMessages = count
		completed := now
		run.CompletedAt = &completed
		if req.DryRun {
			return run, nil
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO purge_runs(id,started_at,removed_messages,error) VALUES(?,?,0,'')", id, micros(now)); e != nil {
			return run, e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM messages_fts WHERE message_id IN (SELECT id FROM messages m WHERE NOT EXISTS(SELECT 1 FROM messages live WHERE live.thread_root_id=m.thread_root_id AND (live.expires_at IS NULL OR live.expires_at>?)))", micros(now)); e != nil {
			return run, e
		}
		result, e := tx.ExecContext(ctx, "DELETE FROM messages AS m WHERE NOT EXISTS(SELECT 1 FROM messages live WHERE live.thread_root_id=m.thread_root_id AND (live.expires_at IS NULL OR live.expires_at>?))", micros(now))
		if e != nil {
			return run, e
		}
		run.RemovedMessages, e = result.RowsAffected()
		if e != nil {
			return run, e
		}
		_, e = tx.ExecContext(ctx, "UPDATE purge_runs SET completed_at=?,removed_messages=? WHERE id=?", micros(now), run.RemovedMessages, id)
		return run, e
	})
}

func (a *Adapter) Snapshot(ctx context.Context) (app.Snapshot, error) {
	out := app.Snapshot{StoreID: a.storeID, Agents: []domain.Agent{}, Aliases: []app.AliasRecord{}, AgentExternalRefs: []app.ExternalAgentRefRecord{}, Topics: []domain.Topic{}, TopicExternalRefs: []app.ExternalTopicRefRecord{}, Subscriptions: []domain.Subscription{}, Messages: []domain.Message{}}
	rows, e := a.read.QueryContext(ctx, "SELECT "+agentCols+" FROM agents ORDER BY id")
	if e != nil {
		return out, e
	}
	for rows.Next() {
		v, e := scanAgent(rows)
		if e != nil {
			_ = rows.Close()
			return out, e
		}
		out.Agents = append(out.Agents, v)
	}
	if e = rows.Close(); e != nil {
		return out, e
	}
	rows, e = a.read.QueryContext(ctx, "SELECT handle,agent_id,expires_at FROM agent_aliases ORDER BY lower(handle),agent_id")
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v app.AliasRecord
		var at int64
		if e = rows.Scan(&v.Handle, &v.AgentID, &at); e != nil {
			_ = rows.Close()
			return out, e
		}
		v.ExpiresAt = timeFrom(at)
		out.Aliases = append(out.Aliases, v)
	}
	_ = rows.Close()
	rows, e = a.read.QueryContext(ctx, "SELECT namespace,external_key,agent_id FROM agent_external_refs ORDER BY namespace,external_key")
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v app.ExternalAgentRefRecord
		if e = rows.Scan(&v.ExternalRef.Namespace, &v.ExternalRef.Key, &v.AgentID); e != nil {
			_ = rows.Close()
			return out, e
		}
		out.AgentExternalRefs = append(out.AgentExternalRefs, v)
	}
	_ = rows.Close()
	rows, e = a.read.QueryContext(ctx, "SELECT "+topicCols+" FROM topics ORDER BY id")
	if e != nil {
		return out, e
	}
	for rows.Next() {
		v, e := scanTopic(rows)
		if e != nil {
			_ = rows.Close()
			return out, e
		}
		out.Topics = append(out.Topics, v)
	}
	_ = rows.Close()
	rows, e = a.read.QueryContext(ctx, "SELECT namespace,external_key,topic_id FROM topic_external_refs ORDER BY namespace,external_key")
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v app.ExternalTopicRefRecord
		if e = rows.Scan(&v.ExternalRef.Namespace, &v.ExternalRef.Key, &v.TopicID); e != nil {
			_ = rows.Close()
			return out, e
		}
		out.TopicExternalRefs = append(out.TopicExternalRefs, v)
	}
	_ = rows.Close()
	rows, e = a.read.QueryContext(ctx, "SELECT "+subscriptionCols+" FROM subscriptions ORDER BY agent_id,topic_id")
	if e != nil {
		return out, e
	}
	for rows.Next() {
		v, e := scanSubscription(rows)
		if e != nil {
			_ = rows.Close()
			return out, e
		}
		out.Subscriptions = append(out.Subscriptions, v)
	}
	_ = rows.Close()
	rows, e = a.read.QueryContext(ctx, "SELECT "+messageCols+" FROM messages ORDER BY topic_id,sequence,id")
	if e != nil {
		return out, e
	}
	for rows.Next() {
		v, e := scanMessage(rows)
		if e != nil {
			_ = rows.Close()
			return out, e
		}
		out.Messages = append(out.Messages, v)
	}
	if e = rows.Close(); e != nil {
		return out, e
	}
	return out, nil
}

func (a *Adapter) Doctor(ctx context.Context) (app.DoctorReport, error) {
	out := app.DoctorReport{Healthy: true, Checks: map[string]string{}}
	var integrity string
	if e := a.read.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); e != nil {
		return out, e
	}
	out.Checks["sqlite_integrity"] = integrity
	if integrity != "ok" {
		out.Healthy = false
	}
	var mode string
	if e := a.read.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); e != nil {
		return out, e
	}
	out.Checks["journal_mode"] = mode
	if mode != "wal" {
		out.Healthy = false
	}
	var version int
	if e := a.read.QueryRowContext(ctx, "SELECT max(version) FROM schema_migrations").Scan(&version); e != nil {
		return out, e
	}
	out.Checks["schema_version"] = fmt.Sprint(version)
	if version != schemaVersion {
		out.Healthy = false
	}
	return out, nil
}
