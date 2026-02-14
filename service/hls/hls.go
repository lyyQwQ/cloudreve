package hls

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/ent/metadata"
	"github.com/cloudreve/Cloudreve/v4/ent/schema"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

const (
	hlsAvailableMetadataKey = "hls:available"
)

var (
	segmentNamePattern = regexp.MustCompile(`^(\d{3}\.ts|segment_\d{5}\.ts)$`)
)

func GetStatus(c *gin.Context) {
	dep := dependency.FromContext(c)
	fileID, ok := parsePlayableFileID(c, dep)
	if !ok {
		return
	}

	if !fileExists(c, dep, fileID) {
		legacyStubResponse(c)
		return
	}

	artifact, err := dep.DBClient().HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(c)
	if err != nil {
		if ent.IsNotFound(err) {
			notFound(c, "hls artifact not found")
			return
		}
		internalError(c, "failed to query hls artifact", err)
		return
	}

	c.JSON(http.StatusOK, serializer.Response{Code: 0, Msg: "ok", Data: gin.H{
		"file_id": fileID,
		"has_hls": true,
		"artifact": gin.H{
			"storage_path":  artifact.StoragePath,
			"segment_count": artifact.SegmentCount,
			"total_size":    artifact.TotalSize,
			"codec":         artifact.Codec,
		},
	}})
}

func Delete(c *gin.Context) {
	dep := dependency.FromContext(c)
	fileID, ok := parsePlayableFileID(c, dep)
	if !ok {
		return
	}

	if !fileExists(c, dep, fileID) {
		legacyStubResponse(c)
		return
	}

	artifact, err := dep.DBClient().HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(c)
	if err != nil {
		if ent.IsNotFound(err) {
			notFound(c, "hls artifact not found")
			return
		}
		internalError(c, "failed to query hls artifact", err)
		return
	}

	if err := os.RemoveAll(artifact.StoragePath); err != nil {
		internalError(c, "failed to remove hls artifact dir", err)
		return
	}

	if err := dep.DBClient().HLSArtifact.DeleteOneID(artifact.ID).Exec(c); err != nil {
		internalError(c, "failed to delete hls artifact", err)
		return
	}

	if _, err := dep.DBClient().Metadata.Delete().
		Where(metadata.FileID(fileID), metadata.Name(hlsAvailableMetadataKey)).
		Exec(schema.SkipSoftDelete(c)); err != nil {
		internalError(c, "failed to clear hls metadata", err)
		return
	}

	c.JSON(http.StatusOK, serializer.Response{Code: 0, Msg: "ok", Data: gin.H{"deleted": true}})
}

func PlayIndex(c *gin.Context) {
	dep := dependency.FromContext(c)
	fileID, ok := parsePlayableFileID(c, dep)
	if !ok {
		return
	}

	if !fileExists(c, dep, fileID) {
		legacyStubResponse(c)
		return
	}

	artifact, err := dep.DBClient().HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(c)
	if err != nil {
		if ent.IsNotFound(err) {
			notFound(c, "hls artifact not found")
			return
		}
		internalError(c, "failed to query hls artifact", err)
		return
	}

	playlistPath := filepath.Join(artifact.StoragePath, "index.m3u8")
	raw, err := os.ReadFile(playlistPath)
	if err != nil {
		if os.IsNotExist(err) {
			notFound(c, "hls index not found")
			return
		}
		internalError(c, "failed to read hls index", err)
		return
	}

	rewritten, err := rewritePlaylistWithSignedSegments(c, dep, fileID, string(raw))
	if err != nil {
		internalError(c, "failed to rewrite hls index", err)
		return
	}

	c.Data(http.StatusOK, "application/vnd.apple.mpegurl", []byte(rewritten))
}

