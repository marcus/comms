package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

// legacyDatabase writes a schema-1 database directly, the way a store created
// by comms v1.3.0 looks on disk.
func legacyDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comms.db")
	db, e := sql.Open("sqlite", sqliteDSN(path, false))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	body, e := migrationFiles.ReadFile("migrations/001_initial.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(body)); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO schema_migrations(version,applied_at) VALUES(1,0)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO store_meta(key,value) VALUES('store_id','sto_legacy')"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO agents(id,handle,created_at,updated_at,last_seen_at) VALUES('agt_aaaaaaaaaaaaaaaaaaaaaaaaaa','legacy',0,0,0)"); e != nil {
		t.Fatal(e)
	}
	return path
}

func TestMigrationRunnerUpgradesALegacyDatabaseInPlace(t *testing.T) {
	path := legacyDatabase(t)
	adapter, e := Open(context.Background(), Options{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	handshake, e := adapter.Handshake(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if handshake.SchemaVersion != schemaVersion || handshake.StoreID != "sto_legacy" {
		t.Fatalf("handshake=%#v", handshake)
	}
	var version int
	if e = adapter.read.QueryRow("SELECT max(version) FROM schema_migrations").Scan(&version); e != nil {
		t.Fatal(e)
	}
	if version != 2 {
		t.Fatalf("schema version after migration=%d", version)
	}
	var retained string
	if e = adapter.read.QueryRow("SELECT handle FROM agents WHERE id='agt_aaaaaaaaaaaaaaaaaaaaaaaaaa'").Scan(&retained); e != nil || retained != "legacy" {
		t.Fatalf("migration lost existing rows: %q %v", retained, e)
	}
	var retrievals int
	if e = adapter.read.QueryRow("SELECT count(*) FROM message_retrievals").Scan(&retrievals); e != nil {
		t.Fatalf("message_retrievals missing after migration: %v", e)
	}
	report, e := adapter.Doctor(context.Background())
	if e != nil || !report.Healthy || report.Checks["schema_version"] != "2" {
		t.Fatalf("doctor=%#v %v", report, e)
	}
}

func TestEmbeddedMigrationsMatchTheSchemaVersion(t *testing.T) {
	migrations, e := embeddedMigrations()
	if e != nil {
		t.Fatal(e)
	}
	if len(migrations) != schemaVersion {
		t.Fatalf("embedded migrations=%d schema version=%d", len(migrations), schemaVersion)
	}
}

// retrievalRow reads the stored row for one message and agent.
func retrievalRow(t *testing.T, a *Adapter, message domain.MessageID, agent domain.AgentID) (first, last int64, inspected sql.NullInt64, count int64, found bool) {
	t.Helper()
	e := a.read.QueryRow("SELECT first_seen_at,last_seen_at,first_inspected_at,seen_count FROM message_retrievals WHERE message_id=? AND agent_id=?", message, agent).Scan(&first, &last, &inspected, &count)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, 0, inspected, 0, false
	}
	if e != nil {
		t.Fatal(e)
	}
	return first, last, inspected, count, true
}

func TestRecordRetrievalsEscalatesDepthAndSkipsAuthorsAndUnknownReaders(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)
	message, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "build", Title: "one", Body: "body"})
	if e != nil {
		t.Fatal(e)
	}
	seen := sys.clock.Now()
	inspectedAt := seen.Add(time.Minute)
	if e = sys.adapter.RecordRetrievals(ctx, []app.RetrievalEvent{
		{MessageID: message.ID, Agent: "bob", Depth: app.RetrievalPreview, At: seen},
		{MessageID: message.ID, Agent: "alice", Depth: app.RetrievalFull, At: seen},
		{MessageID: message.ID, Agent: "nobody", Depth: app.RetrievalFull, At: seen},
		{MessageID: "msg_aaaaaaaaaaaaaaaaaaaaaaaaaa", Agent: "bob", Depth: app.RetrievalFull, At: seen},
	}); e != nil {
		t.Fatal(e)
	}
	first, last, inspected, count, found := retrievalRow(t, sys.adapter, message.ID, bob.ID)
	if !found || count != 1 || inspected.Valid || first != micros(seen) || last != micros(seen) {
		t.Fatalf("preview row=%d %d %#v %d %v", first, last, inspected, count, found)
	}
	if _, _, _, _, found = retrievalRow(t, sys.adapter, message.ID, alice.ID); found {
		t.Fatal("author retrieval was recorded")
	}
	// A full retrieval after a preview escalates without losing the first sighting.
	if e = sys.adapter.RecordRetrievals(ctx, []app.RetrievalEvent{{MessageID: message.ID, Agent: string(bob.ID), Depth: app.RetrievalFull, At: inspectedAt}}); e != nil {
		t.Fatal(e)
	}
	first, last, inspected, count, _ = retrievalRow(t, sys.adapter, message.ID, bob.ID)
	if first != micros(seen) || last != micros(inspectedAt) || !inspected.Valid || inspected.Int64 != micros(inspectedAt) || count != 2 {
		t.Fatalf("escalated row=%d %d %#v %d", first, last, inspected, count)
	}
	// A later preview never overwrites the recorded full inspection.
	if e = sys.adapter.RecordRetrievals(ctx, []app.RetrievalEvent{{MessageID: message.ID, Agent: "bob", Depth: app.RetrievalPreview, At: inspectedAt.Add(time.Minute)}}); e != nil {
		t.Fatal(e)
	}
	_, _, inspected, count, _ = retrievalRow(t, sys.adapter, message.ID, bob.ID)
	if !inspected.Valid || inspected.Int64 != micros(inspectedAt) || count != 3 {
		t.Fatalf("preview after full row=%#v %d", inspected, count)
	}
	if e = sys.adapter.RecordRetrievals(ctx, nil); e != nil {
		t.Fatalf("empty batch=%v", e)
	}
}

