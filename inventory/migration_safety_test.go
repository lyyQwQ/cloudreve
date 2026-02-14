package inventory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	entschema "entgo.io/ent/dialect/sql/schema"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/enttest"
	entmigrate "github.com/cloudreve/Cloudreve/v4/ent/migrate"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

const testRequiredDBVersion = "4.13.0"

func TestMigrationSafetyTODO12f(t *testing.T) {
	t.Run("empty db migration creates required tables and hls foreign key", func(t *testing.T) {
		dsn := newMigrationTestDSN(t, "empty")
		ctx := context.Background()

		client, err := ent.Open("sqlite3", dsn)
		if err != nil {
			t.Fatalf("open ent client: %v", err)
		}
		defer client.Close()

		if _, err := InitializeDBClient(testLogger(), client, testKVStore(), testRequiredDBVersion); err != nil {
			t.Fatalf("initialize db client with migration: %v", err)
		}

		rawDB := openMigrationRawDB(t, dsn)
		defer rawDB.Close()

		assertTableExists(t, rawDB, "settings")
		assertTableExists(t, rawDB, "files")
		assertTableExists(t, rawDB, "hls_artifacts")
		assertHLSArtifactForeignKey(t, rawDB)

		if _, err := client.HLSArtifact.Query().Count(ctx); err != nil {
			t.Fatalf("query hls_artifacts after migration: %v", err)
		}
	})

	t.Run("existing-data db migration keeps old data and creates new table", func(t *testing.T) {
		dsn := newMigrationTestDSN(t, "existing")
		ctx := context.Background()

		client, err := ent.Open("sqlite3", dsn)
		if err != nil {
			t.Fatalf("open ent client: %v", err)
		}
		defer client.Close()

		if err := createOldSchemaWithoutHLSArtifacts(ctx, client); err != nil {
			t.Fatalf("create old schema: %v", err)
		}

		created, err := client.Setting.Create().SetName("migration_safety_existing_data").SetValue("keep_me").Save(ctx)
		if err != nil {
			t.Fatalf("insert pre-migration data: %v", err)
		}

		if _, err := InitializeDBClient(testLogger(), client, testKVStore(), testRequiredDBVersion); err != nil {
			t.Fatalf("run migration on existing db: %v", err)
		}

		got, err := client.Setting.Query().Where(setting.NameEQ("migration_safety_existing_data")).Only(ctx)
		if err != nil {
			t.Fatalf("query preserved data: %v", err)
		}
		if got.ID != created.ID || got.Value != created.Value {
			t.Fatalf("existing data changed after migration: got id=%d value=%q, want id=%d value=%q", got.ID, got.Value, created.ID, created.Value)
		}

		if _, err := client.HLSArtifact.Query().Count(ctx); err != nil {
			t.Fatalf("query hls_artifacts after migration: %v", err)
		}
	})

	t.Run("repeated migration is idempotent", func(t *testing.T) {
		dsn := newMigrationTestDSN(t, "idempotent")
		ctx := context.Background()

		client, err := ent.Open("sqlite3", dsn)
		if err != nil {
			t.Fatalf("open ent client: %v", err)
		}
		defer client.Close()

		if _, err := InitializeDBClient(testLogger(), client, testKVStore(), testRequiredDBVersion); err != nil {
			t.Fatalf("first migration failed: %v", err)
		}

		if _, err := client.Setting.Create().SetName("migration_safety_idempotent_data").SetValue("stable").Save(ctx); err != nil {
			t.Fatalf("insert sentinel data: %v", err)
		}

		if _, err := InitializeDBClient(testLogger(), client, testKVStore(), testRequiredDBVersion); err != nil {
			t.Fatalf("second migration failed: %v", err)
		}

		versionMarkCount, err := client.Setting.Query().Where(setting.NameEQ(DBVersionPrefix + testRequiredDBVersion)).Count(ctx)
		if err != nil {
			t.Fatalf("count version marker after repeated migration: %v", err)
		}
		if versionMarkCount != 1 {
			t.Fatalf("unexpected version marker count after repeated migration: got %d want 1", versionMarkCount)
		}

		sentinelCount, err := client.Setting.Query().Where(setting.NameEQ("migration_safety_idempotent_data")).Count(ctx)
		if err != nil {
			t.Fatalf("count sentinel data: %v", err)
		}
		if sentinelCount != 1 {
			t.Fatalf("unexpected sentinel data count: got %d want 1", sentinelCount)
		}

		sentinel, err := client.Setting.Query().Where(setting.NameEQ("migration_safety_idempotent_data")).Only(ctx)
		if err != nil {
			t.Fatalf("load sentinel data: %v", err)
		}
		if sentinel.Value != "stable" {
			t.Fatalf("sentinel data changed after repeated migration: got %q want %q", sentinel.Value, "stable")
		}
	})

	t.Run("with db_version marker migration gate indicates no migration needed", func(t *testing.T) {
		dsn := newMigrationTestDSN(t, "gate_has_marker")
		ctx := context.Background()

		client := enttest.Open(t, "sqlite3", dsn)
		defer client.Close()

		if _, err := client.Setting.Create().SetName(DBVersionPrefix + testRequiredDBVersion).SetValue("installed").Save(ctx); err != nil {
			t.Fatalf("create db version marker: %v", err)
		}

		if needMigration(client, ctx, testRequiredDBVersion) {
			t.Fatalf("needMigration should be false when db version marker exists")
		}
	})

	t.Run("without db_version marker migration gate indicates migration needed", func(t *testing.T) {
		dsn := newMigrationTestDSN(t, "gate_no_marker")
		ctx := context.Background()

		client := enttest.Open(t, "sqlite3", dsn)
		defer client.Close()

		if !needMigration(client, ctx, testRequiredDBVersion) {
			t.Fatalf("needMigration should be true when db version marker does not exist")
		}
	})
}

