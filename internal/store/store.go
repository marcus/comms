// Package store contains the sole production persistence adapter for Comms.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const schemaVersion = 2

type Options struct {
	Path            string
	ReadConnections int
	QueueDepth      int
}

type Adapter struct {
	write     *sql.DB
	read      *sql.DB
	owner     *os.File
	requests  chan writeRequest
	done      chan struct{}
	closeOnce sync.Once
	sendMu    sync.RWMutex
	closed    atomic.Bool
	storeID   string
}
type writeRequest struct {
	ctx    context.Context
	fn     func(*sql.Conn) (any, error)
	result chan writeResult
}
type writeResult struct {
	value any
	err   error
}

func ResolveStatePath(create bool) (string, error) {
	dir := os.Getenv("COMMS_STATE_DIR")
	if dir == "" {
		dir = os.Getenv("XDG_STATE_HOME")
		if dir == "" {
			home, e := os.UserHomeDir()
			if e != nil {
				return "", e
			}
			dir = filepath.Join(home, ".local", "state")
		}
		dir = filepath.Join(dir, "comms")
	}
	if create {
		if e := os.MkdirAll(dir, 0o700); e != nil {
			return "", fmt.Errorf("create state directory: %w", e)
		}
	}
	return filepath.Join(dir, "comms.db"), nil
}

func Open(ctx context.Context, opts Options) (*Adapter, error) {
	path := opts.Path
	if path == "" {
		var e error
		path, e = ResolveStatePath(true)
		if e != nil {
			return nil, e
		}
	} else if e := os.MkdirAll(filepath.Dir(path), 0o700); e != nil {
		return nil, e
	}
	owner, e := acquireOwner(path + ".lock")
	if e != nil {
		return nil, e
	}
	cleanup := func() { _ = unix.Flock(int(owner.Fd()), unix.LOCK_UN); _ = owner.Close() }
	dsn := sqliteDSN(path, false)
	w, e := sql.Open("sqlite", dsn)
	if e != nil {
		cleanup()
		return nil, e
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	if e = migrate(ctx, w); e != nil {
		_ = w.Close()
		cleanup()
		return nil, e
	}
	r, e := sql.Open("sqlite", sqliteDSN(path, true))
	if e != nil {
		_ = w.Close()
		cleanup()
		return nil, e
	}
	reads := opts.ReadConnections
	if reads <= 0 {
		reads = 4
	}
	r.SetMaxOpenConns(reads)
	r.SetMaxIdleConns(reads)
	if e = r.PingContext(ctx); e != nil {
		_ = r.Close()
		_ = w.Close()
		cleanup()
		return nil, e
	}
	var storeID string
	if e = w.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='store_id'").Scan(&storeID); e != nil {
		_ = r.Close()
		_ = w.Close()
		cleanup()
		return nil, e
	}
	depth := opts.QueueDepth
	if depth <= 0 {
		depth = 64
	}
	a := &Adapter{write: w, read: r, owner: owner, requests: make(chan writeRequest, depth), done: make(chan struct{}), storeID: storeID}
	go a.runWriter()
	return a, nil
}

func sqliteDSN(path string, readOnly bool) string {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	if readOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func acquireOwner(path string) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: another service owns %s", app.ErrUnavailable, path)
	}
	if e = f.Truncate(0); e == nil {
		_, e = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	}
	if e != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return nil, e
	}
	return f, nil
}

// migration is one embedded schema step. Files are named NNN_description.sql
// and applied in version order, so adding a file is the whole change.
type migration struct {
	version int
	body    string
}

// embeddedMigrations returns every embedded migration in version order and
// refuses a catalog that would leave the schema ambiguous: a malformed name, a
// gap or duplicate in the sequence, or a highest version that disagrees with
// the schemaVersion this binary claims to speak.
func embeddedMigrations() ([]migration, error) {
	entries, e := migrationFiles.ReadDir("migrations")
	if e != nil {
		return nil, e
	}
	out := make([]migration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, found := strings.Cut(name, "_")
		version, convErr := strconv.Atoi(prefix)
		if !found || convErr != nil || version <= 0 {
			return nil, fmt.Errorf("migration %q must be named NNN_description.sql", name)
		}
		body, e := migrationFiles.ReadFile("migrations/" + name)
		if e != nil {
			return nil, e
		}
		out = append(out, migration{version: version, body: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must run contiguously from 1; found %d at position %d", m.version, i+1)
		}
	}
	if len(out) == 0 || out[len(out)-1].version != schemaVersion {
		return nil, fmt.Errorf("highest embedded migration does not match schema version %d", schemaVersion)
	}
	return out, nil
}

func currentSchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var exists int
	if e := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&exists); e != nil {
		return 0, e
	}
	if exists == 0 {
		return 0, nil
	}
	var current int
	e := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&current)
	return current, e
}