func PlaySegment(c *gin.Context) {
	dep := dependency.FromContext(c)
	fileID, ok := parsePlayableFileID(c, dep)
	if !ok {
		return
	}

	if !fileExists(c, dep, fileID) {
		legacyStubResponse(c)
		return
	}

	segment := c.Param("segment")
	if !isValidSegment(segment) || hasEncodedSeparator(c.Request.URL.EscapedPath()) {
		badRequest(c, "invalid segment", nil)
		return
	}

	artifact, err := dep.DBClient().HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(c)
	if err != nil {
		if ent.IsNotFound(err) {
			notFound(c, "hls artifact not found")
			return
		}
		internalError(c, "failed to query hls artifact", err)
		return
	}

	if err := auth.CheckURI(c, dep.GeneralAuth(), c.Request.URL); err != nil {
		c.JSON(http.StatusForbidden, serializer.Response{Code: serializer.CodeCredentialInvalid, Msg: "invalid sign", Error: err.Error()})
		return
	}

	segmentPath := filepath.Join(artifact.StoragePath, segment)
	raw, err := os.ReadFile(segmentPath)
	if err != nil {
		if os.IsNotExist(err) {
			notFound(c, "hls segment not found")
			return
		}
		internalError(c, "failed to read hls segment", err)
		return
	}

	c.Data(http.StatusOK, "video/mp2t", raw)
}

func parsePlayableFileID(c *gin.Context, dep dependency.Dep) (int, bool) {
	raw := strings.TrimSpace(c.Param("fileId"))
	if raw == "" {
		badRequest(c, "invalid file id", fmt.Errorf("empty file id"))
		return 0, false
	}

	if fileID, err := strconv.Atoi(raw); err == nil {
		if fileID <= 0 {
			badRequest(c, "invalid file id", fmt.Errorf("non-positive file id"))
			return 0, false
		}
		return fileID, true
	}

	decoded, err := dep.HashIDEncoder().Decode(raw, hashid.FileID)
	if err != nil || decoded <= 0 {
		badRequest(c, "invalid file id", err)
		return 0, false
	}

	return decoded, true
}

func rewritePlaylistWithSignedSegments(c *gin.Context, dep dependency.Dep, fileID int, playlist string) (string, error) {
	lines := strings.Split(playlist, "\n")

	expire := dep.SettingProvider().EntityUrlValidDuration(c)
	var expireAt *time.Time
	if expire > 0 {
		t := time.Now().Add(expire)
		expireAt = &t
	}

	for i := range lines {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if !isValidSegment(line) {
			return "", fmt.Errorf("invalid segment name in m3u8: %s", line)
		}

		signed, err := auth.SignURI(c, dep.GeneralAuth(), fmt.Sprintf("/api/v4/hls/%d/play/%s", fileID, line), expireAt)
		if err != nil {
			return "", err
		}

		lines[i] = signed.String()
	}

	return strings.Join(lines, "\n"), nil
}

func isValidSegment(segment string) bool {
	if strings.Contains(segment, "/") || strings.Contains(segment, "\\") {
		return false
	}

	return segmentNamePattern.MatchString(segment)
}

func hasEncodedSeparator(escapedPath string) bool {
	escapedPath = strings.ToLower(escapedPath)
	return strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c")
}

func badRequest(c *gin.Context, msg string, err error) {
	resp := serializer.Response{Code: serializer.CodeParamErr, Msg: msg}
	if err != nil {
		resp.Error = err.Error()
	}
	c.JSON(http.StatusBadRequest, resp)
}

func notFound(c *gin.Context, msg string) {
	c.JSON(http.StatusNotFound, serializer.Response{Code: serializer.CodeNotFound, Msg: msg})
}

func internalError(c *gin.Context, msg string, err error) {
	resp := serializer.Response{Code: serializer.CodeInternalSetting, Msg: msg}
	if err != nil {
		resp.Error = err.Error()
	}
	c.JSON(http.StatusInternalServerError, resp)
}

func fileExists(c *gin.Context, dep dependency.Dep, fileID int) bool {
	_, err := dep.DBClient().File.Get(c, fileID)
	return err == nil
}

func legacyStubResponse(c *gin.Context) {
	c.JSON(http.StatusOK, serializer.Response{Code: 0, Msg: "ok", Data: gin.H{"legacy": true}})
}
