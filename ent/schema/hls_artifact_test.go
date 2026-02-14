package schema_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"testing"

	"entgo.io/ent/dialect/sql/schema"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/enttest"
	"github.com/cloudreve/Cloudreve/v4/ent/migrate"
	_ "github.com/cloudreve/Cloudreve/v4/ent/runtime"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"modernc.org/sqlite"
)

type sqlite3Driver struct {
	*sqlite.Driver
}

type sqlite3DriverConn interface {
	Exec(string, []driver.Value) (driver.Result, error)
}

func (d sqlite3Driver) Open(name string) (conn driver.Conn, err error) {
	conn, err = d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	_, err = conn.(sqlite3DriverConn).Exec("PRAGMA foreign_keys = ON;", nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func init() {
	sql.Register("sqlite3", sqlite3Driver{Driver: &sqlite.Driver{}})
}

func TestHLSArtifact_TableCreated(t *testing.T) {
	client := enttest.Open(t, "sqlite3", filepath.Join(t.TempDir(), "ent.db"))
	defer client.Close()

	ctx := context.Background()
	if _, err := client.HLSArtifact.Query().Count(ctx); err != nil {
		t.Fatalf("query hls_artifacts table: %v", err)
	}
}

func TestHLSArtifact_MigrationCompatibility(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "compat.db")

	client, err := ent.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open ent client: %v", err)
	}
	defer client.Close()

	tables, err := schema.CopyTables(migrate.Tables)
	if err != nil {
		t.Fatalf("copy migrate tables: %v", err)
	}

	oldTables := make([]*schema.Table, 0, len(tables))
	filtered := false
	for _, tbl := range tables {
		if tbl != nil && tbl.Name == "hls_artifacts" {
			filtered = true
			continue
		}
		oldTables = append(oldTables, tbl)
	}
	if !filtered {
		t.Fatalf("expected table hls_artifacts to exist in migrate.Tables")
	}

	if err := migrate.Create(ctx, client.Schema, oldTables); err != nil {
		t.Fatalf("create old schema: %v", err)
	}

	created, err := client.Setting.Create().SetName("migration_compat").SetValue("v1").Save(ctx)
	if err != nil {
		t.Fatalf("insert existing data: %v", err)
	}

	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("migrate to new schema: %v", err)
	}

	got, err := client.Setting.Query().Where(setting.Name("migration_compat")).Only(ctx)
	if err != nil {
		t.Fatalf("query existing data after migration: %v", err)
	}
	if got.ID != created.ID || got.Value != created.Value {
		t.Fatalf("existing data changed after migration: got id=%d value=%q want id=%d value=%q", got.ID, got.Value, created.ID, created.Value)
	}

	if _, err := client.HLSArtifact.Query().Count(ctx); err != nil {
		t.Fatalf("query new table after migration: %v", err)
	}
}

func TestHLSArtifact_CRUDAndEdge(t *testing.T) {
	// Rollback hint: DROP TABLE hls_artifacts
	client := enttest.Open(t, "sqlite3", filepath.Join(t.TempDir(), "crud.db"))
	defer client.Close()

	ctx := context.Background()

	grp, err := client.Group.Create().
		SetName("g1").
		SetPermissions(&boolset.BooleanSet{}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	usr, err := client.User.Create().
		SetEmail("u1@example.com").
		SetNick("u1").
		SetGroupUsers(grp.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	f, err := client.File.Create().
		SetType(0).
		SetName("video.mp4").
		SetOwnerID(usr.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}

	a, err := client.HLSArtifact.Create().
		SetSourceFileID(f.ID).
		SetStoragePath("hls/video").
		Save(ctx)
	if err != nil {
		t.Fatalf("create hls artifact: %v", err)
	}
	if a.SegmentCount != 0 || a.TotalSize != 0 || a.Codec != "h264/aac" {
		t.Fatalf("unexpected defaults: segment_count=%d total_size=%d codec=%q", a.SegmentCount, a.TotalSize, a.Codec)
	}

	fileViaEdge, err := a.QuerySourceFile().Only(ctx)
	if err != nil {
		t.Fatalf("query source file edge: %v", err)
	}
	if fileViaEdge.ID != f.ID {
		t.Fatalf("unexpected edge target: got file id=%d want id=%d", fileViaEdge.ID, f.ID)
	}

	artifactViaFile, err := f.QueryHlsArtifact().Only(ctx)
	if err != nil {
		t.Fatalf("query inverse edge from file: %v", err)
	}
	if artifactViaFile.ID != a.ID {
		t.Fatalf("unexpected inverse edge target: got artifact id=%d want id=%d", artifactViaFile.ID, a.ID)
	}

	updated, err := client.HLSArtifact.UpdateOneID(a.ID).
		SetSegmentCount(10).
		SetTotalSize(1024).
		SetCodec("h265/aac").
		Save(ctx)
	if err != nil {
		t.Fatalf("update hls artifact: %v", err)
	}
	if updated.SegmentCount != 10 || updated.TotalSize != 1024 || updated.Codec != "h265/aac" {
		t.Fatalf("update not persisted: segment_count=%d total_size=%d codec=%q", updated.SegmentCount, updated.TotalSize, updated.Codec)
	}

	if err := client.HLSArtifact.DeleteOneID(a.ID).Exec(ctx); err != nil {
		t.Fatalf("delete hls artifact: %v", err)
	}
	if n, err := client.HLSArtifact.Query().Count(ctx); err != nil {
		t.Fatalf("count hls artifacts: %v", err)
	} else if n != 0 {
		t.Fatalf("expected 0 artifacts after delete, got %d", n)
	}
}
