package inventory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/ent/metadata"
	"github.com/cloudreve/Cloudreve/v4/ent/schema"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/samber/lo"
)

const (
	HLSAvailableMetadataKey   = "hls:available"
	HLSAvailableMetadataValue = "1"

	MetadataRestoreURIKey = "sys:restore_uri"
	HLSArtifactTempDir    = "cloudreve-hls"
)

type HLSReconcileReport struct {
	ArtifactMissingIndex   int
	OrphanDirsFound        int
	OrphanDirsDeleted      int
	OrphanBytesReclaimable int64
	OrphanBytesReclaimed   int64
	Errors                 int
}

func IsAllowedHLSArtifactPath(storagePath string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(storagePath))
	if cleaned == "" || cleaned == "." || cleaned == string(os.PathSeparator) || !filepath.IsAbs(cleaned) {
		return false
	}

	for _, prefix := range allowedHLSPrefixes() {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+string(os.PathSeparator)) {
			return true
		}
	}

	return false
}

func RemoveHLSArtifactDir(storagePath string) error {
	cleaned := filepath.Clean(strings.TrimSpace(storagePath))
	if !IsAllowedHLSArtifactPath(cleaned) {
		return fmt.Errorf("hls artifact dir %q escapes allowed prefixes", storagePath)
	}

	if err := os.RemoveAll(cleaned); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove hls artifact dir %q: %w", cleaned, err)
	}

	return nil
}

func CollectHLSArtifactPaths(ctx context.Context, client *ent.Client, fileIDs []int) ([]string, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}

	artifacts, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileIDIn(fileIDs...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query hls artifacts of files %v: %w", fileIDs, err)
	}

	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		storagePath := filepath.Clean(strings.TrimSpace(artifact.StoragePath))
		if storagePath == "" {
			continue
		}

		if !IsAllowedHLSArtifactPath(storagePath) {
			return nil, fmt.Errorf("invalid hls artifact dir %q", artifact.StoragePath)
		}

		paths = append(paths, storagePath)
	}

	return lo.Uniq(paths), nil
}

func UpsertHLSArtifact(ctx context.Context, client *ent.Client, fileID int, ownerID int, outputDir string, segmentCount int, totalSize int64, codec string) (oldStoragePath string, storageDiff int64, err error) {
	cleanedOutputDir := filepath.Clean(strings.TrimSpace(outputDir))
	if !IsAllowedHLSArtifactPath(cleanedOutputDir) {
		return "", 0, fmt.Errorf("invalid hls artifact dir %q for file %d owner %d", outputDir, fileID, ownerID)
	}

	if segmentCount < 0 {
		return "", 0, fmt.Errorf("invalid segment count %d for file %d", segmentCount, fileID)
	}
	if totalSize < 0 {
		return "", 0, fmt.Errorf("invalid total size %d for file %d", totalSize, fileID)
	}

	var oldTotalSize int64
	oldArtifact, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return "", 0, fmt.Errorf("failed to query hls artifact of file %d: %w", fileID, err)
	}
	if oldArtifact != nil {
		oldStoragePath = oldArtifact.StoragePath
		oldTotalSize = oldArtifact.TotalSize
	}

	if err := client.HLSArtifact.Create().
		SetSourceFileID(fileID).
		SetStoragePath(cleanedOutputDir).
		SetSegmentCount(segmentCount).
		SetTotalSize(totalSize).
		SetCodec(codec).
		OnConflictColumns(hlsartifact.FieldSourceFileID).
		UpdateStoragePath().
		UpdateSegmentCount().
		UpdateTotalSize().
		UpdateCodec().
		Exec(ctx); err != nil {
		return "", 0, fmt.Errorf("failed to upsert hls artifact of file %d: %w", fileID, err)
	}

	if err := client.Metadata.Create().
		SetFileID(fileID).
		SetName(HLSAvailableMetadataKey).
		SetValue(HLSAvailableMetadataValue).
		SetIsPublic(true).
		SetNillableDeletedAt(nil).
		OnConflictColumns(metadata.FieldFileID, metadata.FieldName).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return "", 0, fmt.Errorf("failed to upsert hls metadata of file %d: %w", fileID, err)
	}

	return oldStoragePath, totalSize - oldTotalSize, nil
}

func DeleteHLSArtifact(ctx context.Context, client *ent.Client, fileID int) (storagePath string, totalSize int64, err error) {
	hardDeleteCtx := schema.SkipSoftDelete(ctx)

	artifact, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(hardDeleteCtx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", 0, nil
		}

		return "", 0, fmt.Errorf("failed to query hls artifact of file %d: %w", fileID, err)
	}

	if err := client.HLSArtifact.DeleteOneID(artifact.ID).Exec(hardDeleteCtx); err != nil {
		return "", 0, fmt.Errorf("failed to delete hls artifact of file %d: %w", fileID, err)
	}

	if err := ClearHLSAvailableMetadata(ctx, client, fileID); err != nil {
		return "", 0, err
	}

	return artifact.StoragePath, artifact.TotalSize, nil
}