func createOldSchemaWithoutHLSArtifacts(ctx context.Context, client *ent.Client) error {
	tables, err := entschema.CopyTables(entmigrate.Tables)
	if err != nil {
		return fmt.Errorf("copy migrate tables: %w", err)
	}

	oldTables := make([]*entschema.Table, 0, len(tables))
	filtered := false
	for _, tbl := range tables {
		if tbl != nil && tbl.Name == "hls_artifacts" {
			filtered = true
			continue
		}
		oldTables = append(oldTables, tbl)
	}
	if !filtered {
		return fmt.Errorf("hls_artifacts table not found in migrate tables")
	}

	if err := entmigrate.Create(ctx, client.Schema, oldTables); err != nil {
		return fmt.Errorf("create old schema: %w", err)
	}

	return nil
}

func newMigrationTestDSN(t *testing.T, suffix string) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_")
	return fmt.Sprintf("file:%s_%s?mode=memory&cache=shared&_fk=1&_pragma=foreign_keys(1)", name, suffix)
}

func testLogger() logging.Logger {
	return logging.NewConsoleLogger(logging.LevelError)
}

func testKVStore() cache.Driver {
	return cache.NewMemoStore("", testLogger())
}

func openMigrationRawDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open raw sqlite connection: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping raw sqlite connection: %v", err)
	}
	return db
}

func assertTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()

	var count int
	err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count)
	if err != nil {
		t.Fatalf("query sqlite_master for table %s: %v", table, err)
	}
	if count != 1 {
		t.Fatalf("table %s not found after migration", table)
	}
}

func assertHLSArtifactForeignKey(t *testing.T, db *sql.DB) {
	t.Helper()

	rows, err := db.Query(`PRAGMA foreign_key_list('hls_artifacts')`)
	if err != nil {
		t.Fatalf("query hls_artifacts foreign keys: %v", err)
	}
	defer rows.Close()

	var found bool
	for rows.Next() {
		var id int
		var seq int
		var table string
		var from string
		var to string
		var onUpdate string
		var onDelete string
		var match string

		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan hls_artifacts foreign key row: %v", err)
		}

		if table == "files" && from == "source_file_id" && to == "id" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate hls_artifacts foreign key rows: %v", err)
	}

	if !found {
		t.Fatalf("expected foreign key hls_artifacts.source_file_id -> files.id was not found")
	}
}
