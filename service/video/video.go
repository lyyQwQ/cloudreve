package video

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

type fileIDRaw string

type fileIDRequest struct {
	FileID fileIDRaw `json:"file_id" form:"file_id" binding:"required"`
}

type subtitleBurnRequest struct {
	FileID   fileIDRaw                        `json:"file_id" binding:"required"`
	Subtitle *subtitleSelectionRequestPayload `json:"subtitle"`
}

type subtitleSelectionRequestPayload struct {
	Mode          string `json:"mode"`
	ExternalName  string `json:"external_name"`
	EmbeddedIndex *int   `json:"embedded_index"`
}

func GetInfo(c *gin.Context) {
	var req fileIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "bad request", Error: err.Error()})
		return
	}

	fileID, err := resolveFileID(dependency.FromContext(c), string(req.FileID))
	if err != nil {
		c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "bad request", Error: err.Error()})
		return
	}

	info, status, resp := getVideoInfo(c, fileID)
	if info == nil {
		c.JSON(status, resp)
		return
	}

	c.JSON(http.StatusOK, serializer.Response{Code: 0, Data: info})
}

func ListSubtitles(c *gin.Context) {
	var req fileIDRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "bad request", Error: err.Error()})
		return
	}

	fileID, err := resolveFileID(dependency.FromContext(c), string(req.FileID))
	if err != nil {
		c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "bad request", Error: err.Error()})
		return
	}

	info, status, resp := getVideoInfo(c, fileID)
	if info == nil {
		c.JSON(status, resp)
		return
	}

	c.JSON(http.StatusOK, serializer.Response{Code: 0, Data: info.Subtitles})
}

func BurnSubtitle(c *gin.Context) {
	createVideoTask(c, queue.VideoSubtitleBurnTaskType)
}

func SliceHLS(c *gin.Context) {
	createVideoTask(c, queue.VideoHLSSliceTaskType)
}

func createVideoTask(c *gin.Context, taskType string) {
	var req fileIDRequest
	var subtitleOption *queue.VideoSubtitleOption

	switch taskType {
	case queue.VideoSubtitleBurnTaskType:
		var burnReq subtitleBurnRequest
		if err := c.ShouldBindJSON(&burnReq); err != nil {
			c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "bad request", Error: err.Error()})
			return
		}
		req.FileID = burnReq.FileID
		option, err := validateSubtitleSelection(burnReq.Subtitle)
		if err != nil {
			c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "bad request", Error: err.Error()})
			return
		}
		subtitleOption = option
	default:
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "bad request", Error: err.Error()})
			return
		}
	}

	dep := dependency.FromContext(c)
	fileID, err := resolveFileID(dep, string(req.FileID))
	if err != nil {
		c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "bad request", Error: err.Error()})
		return
	}

	user := inventory.UserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, serializer.Response{Code: 1, Msg: "unauthorized"})
		return
	}

	exists, err := hasPendingTaskForFile(c, dep, user.ID, taskType, fileID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, serializer.Response{Code: 1, Msg: "internal error", Error: err.Error()})
		return
	}
	if exists {
		c.JSON(http.StatusConflict, serializer.Response{Code: 1, Msg: "task already exists"})
		return
	}

	if taskType == queue.VideoSubtitleBurnTaskType && subtitleOption != nil {
		fileName, exists, err := checkBurnedOutputExists(c, dep, fileID, user.ID, subtitleOption)
		if err != nil {
			logging.FromContext(c).Warning("Video task dedup check failed: %v", err)
		} else if exists {
			c.JSON(http.StatusConflict, serializer.Response{Code: 1, Msg: "burned output already exists", Data: gin.H{"file_name": fileName}})
			return
		}
	}

	var tk queue.Task
	switch taskType {
	case queue.VideoSubtitleBurnTaskType:
		t, err := queue.NewVideoSubtitleBurnTask(c, fileID, user, subtitleOption)
		if err != nil {
			c.JSON(http.StatusInternalServerError, serializer.Response{Code: 1, Msg: "failed to create task", Error: err.Error()})
			return
		}
		tk = t
	case queue.VideoHLSSliceTaskType:
		if status, resp, err := precheckHLSCodec(c, fileID); err != nil {
			c.JSON(status, resp)
			return
		}

		t, err := queue.NewVideoHLSSliceTask(c, fileID, user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, serializer.Response{Code: 1, Msg: "failed to create task", Error: err.Error()})
			return
		}
		tk = t
	default:
		c.JSON(http.StatusInternalServerError, serializer.Response{Code: 1, Msg: "unknown task type"})
		return
	}

	if err := dep.VideoProcessQueue(c).QueueTask(c, tk); err != nil {
		c.JSON(http.StatusInternalServerError, serializer.Response{Code: 1, Msg: "failed to queue task", Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, serializer.Response{Code: 0, Data: gin.H{"task_id": tk.ID()}})
}

func validateSubtitleSelection(selection *subtitleSelectionRequestPayload) (*queue.VideoSubtitleOption, error) {
	if selection == nil {
		return nil, nil
	}

	mode := strings.ToLower(strings.TrimSpace(selection.Mode))
	option := &queue.VideoSubtitleOption{Mode: mode}
	externalName := strings.TrimSpace(selection.ExternalName)

	switch mode {
	case queue.VideoSubtitleModeAuto:
		if externalName != "" || selection.EmbeddedIndex != nil {
			return nil, fmt.Errorf("subtitle fields do not match mode auto")
		}
	case queue.VideoSubtitleModeExternal:
		if externalName == "" || selection.EmbeddedIndex != nil {
			return nil, fmt.Errorf("subtitle fields do not match mode external")
		}
		option.ExternalName = externalName
	case queue.VideoSubtitleModeEmbedded:
		if externalName != "" || selection.EmbeddedIndex == nil {
			return nil, fmt.Errorf("subtitle fields do not match mode embedded")
		}
		if *selection.EmbeddedIndex < 0 {
			return nil, fmt.Errorf("subtitle embedded_index must be >= 0")
		}
		idx := *selection.EmbeddedIndex
		option.EmbeddedIndex = &idx
	default:
		return nil, fmt.Errorf("invalid subtitle mode")
	}

	return option, nil
}

func (r *fileIDRaw) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return fmt.Errorf("file_id is required")
	}

	if strings.HasPrefix(trimmed, "\"") {
		var raw string
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		*r = fileIDRaw(strings.TrimSpace(raw))
		return nil
	}

	var numeric int64
	if err := json.Unmarshal(data, &numeric); err != nil {
		return fmt.Errorf("invalid file_id")
	}

	*r = fileIDRaw(strconv.FormatInt(numeric, 10))
	return nil
}

