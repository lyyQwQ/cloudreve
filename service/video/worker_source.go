package video

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent/entity"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/ffmpegworker"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/gin-gonic/gin"
)

func ServeWorkerSource(c *gin.Context) {
	taskID, err := strconv.Atoi(c.Param("taskId"))
	if err != nil || taskID <= 0 {
		c.String(http.StatusBadRequest, "invalid task id")
		return
	}

	dep := dependency.FromContext(c)
	claims, err := ffmpegworker.VerifySourceURL(taskID, c.Request.URL.Query(), dep.SettingProvider().SecretKey(c), timeNow())
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, ffmpegworker.ErrExpiredToken) {
			status = http.StatusGone
		}
		c.String(status, "invalid worker source url")
		return
	}

	taskModel, err := dep.TaskClient().GetTaskByID(c, taskID)
	if err != nil {
		c.String(http.StatusNotFound, "task not found")
		return
	}
	state, err := queue.ParseVideoTaskState(taskModel.PrivateState)
	if err != nil || state.FileID != claims.FileID {
		c.String(http.StatusForbidden, "invalid worker source task")
		return
	}

	loadCtx := c.Request.Context()
	loadCtx = contextWithLoadEntity(loadCtx)
	fileModel, err := dep.FileClient().GetByID(loadCtx, claims.FileID)
	if err != nil || fileModel.PrimaryEntity != claims.EntityID {
		c.String(http.StatusNotFound, "file not found")
		return
	}

	entityModel, err := dep.DBClient().Entity.Query().Where(entity.ID(claims.EntityID)).Only(c)
	if err != nil {
		c.String(http.StatusNotFound, "entity not found")
		return
	}
	if entityModel.Source == "" {
		c.String(http.StatusNotFound, "entity source not found")
		return
	}

	f, err := os.Open(entityModel.Source)
	if err != nil {
		c.String(http.StatusNotFound, "entity data not found")
		return
	}
	defer f.Close()

	name := fileModel.Name
	if name == "" {
		name = "source"
	}
	c.Header("Accept-Ranges", "bytes")
	http.ServeContent(c.Writer, c.Request, name, entityModel.UpdatedAt, f)
}

func ServeWorkerSubtitle(c *gin.Context) {
	taskID, err := strconv.Atoi(c.Param("taskId"))
	if err != nil || taskID <= 0 {
		c.String(http.StatusBadRequest, "invalid task id")
		return
	}

	dep := dependency.FromContext(c)
	claims, err := ffmpegworker.VerifySubtitleURL(taskID, c.Request.URL.Query(), dep.SettingProvider().SecretKey(c), timeNow())
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, ffmpegworker.ErrExpiredToken) {
			status = http.StatusGone
		}
		c.String(status, "invalid worker subtitle url")
		return
	}
	if !queue.IsSupportedExternalSubtitleName(claims.SubtitleName) || filepath.Base(claims.SubtitleName) != claims.SubtitleName || strings.Contains(claims.SubtitleName, "/") || strings.Contains(claims.SubtitleName, "\\") {
		c.String(http.StatusForbidden, "invalid worker subtitle")
		return
	}

	taskModel, err := dep.TaskClient().GetTaskByID(c, taskID)
	if err != nil {
		c.String(http.StatusNotFound, "task not found")
		return
	}
	state, err := queue.ParseVideoTaskState(taskModel.PrivateState)
	if err != nil || state.FileID != claims.FileID || state.Subtitle == nil || !strings.EqualFold(strings.TrimSpace(state.Subtitle.Mode), queue.VideoSubtitleModeExternal) || strings.TrimSpace(state.Subtitle.ExternalName) != claims.SubtitleName {
		c.String(http.StatusForbidden, "invalid worker subtitle task")
		return
	}

	loadCtx := c.Request.Context()
	loadCtx = contextWithLoadEntity(loadCtx)
	fileModel, err := dep.FileClient().GetByID(loadCtx, claims.FileID)
	if err != nil || fileModel.PrimaryEntity != claims.EntityID {
		c.String(http.StatusNotFound, "file not found")
		return
	}

	entityModel, err := dep.DBClient().Entity.Query().Where(entity.ID(claims.EntityID)).Only(c)
	if err != nil {
		c.String(http.StatusNotFound, "entity not found")
		return
	}
	if entityModel.Source == "" {
		c.String(http.StatusNotFound, "entity source not found")
		return
	}

	subtitlePath, err := queue.ResolveExternalSubtitlePath(entityModel.Source, claims.SubtitleName)
	if err != nil {
		c.String(http.StatusNotFound, "subtitle not found")
		return
	}
	f, err := os.Open(subtitlePath)
	if err != nil {
		c.String(http.StatusNotFound, "subtitle data not found")
		return
	}
	defer f.Close()

	c.Header("Accept-Ranges", "bytes")
	http.ServeContent(c.Writer, c.Request, claims.SubtitleName, entityModel.UpdatedAt, f)
}

var timeNow = func() time.Time {
	return time.Now()
}

func contextWithLoadEntity(ctx context.Context) context.Context {
	return context.WithValue(ctx, inventory.LoadFileEntity{}, true)
}
