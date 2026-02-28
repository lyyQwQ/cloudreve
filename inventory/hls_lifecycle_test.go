package inventory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/enttest"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/ent/metadata"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
)

func TestUpsertHLSArtifact_Create(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	file := createHLSLifecycleFixture(t, ctx, client)
	outputDir := newAllowedHLSArtifactDir(t, "upsert-create-*")
	totalSize := int64(4096)

	oldStoragePath, storageDiff, err := UpsertHLSArtifact(ctx, client, file.ID, file.OwnerID, outputDir, 3, totalSize, "h264/aac")
	if err != nil {
		t.Fatalf("UpsertHLSArtifact create failed: %v", err)
	}
	if oldStoragePath != "" {
		t.Fatalf("unexpected oldStoragePath, got=%q want=%q", oldStoragePath, "")
	}
	if storageDiff != totalSize {
		t.Fatalf("unexpected storageDiff, got=%d want=%d", storageDiff, totalSize)
	}

	artifact, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileIDEQ(file.ID)).Only(ctx)
	if err != nil {
		t.Fatalf("query hls artifact after create: %v", err)
	}
	if artifact.StoragePath != outputDir {
		t.Fatalf("unexpected artifact storage path, got=%q want=%q", artifact.StoragePath, outputDir)
	}

	meta, err := client.Metadata.Query().Where(
		metadata.FileIDEQ(file.ID),
		metadata.NameEQ(HLSAvailableMetadataKey),
	).Only(ctx)
	if err != nil {
		t.Fatalf("query hls metadata after create: %v", err)
	}
	if meta.Value != HLSAvailableMetadataValue {
		t.Fatalf("unexpected hls metadata value, got=%q want=%q", meta.Value, HLSAvailableMetadataValue)
	}
}

func TestUpsertHLSArtifact_Update(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	file := createHLSLifecycleFixture(t, ctx, client)

	oldOutputDir := newAllowedHLSArtifactDir(t, "upsert-update-old-*")
	oldSize := int64(1000)
	if _, _, err := UpsertHLSArtifact(ctx, client, file.ID, file.OwnerID, oldOutputDir, 2, oldSize, "h264/aac"); err != nil {
		t.Fatalf("initial UpsertHLSArtifact failed: %v", err)
	}

	newOutputDir := newAllowedHLSArtifactDir(t, "upsert-update-new-*")
	newSize := int64(2500)
	oldStoragePath, storageDiff, err := UpsertHLSArtifact(ctx, client, file.ID, file.OwnerID, newOutputDir, 5, newSize, "h265/aac")
	if err != nil {
		t.Fatalf("second UpsertHLSArtifact failed: %v", err)
	}
	if oldStoragePath != oldOutputDir {
		t.Fatalf("unexpected oldStoragePath, got=%q want=%q", oldStoragePath, oldOutputDir)
	}
	if storageDiff != newSize-oldSize {
		t.Fatalf("unexpected storageDiff, got=%d want=%d", storageDiff, newSize-oldSize)
	}

	artifact, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileIDEQ(file.ID)).Only(ctx)
	if err != nil {
		t.Fatalf("query hls artifact after update: %v", err)
	}
	if artifact.StoragePath != newOutputDir {
		t.Fatalf("artifact storage path not updated, got=%q want=%q", artifact.StoragePath, newOutputDir)
	}
	if artifact.TotalSize != newSize {
		t.Fatalf("artifact total size not updated, got=%d want=%d", artifact.TotalSize, newSize)
	}
}

func TestUpsertHLSArtifact_InvalidPath(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	file := createHLSLifecycleFixture(t, ctx, client)
	invalidDir := t.TempDir()

	if _, _, err := UpsertHLSArtifact(ctx, client, file.ID, file.OwnerID, invalidDir, 1, 100, "h264/aac"); err == nil {
		t.Fatalf("expected error for invalid output dir %q, got nil", invalidDir)
	}
}

func TestUpsertHLSArtifact_NegativeParams(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	file := createHLSLifecycleFixture(t, ctx, client)
	outputDir := newAllowedHLSArtifactDir(t, "upsert-negative-*")

	if _, _, err := UpsertHLSArtifact(ctx, client, file.ID, file.OwnerID, outputDir, -1, 100, "h264/aac"); err == nil {
		t.Fatalf("expected error for negative segmentCount, got nil")
	}

	if _, _, err := UpsertHLSArtifact(ctx, client, file.ID, file.OwnerID, outputDir, 1, -1, "h264/aac"); err == nil {
		t.Fatalf("expected error for negative totalSize, got nil")
	}
}

func TestDeleteHLSArtifact_Existing(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	file := createHLSLifecycleFixture(t, ctx, client)
	outputDir := newAllowedHLSArtifactDir(t, "delete-existing-*")
	totalSize := int64(2048)

	if _, _, err := UpsertHLSArtifact(ctx, client, file.ID, file.OwnerID, outputDir, 4, totalSize, "h264/aac"); err != nil {
		t.Fatalf("prepare UpsertHLSArtifact failed: %v", err)
	}

	storagePath, deletedSize, err := DeleteHLSArtifact(ctx, client, file.ID)
	if err != nil {
		t.Fatalf("DeleteHLSArtifact failed: %v", err)
	}
	if storagePath != outputDir {
		t.Fatalf("unexpected storagePath, got=%q want=%q", storagePath, outputDir)
	}
	if deletedSize != totalSize {
		t.Fatalf("unexpected deleted size, got=%d want=%d", deletedSize, totalSize)
	}

	if _, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileIDEQ(file.ID)).Only(ctx); !ent.IsNotFound(err) {
		t.Fatalf("artifact should be deleted, got err=%v", err)
	}

	if _, err := client.Metadata.Query().Where(
		metadata.FileIDEQ(file.ID),
		metadata.NameEQ(HLSAvailableMetadataKey),
	).Only(ctx); !ent.IsNotFound(err) {
		t.Fatalf("hls metadata should be deleted, got err=%v", err)
	}
}