func resolveFileID(dep dependency.Dep, raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("file_id is required")
	}

	if numeric, err := strconv.Atoi(trimmed); err == nil {
		if numeric <= 0 {
			return 0, fmt.Errorf("invalid file_id")
		}
		return numeric, nil
	}

	decoded, err := dep.HashIDEncoder().Decode(trimmed, hashid.FileID)
	if err != nil || decoded <= 0 {
		return 0, fmt.Errorf("invalid file_id")
	}

	return decoded, nil
}

func hasPendingTaskForFile(c *gin.Context, dep dependency.Dep, ownerID int, taskType string, fileID int) (bool, error) {
	models, err := dep.DBClient().Task.Query().
		Where(task.Type(taskType)).
		Where(task.UserTasks(ownerID)).
		Where(task.StatusIn(task.StatusQueued, task.StatusProcessing, task.StatusSuspending)).
		All(c)
	if err != nil {
		return false, err
	}

	for _, m := range models {
		st, err := queue.ParseVideoTaskState(m.PrivateState)
		if err != nil {
			continue
		}
		if st.FileID == fileID {
			return true, nil
		}
	}

	return false, nil
}

func checkBurnedOutputExists(ctx context.Context, dep dependency.Dep, fileID int, ownerID int, subtitleOption *queue.VideoSubtitleOption) (string, bool, error) {
	loadCtx := context.WithValue(ctx, inventory.LoadFileEntity{}, true)
	fileModel, err := dep.FileClient().GetByID(loadCtx, fileID)
	if err != nil {
		return "", false, err
	}

	parent, err := dep.FileClient().GetParentFile(ctx, fileModel, false)
	if err != nil {
		return "", false, err
	}

	burnedFolder, err := dep.FileClient().GetChildFile(ctx, parent, ownerID, "burned", false)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}

	baseName := strings.TrimSpace(fileModel.Name)
	baseName = filepath.Base(baseName)
	stem := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	if stem == "" {
		stem = "video"
	}
	stem = strings.ReplaceAll(stem, "/", "_")
	stem = strings.ReplaceAll(stem, "\\", "_")

	lang := "sub"
	if subtitleOption != nil {
		mode := strings.ToLower(strings.TrimSpace(subtitleOption.Mode))
		if mode == queue.VideoSubtitleModeExternal && strings.TrimSpace(subtitleOption.ExternalName) != "" {
			externalBase := filepath.Base(strings.TrimSpace(subtitleOption.ExternalName))
			ext := filepath.Ext(externalBase)
			base := strings.TrimSuffix(externalBase, ext)
			if lastDot := strings.LastIndex(base, "."); lastDot >= 0 && lastDot+1 < len(base) {
				maybe := strings.TrimSpace(base[lastDot+1:])
				if maybe != "" {
					lang = maybe
				}
			}
		}
	}
	lang = sanitizeLanguage(lang)

	fileName := fmt.Sprintf("%s_%s.mp4", stem, lang)
	fileName = strings.ReplaceAll(fileName, "/", "_")
	fileName = strings.ReplaceAll(fileName, "\\", "_")

	_, err = dep.FileClient().GetChildFile(ctx, burnedFolder, ownerID, fileName, false)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}

	return fileName, true, nil
}

func sanitizeLanguage(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return "sub"
	}
	lang = filepath.Base(lang)
	var b strings.Builder
	b.Grow(len(lang))
	for _, r := range lang {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	cleaned := strings.Trim(b.String(), "_")
	if cleaned == "" {
		return "sub"
	}
	return cleaned
}

func precheckHLSCodec(c *gin.Context, fileID int) (int, serializer.Response, error) {
	info, status, resp := getVideoInfo(c, fileID)
	if info == nil {
		return status, resp, fmt.Errorf("precheck failed")
	}

	if !info.HLSCompatible {
		unsupported := fmt.Errorf("%w: video codec=%q, audio codec=%q", queue.ErrUnsupportedCodec, info.Codec, info.AudioCodec)
		return http.StatusBadRequest, serializer.Response{Code: 1, Msg: "unsupported codec", Error: unsupported.Error()}, unsupported
	}

	return http.StatusOK, serializer.Response{}, nil
}