func migrate(ctx context.Context, db *sql.DB) error {
	migrations, e := embeddedMigrations()
	if e != nil {
		return e
	}
	current, e := currentSchemaVersion(ctx, db)
	if e != nil {
		return e
	}
	// Refuse future schemas before pragmas that can alter the database.
	if current > schemaVersion {
		return fmt.Errorf("%w: database schema %d is newer than supported %d", app.ErrConflict, current, schemaVersion)
	}
	if _, e := db.ExecContext(ctx, "PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;"); e != nil {
		return fmt.Errorf("configure sqlite: %w", e)
	}
	now := micros(time.Now().UTC())
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if e := applyMigration(ctx, db, m, now); e != nil {
			return e
		}
	}
	return ensureStoreID(ctx, db)
}

// applyMigration runs one step and records its version in the same transaction,
// so a failure leaves the database at the previous version rather than half
// upgraded.
func applyMigration(ctx context.Context, db *sql.DB, m migration, now int64) error {
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, m.body); e != nil {
		return fmt.Errorf("migration %d: %w", m.version, e)
	}
	if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(?,?)", m.version, now); e != nil {
		return e
	}
	return tx.Commit()
}

func ensureStoreID(ctx context.Context, db *sql.DB) error {
	var existing string
	e := db.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='store_id'").Scan(&existing)
	if e == nil {
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	id, e := domain.NewTopicID()
	if e != nil {
		return e
	}
	_, e = db.ExecContext(ctx, "INSERT OR IGNORE INTO store_meta(key,value) VALUES('store_id',?)", strings.Replace(string(id), "top_", "sto_", 1))
	return e
}

func (a *Adapter) runWriter() {
	conn, e := a.write.Conn(context.Background())
	if e != nil {
		for req := range a.requests {
			req.result <- writeResult{err: e}
		}
		close(a.done)
		return
	}
	defer func() { _ = conn.Close() }()
	for req := range a.requests {
		if e := req.ctx.Err(); e != nil {
			req.result <- writeResult{err: e}
			continue
		}
		v, e := req.fn(conn)
		req.result <- writeResult{v, e}
	}
	close(a.done)
}
func (a *Adapter) submit(ctx context.Context, fn func(*sql.Conn) (any, error)) (any, error) {
	a.sendMu.RLock()
	if a.closed.Load() {
		a.sendMu.RUnlock()
		return nil, app.ErrClosed
	}
	req := writeRequest{ctx: ctx, fn: fn, result: make(chan writeResult, 1)}
	select {
	case a.requests <- req:
		a.sendMu.RUnlock()
	case <-ctx.Done():
		a.sendMu.RUnlock()
		return nil, ctx.Err()
	default:
		a.sendMu.RUnlock()
		return nil, app.ErrOverloaded
	}
	select {
	case r := <-req.result:
		return r.value, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (a *Adapter) Close() error {
	var out error
	a.closeOnce.Do(func() {
		a.sendMu.Lock()
		a.closed.Store(true)
		close(a.requests)
		a.sendMu.Unlock()
		<-a.done
		_, _ = a.write.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		e1 := a.read.Close()
		e2 := a.write.Close()
		_ = unix.Flock(int(a.owner.Fd()), unix.LOCK_UN)
		e3 := a.owner.Close()
		out = errors.Join(e1, e2, e3)
	})
	return out
}

func (a *Adapter) Handshake(context.Context) (app.Handshake, error) {
	return app.Handshake{StoreID: a.storeID, ProtocolVersion: app.ProtocolVersion, SchemaVersion: schemaVersion, Capabilities: []string{"agents", "topics", "subscriptions", "messages", "direct", "threads", "receipts", "search", "retention", "observe", "export"}}, nil
}

func micros(t time.Time) int64   { return t.UTC().UnixMicro() }
func timeFrom(v int64) time.Time { return time.UnixMicro(v).UTC() }
func nullableTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := timeFrom(v.Int64)
	return &t
}
func nullableMicros(v *time.Time) any {
	if v == nil {
		return nil
	}
	return micros(*v)
}
func rawJSON(v json.RawMessage) any {
	if len(v) == 0 {
		return nil
	}
	return string(v)
}
func encodeCursor(parts ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "\x00")))
}
func decodeCursor(v string, want int) ([]string, error) {
	if v == "" {
		return make([]string, want), nil
	}
	b, e := base64.RawURLEncoding.DecodeString(v)
	if e != nil {
		return nil, fmt.Errorf("%w: malformed cursor", domain.ErrInvalid)
	}
	p := strings.Split(string(b), "\x00")
	if len(p) != want {
		return nil, fmt.Errorf("%w: malformed cursor", domain.ErrInvalid)
	}
	return p, nil
}