func CascadeDeleteHLSArtifacts(ctx context.Context, client *ent.Client, fileIDs []int, ownerByFileID map[int]int) (StorageDiff, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}

	hardDeleteCtx := schema.SkipSoftDelete(ctx)
	artifacts, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileIDIn(fileIDs...)).All(hardDeleteCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to query hls artifacts of files %v: %w", fileIDs, err)
	}

	diff := make(StorageDiff)
	for _, artifact := range artifacts {
		ownerID, ok := ownerByFileID[artifact.SourceFileID]
		if !ok {
			continue
		}

		diff[ownerID] -= artifact.TotalSize
	}

	if _, err := client.HLSArtifact.Delete().Where(hlsartifact.SourceFileIDIn(fileIDs...)).Exec(hardDeleteCtx); err != nil {
		return nil, fmt.Errorf("failed to delete hls artifacts of files %v: %w", fileIDs, err)
	}

	if err := ClearHLSAvailableMetadata(ctx, client, fileIDs...); err != nil {
		return nil, err
	}

	return diff, nil
}

func ClearHLSAvailableMetadata(ctx context.Context, client *ent.Client, fileIDs ...int) error {
	if len(fileIDs) == 0 {
		return nil
	}

	if _, err := client.Metadata.Delete().
		Where(metadata.FileIDIn(fileIDs...), metadata.Name(HLSAvailableMetadataKey)).
		Exec(schema.SkipSoftDelete(ctx)); err != nil {
		return fmt.Errorf("failed to clear hls metadata of files %v: %w", fileIDs, err)
	}

	return nil
}

func IsFileSoftDeleted(ctx context.Context, client *ent.Client, fileID int) (bool, error) {
	count, err := client.Metadata.Query().
		Where(metadata.FileID(fileID), metadata.Name(MetadataRestoreURIKey)).
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to query restore metadata of file %d: %w", fileID, err)
	}

	return count > 0, nil
}

func SyncHLSAvailabilityByArtifact(ctx context.Context, client *ent.Client, fileID int) error {
	softDeleted, err := IsFileSoftDeleted(ctx, client, fileID)
	if err != nil {
		return err
	}

	if softDeleted {
		return ClearHLSAvailableMetadata(ctx, client, fileID)
	}

	artifact, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ClearHLSAvailableMetadata(ctx, client, fileID)
		}

		return fmt.Errorf("failed to query hls artifact of file %d: %w", fileID, err)
	}

	indexPath := filepath.Join(artifact.StoragePath, "index.m3u8")
	if _, err := os.Stat(indexPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ClearHLSAvailableMetadata(ctx, client, fileID)
		}

		return fmt.Errorf("failed to stat hls index %q: %w", indexPath, err)
	}

	if err := client.Metadata.Create().
		SetFileID(fileID).
		SetName(HLSAvailableMetadataKey).
		SetValue(HLSAvailableMetadataValue).
		SetIsPublic(true).
		SetNillableDeletedAt(nil).
		OnConflictColumns(metadata.FieldFileID, metadata.FieldName).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return fmt.Errorf("failed to upsert hls metadata of file %d: %w", fileID, err)
	}

	return nil
}

func ReconcileHLSArtifacts(ctx context.Context, client *ent.Client, dryRun bool, minAge time.Duration) (*HLSReconcileReport, error) {
	report := &HLSReconcileReport{}
	if minAge <= 0 {
		minAge = 30 * time.Minute
	}

	artifacts, err := client.HLSArtifact.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query hls artifacts: %w", err)
	}

	referenced := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		cleaned := filepath.Clean(strings.TrimSpace(artifact.StoragePath))
		if cleaned == "" {
			continue
		}

		if !IsAllowedHLSArtifactPath(cleaned) {
			report.Errors++
			continue
		}

		referenced[cleaned] = struct{}{}
		indexPath := filepath.Join(cleaned, "index.m3u8")
		if _, err := os.Stat(indexPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				report.ArtifactMissingIndex++
				if !dryRun {
					if clearErr := ClearHLSAvailableMetadata(ctx, client, artifact.SourceFileID); clearErr != nil {
						report.Errors++
					}
				}
				continue
			}

			report.Errors++
		}
	}

	for _, root := range allowedHLSPrefixes() {
		if _, err := os.Stat(root); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			report.Errors++
			continue
		}

		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				report.Errors++
				return nil
			}

			if !d.IsDir() {
				return nil
			}

			cleaned := filepath.Clean(path)
			if cleaned == root {
				return nil
			}

			if _, ok := referenced[cleaned]; ok {
				return filepath.SkipDir
			}

			rel, relErr := filepath.Rel(root, cleaned)
			if relErr != nil {
				report.Errors++
				return nil
			}

			parts := strings.Split(rel, string(os.PathSeparator))
			if len(parts) < 2 {
				return nil
			}

			info, infoErr := d.Info()
			if infoErr != nil {
				report.Errors++
				return nil
			}

			if time.Since(info.ModTime()) < minAge {
				return nil
			}

			size, sizeErr := dirSize(cleaned)
			if sizeErr != nil {
				report.Errors++
			}

			report.OrphanDirsFound++
			report.OrphanBytesReclaimable += size

			if dryRun {
				return filepath.SkipDir
			}

			if rmErr := RemoveHLSArtifactDir(cleaned); rmErr != nil {
				report.Errors++
				return filepath.SkipDir
			}

			report.OrphanDirsDeleted++
			report.OrphanBytesReclaimed += size
			return filepath.SkipDir
		})

		if walkErr != nil {
			report.Errors++
		}
	}

	return report, nil
}

func allowedHLSPrefixes() []string {
	return []string{
		filepath.Clean(util.DataPath("hls")),
		filepath.Clean(filepath.Join(os.TempDir(), HLSArtifactTempDir)),
	}
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		total += info.Size()
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("failed to calculate size of %q: %w", path, err)
	}

	return total, nil
}