func TestReceiptsReportSeparatesSubscribersFromInspectors(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	watcher := join(t, sys.service, "watcher", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)
	message, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "build", Title: "one", Body: "body"})
	if e != nil {
		t.Fatal(e)
	}
	now := sys.clock.Now()
	if e = sys.adapter.RecordRetrievals(ctx, []app.RetrievalEvent{
		{MessageID: message.ID, Agent: "bob", Depth: app.RetrievalPreview, At: now},
		{MessageID: message.ID, Agent: "watcher", Depth: app.RetrievalFull, At: now},
	}); e != nil {
		t.Fatal(e)
	}
	report, e := sys.service.Receipts(ctx, string(message.ID))
	if e != nil {
		t.Fatal(e)
	}
	if len(report.Subscribers) != 1 || report.Subscribers[0].Agent.ID != bob.ID {
		t.Fatalf("subscribers=%#v", report.Subscribers)
	}
	subscriber := report.Subscribers[0]
	if subscriber.State != "unread" || subscriber.SeenAt == nil || subscriber.InspectedAt != nil || subscriber.SeenCount != 1 {
		t.Fatalf("preview subscriber=%#v", subscriber)
	}
	if len(report.Inspectors) != 1 || report.Inspectors[0].Agent.ID != watcher.ID || report.Inspectors[0].InspectedAt == nil {
		t.Fatalf("inspectors=%#v", report.Inspectors)
	}
	// Acknowledging keeps the retrieval facts and adds the cursor fact.
	if _, e = sys.service.ReadThrough(ctx, app.ReadThroughRequest{Agent: "bob", Message: string(message.ID)}); e != nil {
		t.Fatal(e)
	}
	report, e = sys.service.Receipts(ctx, string(message.ID))
	if e != nil {
		t.Fatal(e)
	}
	subscriber = report.Subscribers[0]
	if subscriber.State != "read" || subscriber.ReadAt == nil || subscriber.SeenAt == nil {
		t.Fatalf("acknowledged subscriber=%#v", subscriber)
	}
	// A subscriber that follows later has no retrieval row and no retrieval fields.
	follow(t, sys.service, watcher, topic)
	report, e = sys.service.Receipts(ctx, string(message.ID))
	if e != nil {
		t.Fatal(e)
	}
	if len(report.Subscribers) != 2 || len(report.Inspectors) != 0 {
		t.Fatalf("after follow report=%#v", report)
	}
}

func TestPurgeCascadesRetrievalRowsAfterExportHasSeenThem(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)
	message, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "build", Title: "one", Body: "body", Expiry: app.Expiry{After: time.Hour}})
	if e != nil {
		t.Fatal(e)
	}
	if e = sys.adapter.RecordRetrievals(ctx, []app.RetrievalEvent{{MessageID: message.ID, Agent: "bob", Depth: app.RetrievalFull, At: sys.clock.Now()}}); e != nil {
		t.Fatal(e)
	}
	snapshot, e := sys.adapter.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(snapshot.Retrievals) != 1 || snapshot.Retrievals[0].MessageID != message.ID || snapshot.Retrievals[0].AgentID != bob.ID || snapshot.Retrievals[0].FirstInspectedAt == nil {
		t.Fatalf("snapshot retrievals=%#v", snapshot.Retrievals)
	}
	sys.clock.Advance(2 * time.Hour)
	if _, e = sys.service.Purge(ctx, app.PurgeRequest{}); e != nil {
		t.Fatal(e)
	}
	var remaining int
	if e = sys.adapter.read.QueryRow("SELECT count(*) FROM message_retrievals").Scan(&remaining); e != nil {
		t.Fatal(e)
	}
	if remaining != 0 {
		t.Fatalf("purge left %d retrieval rows", remaining)
	}
	snapshot, e = sys.adapter.Snapshot(ctx)
	if e != nil || len(snapshot.Retrievals) != 0 {
		t.Fatalf("snapshot after purge=%#v %v", snapshot.Retrievals, e)
	}
}
