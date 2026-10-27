package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"modernc.org/sqlite"

	"github.com/marcus/comms/internal/app"
)

func TestEveryConstraintClassIsMappedWithoutDriverText(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA foreign_keys=ON;
 CREATE TABLE parent(id TEXT PRIMARY KEY);
 INSERT INTO parent VALUES('parent');
 CREATE TABLE guarded(id INTEGER PRIMARY KEY,u TEXT UNIQUE NOT NULL,n INTEGER CHECK(n>0),p TEXT REFERENCES parent(id));
 INSERT INTO guarded VALUES(1,'unique',1,'parent');
 CREATE TRIGGER reject_marker BEFORE INSERT ON guarded WHEN NEW.u='trigger' BEGIN SELECT RAISE(ABORT,'private driver marker'); END;
 CREATE TABLE typed(n INTEGER) STRICT;`)
	if err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{
		"unique":      `INSERT INTO guarded VALUES(2,'unique',1,'parent')`,
		"primary key": `INSERT INTO guarded VALUES(1,'different',1,'parent')`,
		"not null":    `INSERT INTO guarded VALUES(2,NULL,1,'parent')`,
		"check":       `INSERT INTO guarded VALUES(2,'check',0,'parent')`,
		"foreign key": `INSERT INTO guarded VALUES(2,'foreign',1,'absent')`,
		"trigger":     `INSERT INTO guarded VALUES(2,'trigger',1,'parent')`,
		"datatype":    `INSERT INTO typed VALUES('text')`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := db.Exec(query)
			var raw *sqlite.Error
			if !errors.As(err, &raw) || raw.Code()&0xff != 19 {
				t.Fatalf("not a driver constraint: %v", err)
			}
			normalized := translate(fmt.Errorf("operation detail: %w", err))
			if !errors.Is(normalized, app.ErrConflict) {
				t.Fatalf("classification=%v", normalized)
			}
			for _, text := range []string{normalized.Error(), app.PublicErrorMessage(normalized), app.PublicErrorMessage(err), app.PublicErrorMessage(fmt.Errorf("%w: %w", app.ErrConflict, err))} {
				for _, forbidden := range []string{"sqlite", "constraint failed", "guarded", "parent", "private driver marker", "2067", "787"} {
					if strings.Contains(strings.ToLower(text), forbidden) {
						t.Fatalf("driver text leaked: %s", text)
					}
				}
			}
		})
	}
	_, err = db.Exec("SELECT * FROM nonexistent_private_table")
	if normalized := translate(err); !errors.Is(normalized, app.ErrInternal) || strings.Contains(normalized.Error(), "nonexistent") {
		t.Fatalf("internal storage error=%v", normalized)
	}
	if _, err = db.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO typed VALUES(1)")
	if normalized := translate(err); !errors.Is(normalized, app.ErrUnavailable) {
		t.Fatalf("read-only=%v", normalized)
	}
}

func TestMutationCommitAndOpenErrorsUseTranslation(t *testing.T) {
	sys := newTestSystem(t)
	// A deferred reference failure occurs at COMMIT, outside the mutation body.
	_, err := sys.adapter.submit(context.Background(), func(conn *sql.Conn) (any, error) {
		tx, err := conn.BeginTx(context.Background(), nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err = tx.Exec("PRAGMA defer_foreign_keys=ON; INSERT INTO agent_external_refs VALUES('fixture','key','absent')"); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	})
	if !errors.Is(err, app.ErrConflict) || strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("commit error=%v", err)
	}
}

// New adapter methods must pass through the same error boundary, so a raw
// driver error cannot escape merely because a future query is a read.
func TestEveryStoreOperationHasAnErrorBoundary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || fn.Body == nil {
				continue
			}
			receiver, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			receiverName, ok := receiver.X.(*ast.Ident)
			if !ok || receiverName.Name != "Adapter" {
				continue
			}
			guarded := false
			for _, stmt := range fn.Body.List {
				deferred, ok := stmt.(*ast.DeferStmt)
				if !ok {
					continue
				}
				ast.Inspect(deferred, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "translate" {
							guarded = true
						}
					}
					return true
				})
			}
			if !guarded {
				t.Errorf("%s.%s lacks the storage error boundary", path, fn.Name.Name)
			}
		}
	}
}

func TestOpenHidesCorruptDatabaseDriverText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.db")
	if err := os.WriteFile(path, []byte("private invalid database content"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := Open(context.Background(), Options{Path: path})
	if adapter != nil {
		_ = adapter.Close()
		t.Fatal("opened corrupt fixture")
	}
	if !errors.Is(err, app.ErrInternal) || err.Error() != "internal error: storage operation failed" {
		t.Fatalf("open=%v", err)
	}
}
