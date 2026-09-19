package hls

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

var (
	segmentNamePattern = regexp.MustCompile(`^(\d{3}\.ts|segment_\d{5,}\.ts)$`)
)

func GetStatus(c *gin.Context) {
	dep := dependency.FromContext(c)
	fileID, ok := parsePlayableFileID(c, dep)
	if !ok {
		return
	}

	if !fileExists(c, dep, fileID) {
		notFound(c, "source file not found")
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

	if !authorizeFile(c, dep, fileID, false) {
		return
	}
	playURL, err := requestPlaybackURL(c, dep, fileID)
	if err != nil {
		internalError(c, "failed to sign playback", err)
		return
	}
	diskAvailable := false
	if _, err := os.Stat(filepath.Join(artifact.StoragePath, "index.m3u8")); err == nil {
		diskAvailable = true
	}

	c.JSON(http.StatusOK, serializer.Response{Code: 0, Msg: "ok", Data: gin.H{
		"file_id":        fileID,
		"has_hls":        true,
		"play_url":       playURL,
		"disk_available": diskAvailable,
		"artifact": gin.H{

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
		notFound(c, "source file not found")
		return
	}

	if !authorizeFile(c, dep, fileID, true) {
		return
	}
	file, err := dep.DBClient().File.Get(c, fileID)
	if err != nil {
		if ent.IsNotFound(err) {
			notFound(c, "source file not found")
			return
		}
		internalError(c, "failed to query source file", err)
		return
	}

	tx, err := dep.DBClient().Tx(c)
	if err != nil {
		internalError(c, "failed to create transaction", err)
		return
	}

	storagePath, totalSize, err := inventory.DeleteHLSArtifact(c, tx.Client(), fileID)
	if err != nil {
		_ = tx.Rollback()
		internalError(c, "failed to delete hls artifact", err)
		return
	}

	if storagePath == "" {
		_ = tx.Rollback()
		notFound(c, "hls artifact not found")
		return
	}

	if err := tx.Commit(); err != nil {
		internalError(c, "failed to commit hls delete transaction", err)
		return
	}

	if totalSize != 0 {
		diff := inventory.StorageDiff{file.OwnerID: -totalSize}
		if err := dep.UserClient().ApplyStorageDiff(c, diff); err != nil {
			dep.Logger().Error("Failed to apply hls storage diff", "file_id", fileID, "owner_id", file.OwnerID, "diff", -totalSize, "error", err)
		}
	}

	cleanedStoragePath := filepath.Clean(strings.TrimSpace(storagePath))
	if !inventory.IsAllowedHLSArtifactPath(cleanedStoragePath) {
		internalError(c, "invalid hls artifact dir", fmt.Errorf("hls artifact dir %q escapes allowed prefixes", storagePath))
		return
	}

	if err := os.RemoveAll(cleanedStoragePath); err != nil && !os.IsNotExist(err) {
		internalError(c, "failed to remove hls artifact dir", err)
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
		notFound(c, "source file not found")
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

	// 已登录所有者可直接播放；公开客户端必须使用签名链接。
	if c.Request.URL.Query().Get("sign") == "" {
		if !authorizeFile(c, dep, fileID, false) {
			return
		}
		signed, err := requestPlaybackURL(c, dep, fileID)
		if err != nil {
			internalError(c, "failed to sign playback", err)
			return
		}
		c.Request.URL, err = url.Parse(signed)
		if err != nil {
			internalError(c, "invalid playback url", err)
			return
		}
	}
	if !authorizePlayback(c, dep, fileID, artifact) {
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

	c.Header("Cache-Control", "private, no-store")
	c.Data(http.StatusOK, "application/vnd.apple.mpegurl", []byte(rewritten))
}

func PlaySegment(c *gin.Context) {
	dep := dependency.FromContext(c)
	fileID, ok := parsePlayableFileID(c, dep)
	if !ok {
		return
	}

	if !fileExists(c, dep, fileID) {
		notFound(c, "source file not found")
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

	if !authorizePlayback(c, dep, fileID, artifact) {
		return
	}

	segmentPath := filepath.Join(artifact.StoragePath, segment)
	f, err := os.Open(segmentPath)
	if err != nil {
		if os.IsNotExist(err) {
			notFound(c, "hls segment not found")
			return
		}
		internalError(c, "failed to read hls segment", err)
		return
	}

	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		notFound(c, "segment not found")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Type", "video/mp2t")
	http.ServeContent(c.Writer, c.Request, segment, stat.ModTime(), f)
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

	until, err := strconv.ParseInt(c.Request.URL.Query().Get("until"), 10, 64)
	if err != nil {
		return "", err
	}
	expireAt := time.Unix(until, 0)
	q := c.Request.URL.Query()
	q.Del("sign")

	for i := range lines {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if !isValidSegment(line) {
			return "", fmt.Errorf("invalid segment name in m3u8: %s", line)
		}

		signed, err := signPlaybackURI(c, dep.GeneralAuth(), fmt.Sprintf("/api/v4/hls/%d/play/%s?%s", fileID, line, q.Encode()), &expireAt)
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