func TestDeleteHLSArtifact_Idempotent(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	file := createHLSLifecycleFixture(t, ctx, client)

	storagePath, deletedSize, err := DeleteHLSArtifact(ctx, client, file.ID)
	if err != nil {
		t.Fatalf("DeleteHLSArtifact failed: %v", err)
	}
	if storagePath != "" {
		t.Fatalf("unexpected storagePath on idempotent delete, got=%q want=%q", storagePath, "")
	}
	if deletedSize != 0 {
		t.Fatalf("unexpected deleted size on idempotent delete, got=%d want=%d", deletedSize, 0)
	}
}

func TestCascadeDeleteHLSArtifacts_Batch(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	file1 := createHLSLifecycleFixtureWithSuffix(t, ctx, client, "cascade-a")
	file2 := createHLSLifecycleFixtureWithSuffix(t, ctx, client, "cascade-b")

	outputDir1 := newAllowedHLSArtifactDir(t, "cascade-delete-a-*")
	outputDir2 := newAllowedHLSArtifactDir(t, "cascade-delete-b-*")
	size1 := int64(101)
	size2 := int64(202)

	if _, _, err := UpsertHLSArtifact(ctx, client, file1.ID, file1.OwnerID, outputDir1, 3, size1, "h264/aac"); err != nil {
		t.Fatalf("prepare first hls artifact failed: %v", err)
	}
	if _, _, err := UpsertHLSArtifact(ctx, client, file2.ID, file2.OwnerID, outputDir2, 5, size2, "h265/aac"); err != nil {
		t.Fatalf("prepare second hls artifact failed: %v", err)
	}

	diff, err := CascadeDeleteHLSArtifacts(ctx, client, []int{file1.ID, file2.ID}, map[int]int{
		file1.ID: file1.OwnerID,
		file2.ID: file2.OwnerID,
	})
	if err != nil {
		t.Fatalf("CascadeDeleteHLSArtifacts failed: %v", err)
	}

	artifactCount, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileIDIn(file1.ID, file2.ID)).Count(ctx)
	if err != nil {
		t.Fatalf("query hls artifacts after cascade delete: %v", err)
	}
	if artifactCount != 0 {
		t.Fatalf("hls artifacts should be deleted, got count=%d want=0", artifactCount)
	}

	metadataCount, err := client.Metadata.Query().Where(
		metadata.FileIDIn(file1.ID, file2.ID),
		metadata.NameEQ(HLSAvailableMetadataKey),
	).Count(ctx)
	if err != nil {
		t.Fatalf("query hls metadata after cascade delete: %v", err)
	}
	if metadataCount != 0 {
		t.Fatalf("hls metadata should be deleted, got count=%d want=0", metadataCount)
	}

	if len(diff) != 2 {
		t.Fatalf("unexpected storage diff size, got=%d want=2", len(diff))
	}
	if diff[file1.OwnerID] != -size1 {
		t.Fatalf("unexpected storage diff for owner %d, got=%d want=%d", file1.OwnerID, diff[file1.OwnerID], -size1)
	}
	if diff[file2.OwnerID] != -size2 {
		t.Fatalf("unexpected storage diff for owner %d, got=%d want=%d", file2.OwnerID, diff[file2.OwnerID], -size2)
	}
}

func TestCascadeDeleteHLSArtifacts_EmptyInput(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	diff, err := CascadeDeleteHLSArtifacts(ctx, client, []int{}, nil)
	if err != nil {
		t.Fatalf("CascadeDeleteHLSArtifacts with empty input should not fail: %v", err)
	}
	if diff != nil {
		t.Fatalf("CascadeDeleteHLSArtifacts with empty input should return nil diff, got=%v", diff)
	}
}

func createHLSLifecycleFixture(t *testing.T, ctx context.Context, client *ent.Client) *ent.File {
	return createHLSLifecycleFixtureWithSuffix(t, ctx, client, "default")
}

func createHLSLifecycleFixtureWithSuffix(t *testing.T, ctx context.Context, client *ent.Client, suffix string) *ent.File {
	t.Helper()

	group, err := client.Group.Create().
		SetName("test-" + suffix).
		SetMaxStorage(1_000_000_000).
		SetPermissions(&boolset.BooleanSet{}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	user, err := client.User.Create().
		SetEmail("test-" + suffix + "@test.com").
		SetNick("test-" + suffix).
		SetStorage(0).
		SetGroupUsers(group.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	policy, err := client.StoragePolicy.Create().
		SetName("test-" + suffix).
		SetType(types.PolicyTypeLocal).
		SetSettings(&types.PolicySetting{}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}

	entity, err := client.Entity.Create().
		SetType(int(types.EntityTypeVersion)).
		SetSource("test-" + suffix).
		SetReferenceCount(1).
		SetSize(100).
		SetStoragePolicyEntities(policy.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}

	file, err := client.File.Create().
		SetName("test-" + suffix + ".mp4").
		SetOwnerID(user.ID).
		SetType(0).
		SetPrimaryEntity(entity.ID).
		AddEntities(entity).
		Save(ctx)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}

	return file
}

func newAllowedHLSArtifactDir(t *testing.T, pattern string) string {
	t.Helper()

	base := filepath.Join(os.TempDir(), "cloudreve-hls")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("create allowed hls base dir: %v", err)
	}

	dir, err := os.MkdirTemp(base, pattern)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}