func translate(err error) error {
	if err == nil {
		return nil
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "unique constraint"), strings.Contains(s, "constraint failed"):
		return fmt.Errorf("%w: %w", app.ErrConflict, err)
	default:
		return err
	}
}

func withMutation[T any](ctx context.Context, a *Adapter, m app.Mutation, operation string, now time.Time, fn func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	v, e := a.submit(ctx, func(conn *sql.Conn) (any, error) {
		tx, e := conn.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer func() { _ = tx.Rollback() }()
		if m.ClientID != "" {
			var priorOp, raw string
			e = tx.QueryRowContext(ctx, "SELECT operation,response_json FROM idempotency_keys WHERE client_id=? AND request_id=?", m.ClientID, m.RequestID).Scan(&priorOp, &raw)
			if e == nil {
				if priorOp != operation {
					return nil, fmt.Errorf("%w: idempotency key already used for %s", app.ErrConflict, priorOp)
				}
				var prior T
				if e = json.Unmarshal([]byte(raw), &prior); e != nil {
					return nil, e
				}
				return prior, nil
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
		}
		result, e := fn(tx)
		if e != nil {
			return nil, translate(e)
		}
		if m.ClientID != "" {
			raw, e := json.Marshal(result)
			if e != nil {
				return nil, e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO idempotency_keys(client_id,request_id,operation,response_json,created_at) VALUES(?,?,?,?,?)", m.ClientID, m.RequestID, operation, string(raw), micros(now)); e != nil {
				return nil, translate(e)
			}
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
		return result, nil
	})
	if e != nil {
		return zero, e
	}
	return v.(T), nil
}

func scanAgent(s interface{ Scan(...any) error }) (domain.Agent, error) {
	var a domain.Agent
	var created, updated, seen int64
	var retired sql.NullInt64
	e := s.Scan(&a.ID, &a.Handle, &a.DisplayName, &a.Purpose, &a.Harness, &a.Project, &a.SessionRef, &created, &updated, &seen, &retired)
	a.CreatedAt = timeFrom(created)
	a.UpdatedAt = timeFrom(updated)
	a.LastSeenAt = timeFrom(seen)
	a.RetiredAt = nullableTime(retired)
	return a, e
}

const agentCols = "id,handle,display_name,purpose,harness,project,session_ref,created_at,updated_at,last_seen_at,retired_at"

func resolveAgent(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ref string, now time.Time) (domain.Agent, error) {
	row := q.QueryRowContext(ctx, "SELECT "+agentCols+" FROM agents WHERE id=? OR handle=? COLLATE NOCASE OR id=(SELECT agent_id FROM agent_aliases WHERE handle=? COLLATE NOCASE AND expires_at>?) ORDER BY CASE WHEN id=? THEN 0 WHEN handle=? COLLATE NOCASE THEN 1 ELSE 2 END LIMIT 1", ref, ref, ref, micros(now), ref, ref)
	a, e := scanAgent(row)
	if errors.Is(e, sql.ErrNoRows) {
		return a, fmt.Errorf("%w: agent %q", app.ErrNotFound, ref)
	}
	return a, e
}
func scanTopic(s interface{ Scan(...any) error }) (domain.Topic, error) {
	var t domain.Topic
	var kind string
	var created, updated int64
	var archived sql.NullInt64
	e := s.Scan(&t.ID, &t.Name, &kind, &t.Description, &t.NextSequence, &created, &updated, &archived)
	t.Kind = domain.TopicKind(kind)
	t.CreatedAt = timeFrom(created)
	t.UpdatedAt = timeFrom(updated)
	t.ArchivedAt = nullableTime(archived)
	return t, e
}

const topicCols = "id,name,kind,description,next_sequence,created_at,updated_at,archived_at"
const topicInsertCols = "id,name,name_key,kind,description,next_sequence,created_at,updated_at,archived_at"

func resolveTopic(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ref string) (domain.Topic, error) {
	t, e := scanTopic(q.QueryRowContext(ctx, "SELECT "+topicCols+" FROM topics WHERE id=? OR name_key=? LIMIT 1", ref, domain.TopicNameKey(ref)))
	if errors.Is(e, sql.ErrNoRows) {
		return t, fmt.Errorf("%w: topic %q", app.ErrNotFound, ref)
	}
	return t, e
}
