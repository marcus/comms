package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

func schemaThreeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comms.db")
	db, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, name := range []string{"001_initial.sql", "002_message_retrievals.sql", "003_message_kind.sql"} {
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO schema_migrations VALUES(3,0);
 INSERT INTO store_meta VALUES('store_id','sto_aaaaaaaaaaaaaaaaaaaaaaaaaa');
 INSERT INTO agents(id,handle,created_at,updated_at,last_seen_at,retired_at) VALUES('agt_aaaaaaaaaaaaaaaaaaaaaaaaaa','scout',0,0,0,1),('agt_bbbbbbbbbbbbbbbbbbbbbbbbbb','bob',0,0,0,NULL);
 INSERT INTO agent_external_refs VALUES('host','old-scout','agt_aaaaaaaaaaaaaaaaaaaaaaaaaa');
 INSERT INTO agent_aliases VALUES('old-alias','agt_aaaaaaaaaaaaaaaaaaaaaaaaaa',99999999999999999);
 INSERT INTO topics(id,name,name_key,kind,created_at,updated_at) VALUES('top_aaaaaaaaaaaaaaaaaaaaaaaaaa','history','history','public',0,0);
 INSERT INTO subscriptions VALUES('agt_aaaaaaaaaaaaaaaaaaaaaaaaaa','top_aaaaaaaaaaaaaaaaaaaaaaaaaa',0,NULL,0,NULL,0);
 INSERT INTO messages(id,topic_id,sequence,author_id,author_context_json,title,body,thread_root_id,created_at) VALUES('msg_aaaaaaaaaaaaaaaaaaaaaaaaaa','top_aaaaaaaaaaaaaaaaaaaaaaaaaa',1,'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa','{}','old','historical','msg_aaaaaaaaaaaaaaaaaaaaaaaaaa',0);`)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestActiveHandleMigrationPreservesReferences(t *testing.T) {
	path := schemaThreeFixture(t)
	ctx := context.Background()
	adapter, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	svc := app.NewService(adapter, domain.UTCClock{})
	defer func() { _ = svc.Close() }()
	fresh, err := svc.Join(ctx, app.JoinRequest{Handle: "scout"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Agent.ID == "agt_aaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatal("resurrected old identity")
	}
	old, err := svc.Peek(ctx, app.PeekRequest{Message: "msg_aaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil || old.AuthorID != "agt_aaaaaaaaaaaaaaaaaaaaaaaaaa" || old.AuthorHandle != "scout" {
		t.Fatalf("old=%+v %v", old, err)
	}
	snapshot, err := svc.Snapshot(ctx)
	if err != nil || len(snapshot.AgentExternalRefs) != 1 || len(snapshot.Subscriptions) != 1 || len(snapshot.Aliases) != 0 {
		t.Fatalf("snapshot=%+v %v", snapshot, err)
	}
	var foreignKeys int
	_, err = adapter.submit(ctx, func(conn *sql.Conn) (any, error) {
		return nil, conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys)
	})
	if err != nil || foreignKeys != 1 {
		t.Fatalf("FK enforcement=%d %v", foreignKeys, err)
	}
	rows, err := adapter.read.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("migration broke a child reference")
	}
}

func TestActiveHandleMigrationFailureRollsBackAndRestoresForeignKeys(t *testing.T) {
	path := schemaThreeFixture(t)
	db, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	// Inject an orphan to ensure the rebuild cannot commit invalid references.
	if _, err = db.Exec("PRAGMA foreign_keys=OFF; UPDATE messages SET author_id='absent'; PRAGMA foreign_keys=ON;"); err != nil {
		t.Fatal(err)
	}
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if err = applyMigration(context.Background(), db, migrations[3], 0); err == nil {
		t.Fatal("accepted invalid references")
	}
	var version, enabled int
	if err = db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || version != 3 {
		t.Fatalf("version=%d %v", version, err)
	}
	if err = db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("FK state=%d %v", enabled, err)
	}
	var schema string
	if err = db.QueryRow("SELECT sql FROM sqlite_master WHERE name='agents'").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if len(schema) == 0 {
		t.Fatal("original agents table lost")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
