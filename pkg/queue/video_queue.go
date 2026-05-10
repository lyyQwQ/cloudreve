package queue

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/entity"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/ent/node"
	entsetting "github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	settingpkg "github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

var ErrUnsupportedCodec = errors.New("unsupported codec")

const (
	VideoSubtitleBurnTaskType = "video_subtitle_burn"
	VideoHLSSliceTaskType     = "video_hls_slice"

	VideoSubtitleModeAuto     = "auto"
	VideoSubtitleModeExternal = "external"
	VideoSubtitleModeEmbedded = "embedded"

	hlsCodecName  = "h264/aac"
	SummaryKeyDst = "dst"

	videoFFMpegThreadsSettingName = "video_ffmpeg_threads"
	videoFFMpegNiceSettingName    = "video_ffmpeg_nice"
	videoFFMpegThreadsDefault     = 1
	videoFFMpegNiceDefault        = 10

	tempPathSettingName    = "temp_path"
	tempPathSettingDefault = "temp"

	workerTransferPhaseSourceDownload = "source_download"
	workerTransferPhaseOutputDownload = "output_download"
	workerProgressTransfer            = "worker_transfer"
	workerProgressTranscode           = "worker_transcode"

	subtitleStyle1080p        = "FontSize=22,MarginV=28,Outline=0.3,Shadow=1"
	subtitleStyle720p         = "FontSize=18,MarginV=20,Outline=0.3,Shadow=1"
	subtitleStyleHeightCutoff = 900
	ffmpegStderrTailLimit     = 32 * 1024
)

type VideoSubtitleOption struct {
	Mode          string `json:"mode,omitempty"`
	ExternalName  string `json:"external_name,omitempty"`
	EmbeddedIndex *int   `json:"embedded_index,omitempty"`
}

type VideoTaskState struct {
	FileID          int                  `json:"file_id"`
	NodeID          int                  `json:"node_id,omitempty"`
	Subtitle        *VideoSubtitleOption `json:"subtitle,omitempty"`
	Lang            string               `json:"lang,omitempty"`
	Dst             string               `json:"dst,omitempty"`
	OutputPath      string               `json:"output_path,omitempty"`
	ProgressCurrent int64                `json:"progress_current,omitempty"`
	ProgressTotal   int64                `json:"progress_total,omitempty"`
	Duration        float64              `json:"duration,omitempty"`
	FFmpegProgress  float64              `json:"ffmpeg_progress,omitempty"`

	WorkerJobID             string  `json:"worker_job_id,omitempty"`
	WorkerTransferPhase     string  `json:"worker_transfer_phase,omitempty"`
	WorkerTransferProgress  float64 `json:"worker_transfer_progress,omitempty"`
	WorkerTranscodeProgress float64 `json:"worker_transcode_progress,omitempty"`
	WorkerDownloadedBytes   int64   `json:"worker_downloaded_bytes,omitempty"`
	WorkerTotalBytes        int64   `json:"worker_total_bytes,omitempty"`
	WorkerOutputSize        int64   `json:"worker_output_size,omitempty"`
	WorkerStartedAt         int64   `json:"worker_started_at,omitempty"`
}

func ParseVideoTaskState(state string) (*VideoTaskState, error) {
	var s VideoTaskState
	if err := json.Unmarshal([]byte(state), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

type VideoSubtitleBurnTask struct {
	*DBTask
}

type VideoHLSSliceTask struct {
	*DBTask
}

func init() {
	RegisterResumableTaskFactory(VideoSubtitleBurnTaskType, func(model *ent.Task) Task {
		return NewVideoSubtitleBurnTaskFromModel(model)
	})
	RegisterResumableTaskFactory(VideoHLSSliceTaskType, func(model *ent.Task) Task {
		return NewVideoHLSSliceTaskFromModel(model)
	})
}

func NewVideoSubtitleBurnTask(ctx context.Context, fileID int, creator *ent.User, option *VideoSubtitleOption) (*VideoSubtitleBurnTask, error) {
	stateBytes, err := json.Marshal(&VideoTaskState{FileID: fileID, Subtitle: option})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &VideoSubtitleBurnTask{
		DBTask: &DBTask{
			DirectOwner: creator,
			Task: &ent.Task{
				Type:          VideoSubtitleBurnTaskType,
				CorrelationID: logging.CorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
		},
	}, nil
}

func NewVideoHLSSliceTask(ctx context.Context, fileID int, creator *ent.User) (*VideoHLSSliceTask, error) {
	stateBytes, err := json.Marshal(&VideoTaskState{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &VideoHLSSliceTask{
		DBTask: &DBTask{
			DirectOwner: creator,
			Task: &ent.Task{
				Type:          VideoHLSSliceTaskType,
				CorrelationID: logging.CorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
		},
	}, nil
}

func NewVideoSubtitleBurnTaskFromModel(model *ent.Task) *VideoSubtitleBurnTask {
	return &VideoSubtitleBurnTask{
		DBTask: &DBTask{Task: model},
	}
}

func NewVideoHLSSliceTaskFromModel(model *ent.Task) *VideoHLSSliceTask {
	return &VideoHLSSliceTask{
		DBTask: &DBTask{Task: model},
	}
}

func (t *VideoSubtitleBurnTask) Do(ctx context.Context) (task.Status, error) {
	state, err := ParseVideoTaskState(t.State())
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(fmt.Errorf("failed to unmarshal state: %s (%w)", err, CriticalErr))
	}

	logger := logging.FromContext(ctx)
	logger.Info("Video task start task_type=%s file_id=%d", t.Type(), state.FileID)

	start := time.Now()
	t.updateProgress(1, 4)

	dep, err := resolveVideoTaskDep(ctx)
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}

	scheduledNodeID, fallbackToMaster, err := selectVideoExecutionNode(ctx, dep, state.NodeID)
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}
	if fallbackToMaster {
		logger.Warning("Video subtitle task fallback to master node task_type=%s file_id=%d preferred_node_id=%d selected_node_id=%d", t.Type(), state.FileID, state.NodeID, scheduledNodeID)
	}
	if scheduledNodeID > 0 {
		state.NodeID = scheduledNodeID
		t.persistState(state)
	}

	fileModel, input, err := resolveVideoTaskInput(ctx, dep, state.FileID)
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}

	height, probePayload, probeStderr, err := probeVideoHeight(ctx, input)
	if err != nil {
		logger.Warning("Video subtitle probe failed, fallback to 720p style task_type=%s file_id=%d stderr=%s err=%v", t.Type(), state.FileID, probeStderr, err)
		height = 0
		probePayload = nil
	}

	filterArg, modeUsed, err := buildSubtitleFilterArg(input, state.Subtitle, height)
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}

	if probePayload != nil {
		rawDuration := strings.TrimSpace(probePayload.Format.Duration)
		if rawDuration != "" && !strings.EqualFold(rawDuration, "N/A") {
			if parsed, err := strconv.ParseFloat(rawDuration, 64); err == nil && parsed > 0 {
				state.Duration = parsed
				t.persistState(state)
			}
		}
	}

	state.Lang = resolveSubtitleLanguage(state.Subtitle, modeUsed, state.Lang, probePayload)
	t.persistState(state)

	t.updateProgress(2, 4)

	outputPath := buildSubtitleBurnOutputPath(ctx, state.FileID)
	state.OutputPath = outputPath
	t.persistState(state)

	onProgress := func(pct float64) {
		updateVideoTaskFFMpegProgress(t.DBTask, pct)
	}
	if state.Duration <= 0 {
		onProgress = nil
	}
	bitrate := resolveBitrate(probePayload)
	remoteDone := false
	if remoteCfg := loadRemoteFFMpegWorkerConfig(ctx); shouldUseRemoteSubtitleBurn(input, state.Subtitle, modeUsed, remoteCfg) {
		state.WorkerStartedAt = time.Now().Unix()
		state.WorkerTransferPhase = workerTransferPhaseSourceDownload
		t.persistState(state)

		sourceURL, sourceErr := buildRemoteFFMpegWorkerSourceURL(ctx, t, fileModel, remoteCfg)
		if sourceErr != nil {
			logger.Warning("Video subtitle remote worker source url unavailable task_type=%s file_id=%d err=%v", t.Type(), state.FileID, sourceErr)
		} else {
			req := remoteWorkerSubtitleBurnRequest{
				SourceURL:     sourceURL,
				EmbeddedIndex: remoteSubtitleEmbeddedIndex(state.Subtitle),
				Mode:          modeUsed,
				Duration:      state.Duration,
				Bitrate:       bitrate,
				VideoHeight:   height,
			}
			if modeUsed == VideoSubtitleModeExternal {
				req.SubtitleName = strings.TrimSpace(state.Subtitle.ExternalName)
				subtitleURL, subtitleErr := buildRemoteFFMpegWorkerSubtitleURL(ctx, t, fileModel, remoteCfg, req.SubtitleName)
				if subtitleErr != nil {
					logger.Warning("Video subtitle remote worker subtitle url unavailable task_type=%s file_id=%d err=%v", t.Type(), state.FileID, subtitleErr)
				} else {
					req.SubtitleURL = subtitleURL
				}
			}
			if modeUsed != VideoSubtitleModeExternal || req.SubtitleURL != "" {
				remoteErr := runRemoteSubtitleBurn(ctx, t, remoteCfg, req, outputPath)
				if remoteErr == nil {
					remoteDone = true
				} else if ctx.Err() != nil {
					return task.StatusError, wrapVideoTaskErr(remoteErr)
				} else {
					var startedErr *remoteWorkerStartedError
					if errors.As(remoteErr, &startedErr) {
						logger.Error("Video subtitle remote worker failed after job start task_type=%s file_id=%d mode=%s err=%v", t.Type(), state.FileID, modeUsed, remoteErr)
						return task.StatusError, wrapVideoTaskErr(remoteErr)
					}
					logger.Warning("Video subtitle remote worker failed before job start, fallback to local ffmpeg task_type=%s file_id=%d mode=%s err=%v", t.Type(), state.FileID, modeUsed, remoteErr)
				}
			}
		}
	}

	if !remoteDone {
		ffmpegStderr, err := runSubtitleBurnFFMpeg(ctx, input, filterArg, outputPath, state.Duration, bitrate, onProgress)
		if err != nil {
			logger.Error("Video subtitle ffmpeg failed task_type=%s file_id=%d mode=%s stderr=%s err=%v", t.Type(), state.FileID, modeUsed, ffmpegStderr, err)
			return task.StatusError, wrapVideoTaskErr(err)
		}
	}

	t.updateProgress(3, 4)

	if _, err := os.Stat(outputPath); err != nil {
		return task.StatusError, wrapVideoTaskErr(fmt.Errorf("subtitle output missing: %w", err))
	}

	if err := persistBurnedOutput(ctx, t, fileModel, outputPath, state.Lang); err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}

	t.updateProgress(4, 4)
	logger.Info("Video task completed task_type=%s file_id=%d mode=%s node_id=%d duration=%s", t.Type(), state.FileID, modeUsed, state.NodeID, time.Since(start))
	return task.StatusCompleted, nil
}

func (t *VideoHLSSliceTask) Do(ctx context.Context) (task.Status, error) {
	state, err := ParseVideoTaskState(t.State())
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(fmt.Errorf("failed to unmarshal state: %s (%w)", err, CriticalErr))
	}

	logger := logging.FromContext(ctx)
	logger.Info("Video task start task_type=%s file_id=%d", t.Type(), state.FileID)

	start := time.Now()
	t.updateProgress(1, 3)

	dep, err := resolveVideoTaskDep(ctx)
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}

	fileModel, input, err := resolveVideoTaskInput(ctx, dep, state.FileID)
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}

	vCodec, aCodec, aChannels, hasAudio, probeStderr, err := probeVideoCodecs(ctx, input)
	if err != nil {
		logger.Error("Video hls precheck failed task_type=%s file_id=%d stderr=%s err=%v", t.Type(), state.FileID, probeStderr, err)
		return task.StatusError, wrapVideoTaskErr(err)
	}
	if !strings.EqualFold(vCodec, "h264") {
		unsupported := fmt.Errorf("%w: video codec=%q, audio codec=%q", ErrUnsupportedCodec, vCodec, aCodec)
		return task.StatusError, wrapVideoTaskErr(unsupported)
	}

	outputDir := buildHLSOutputDir(state.FileID)
	state.OutputPath = outputDir
	t.persistState(state)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return task.StatusError, wrapVideoTaskErr(fmt.Errorf("failed to create hls output dir: %w", err))
	}

	playlistPath := filepath.Join(outputDir, "index.m3u8")
	segmentPattern := filepath.Join(outputDir, "segment_%05d.ts")
	ffmpegStderr, err := runHLSFFMpeg(ctx, input, playlistPath, segmentPattern, aCodec, aChannels, hasAudio)
	if err != nil {
		logger.Error("Video hls ffmpeg failed task_type=%s file_id=%d stderr=%s err=%v", t.Type(), state.FileID, ffmpegStderr, err)
		return task.StatusError, wrapVideoTaskErr(err)
	}

	t.updateProgress(2, 3)

	segmentCount, totalSize, err := collectHLSOutputStats(outputDir)
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}

	if err := persistHLSResult(ctx, dep, fileModel, outputDir, segmentCount, totalSize); err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}

	t.updateProgress(3, 3)
	logger.Info("Video task completed task_type=%s file_id=%d duration=%s", t.Type(), state.FileID, time.Since(start))
	return task.StatusCompleted, nil
}

func wrapVideoTaskErr(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, CriticalErr) {
		return err
	}

	if errors.Is(err, ErrUnsupportedCodec) {
		return fmt.Errorf("%w (%w)", err, CriticalErr)
	}

	return err
}

func buildVideoTaskProgress(taskType string, state string) Progresses {
	progress := &Progress{Total: 1, Current: 0}
	res := Progresses{taskType: progress}
	if parsed, err := ParseVideoTaskState(state); err == nil {
		progress.Identifier = strconv.Itoa(parsed.FileID)
		if parsed.ProgressTotal > 0 {
			progress.Total = parsed.ProgressTotal
		}
		if parsed.ProgressCurrent > 0 {
			progress.Current = parsed.ProgressCurrent
		}

		hasWorkerProgress := parsed.WorkerTransferPhase != "" || parsed.WorkerTransferProgress > 0 || parsed.WorkerTranscodeProgress > 0
		if parsed.Duration > 0 && !hasWorkerProgress {
			pct := parsed.FFmpegProgress
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			res["ffmpeg"] = &Progress{Total: 100, Current: int64(pct + 0.5), Identifier: strconv.Itoa(parsed.FileID)}
		}
		if hasWorkerProgress {
			res[workerProgressTransfer] = &Progress{
				Total:      100,
				Current:    int64(clampFFMpegProgress(parsed.WorkerTransferProgress) + 0.5),
				Identifier: parsed.WorkerTransferPhase,
			}
			res[workerProgressTranscode] = &Progress{
				Total:      100,
				Current:    int64(clampFFMpegProgress(parsed.WorkerTranscodeProgress) + 0.5),
				Identifier: strconv.Itoa(parsed.FileID),
			}
		}
	}

	return res
}

func (t *VideoSubtitleBurnTask) Progress(_ context.Context) Progresses {
	return buildVideoTaskProgress(VideoSubtitleBurnTaskType, t.State())
}

func (t *VideoSubtitleBurnTask) Summarize(_ hashid.Encoder) *Summary {
	summary := &Summary{
		Props: map[string]any{
			SummaryKeyDst: "",
		},
	}

	state, err := ParseVideoTaskState(t.State())
	if err != nil {
		return summary
	}

	summary.NodeID = state.NodeID
	summary.Props[SummaryKeyDst] = state.Dst
	if state.WorkerTransferPhase != "" || state.WorkerTransferProgress > 0 || state.WorkerTranscodeProgress > 0 || state.WorkerOutputSize > 0 || state.WorkerStartedAt > 0 {
		summary.Props["worker_transfer_phase"] = state.WorkerTransferPhase
		summary.Props["worker_transfer_progress"] = state.WorkerTransferProgress
		summary.Props["worker_transcode_progress"] = state.WorkerTranscodeProgress
		summary.Props["worker_output_size"] = state.WorkerOutputSize
		summary.Props["worker_started_at"] = state.WorkerStartedAt
	}
	return summary
}

func (t *VideoSubtitleBurnTask) Cleanup(ctx context.Context) error {
	state, err := ParseVideoTaskState(t.State())
	if err != nil {
		return nil
	}

	outputPath := strings.TrimSpace(state.OutputPath)
	if outputPath == "" {
		return nil
	}

	cleaned := filepath.Clean(outputPath)
	allowedPrefixNew := filepath.Clean(util.DataPath(filepath.Join(loadTempPathSetting(ctx), "subtitle-burn")))
	allowedPrefixOld := filepath.Clean(filepath.Join(os.TempDir(), "cloudreve-subtitle-burn"))
	if !strings.HasPrefix(cleaned, allowedPrefixNew+string(os.PathSeparator)) && !strings.HasPrefix(cleaned, allowedPrefixOld+string(os.PathSeparator)) {
		return nil
	}

	if err := os.Remove(cleaned); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return nil
}

func (t *VideoHLSSliceTask) Progress(_ context.Context) Progresses {
	return buildVideoTaskProgress(VideoHLSSliceTaskType, t.State())
}

func (t *VideoHLSSliceTask) Cleanup(ctx context.Context) error {
	state, err := ParseVideoTaskState(t.State())
	if err != nil {
		return nil
	}

	outputDir := strings.TrimSpace(state.OutputPath)
	if outputDir == "" {
		return nil
	}

	cleaned := filepath.Clean(outputDir)
	if shouldSkipHLSCleanupForPersistedArtifact(ctx, state.FileID, cleaned) {
		return nil
	}

	allowedPrefixNew := filepath.Clean(util.DataPath("hls"))
	allowedPrefixOld := filepath.Clean(filepath.Join(os.TempDir(), "cloudreve-hls"))
	if cleaned == allowedPrefixNew || cleaned == allowedPrefixOld {
		return nil
	}
	if !strings.HasPrefix(cleaned, allowedPrefixNew+string(os.PathSeparator)) && !strings.HasPrefix(cleaned, allowedPrefixOld+string(os.PathSeparator)) {
		return nil
	}

	if err := os.RemoveAll(cleaned); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return nil
}

func shouldSkipHLSCleanupForPersistedArtifact(ctx context.Context, fileID int, outputDir string) bool {
	dep, err := resolveVideoTaskDep(ctx)
	if err != nil {
		return false
	}

	artifact, err := dep.DBClient().HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(ctx)
	if err != nil {
		return false
	}

	return filepath.Clean(strings.TrimSpace(artifact.StoragePath)) == outputDir
}

type videoTaskDep interface {
	FileClient() inventory.FileClient
	UserClient() inventory.UserClient
	DBClient() *ent.Client
}

type ffprobeCodecPayload struct {
	Streams []struct {
		Index     int               `json:"index"`
		CodecType string            `json:"codec_type"`
		CodecName string            `json:"codec_name"`
		BitRate   string            `json:"bit_rate"`
		Channels  int               `json:"channels"`
		Height    int               `json:"height"`
		Tags      map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
		Size     string `json:"size"`
	} `json:"format"`
}

func resolveBitrate(payload *ffprobeCodecPayload) int {
	defaultByHeight := func(height int) int {
		switch {
		case height >= 1080:
			return 5_000_000
		case height >= 720:
			return 2_500_000
		case height >= 480:
			return 1_000_000
		default:
			return 500_000
		}
	}

	if payload == nil {
		return defaultByHeight(720)
	}

	height := 0
	for _, stream := range payload.Streams {
		if !strings.EqualFold(strings.TrimSpace(stream.CodecType), "video") {
			continue
		}

		height = stream.Height
		raw := strings.TrimSpace(stream.BitRate)
		if raw != "" && !strings.EqualFold(raw, "N/A") {
			if v, err := strconv.Atoi(raw); err == nil && v > 0 {
				return v
			}
		}
		break
	}

	rawFormatBitrate := strings.TrimSpace(payload.Format.BitRate)
	if rawFormatBitrate != "" && !strings.EqualFold(rawFormatBitrate, "N/A") {
		if v, err := strconv.Atoi(rawFormatBitrate); err == nil && v > 0 {
			return v
		}
	}

	rawSize := strings.TrimSpace(payload.Format.Size)
	rawDuration := strings.TrimSpace(payload.Format.Duration)
	if rawSize != "" && rawDuration != "" && !strings.EqualFold(rawSize, "N/A") && !strings.EqualFold(rawDuration, "N/A") {
		if sizeBytes, err := strconv.ParseInt(rawSize, 10, 64); err == nil && sizeBytes > 0 {
			if dur, err := strconv.ParseFloat(rawDuration, 64); err == nil && dur > 0 {
				bps := int(float64(sizeBytes) * 8 / dur)
				if bps > 0 {
					return bps
				}
			}
		}
	}

	return defaultByHeight(height)
}

func resolveVideoTaskDep(ctx context.Context) (videoTaskDep, error) {
	dep := depFromContext(ctx)
	if dep == nil {
		return nil, fmt.Errorf("missing queue dependency in context (%w)", CriticalErr)
	}

	videoDep, ok := dep.(videoTaskDep)
	if !ok {
		return nil, fmt.Errorf("unsupported queue dependency type (%w)", CriticalErr)
	}

	return videoDep, nil
}

func resolveVideoTaskInput(ctx context.Context, dep videoTaskDep, fileID int) (*ent.File, string, error) {
	loadCtx := context.WithValue(ctx, inventory.LoadFileEntity{}, true)
	fileModel, err := dep.FileClient().GetByID(loadCtx, fileID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, "", fmt.Errorf("file not found: %w (%w)", err, CriticalErr)
		}
		return nil, "", fmt.Errorf("failed to load file: %w", err)
	}

	if fileModel.PrimaryEntity <= 0 {
		return nil, "", fmt.Errorf("file has no primary entity (%w)", CriticalErr)
	}

	var primary *ent.Entity
	for _, e := range fileModel.Edges.Entities {
		if e != nil && e.ID == fileModel.PrimaryEntity {
			primary = e
			break
		}
	}

	if primary == nil {
		primary, err = dep.DBClient().Entity.Query().Where(entity.ID(fileModel.PrimaryEntity)).Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return nil, "", fmt.Errorf("primary entity not found: %w (%w)", err, CriticalErr)
			}
			return nil, "", fmt.Errorf("failed to load primary entity: %w", err)
		}
	}

	if strings.TrimSpace(primary.Source) == "" {
		return nil, "", fmt.Errorf("entity source is empty (%w)", CriticalErr)
	}

	return fileModel, primary.Source, nil
}

func probeVideoCodecs(ctx context.Context, input string) (string, string, int, bool, string, error) {
	payload, stderrText, err := runVideoFFProbe(ctx, input)
	if err != nil {
		return "", "", 0, false, stderrText, err
	}

	var videoCodec string
	var audioCodec string
	var audioChannels int
	hasAudio := false
	for _, stream := range payload.Streams {
		switch strings.ToLower(strings.TrimSpace(stream.CodecType)) {
		case "video":
			if videoCodec == "" {
				videoCodec = strings.TrimSpace(stream.CodecName)
			}
		case "audio":
			hasAudio = true
			if audioCodec == "" {
				audioCodec = strings.TrimSpace(stream.CodecName)
				audioChannels = stream.Channels
			}
		}
	}

	return videoCodec, audioCodec, audioChannels, hasAudio, stderrText, nil
}

func probeVideoHeight(ctx context.Context, input string) (int, *ffprobeCodecPayload, string, error) {
	payload, stderrText, err := runVideoFFProbe(ctx, input)
	if err != nil {
		return 0, nil, stderrText, err
	}

	for _, stream := range payload.Streams {
		if strings.EqualFold(strings.TrimSpace(stream.CodecType), "video") {
			if stream.Height > 0 {
				return stream.Height, payload, stderrText, nil
			}
			break
		}
	}

	return 0, payload, stderrText, nil
}

func buildHLSOutputDir(fileID int) string {
	return filepath.Join(util.DataPath("hls"), strconv.Itoa(fileID), strconv.FormatInt(time.Now().UnixNano(), 10))
}

func buildSubtitleBurnOutputPath(ctx context.Context, fileID int) string {
	base := util.DataPath(filepath.Join(loadTempPathSetting(ctx), "subtitle-burn"))
	return filepath.Join(base, strconv.Itoa(fileID), fmt.Sprintf("burned_%d.mp4", time.Now().UnixNano()))
}

func persistBurnedOutput(ctx context.Context, taskRef *VideoSubtitleBurnTask, fileModel *ent.File, outputPath, lang string) error {
	if taskRef == nil || fileModel == nil {
		return fmt.Errorf("invalid burn persist argument (%w)", CriticalErr)
	}

	dep, err := resolveVideoTaskDep(ctx)
	if err != nil {
		return err
	}

	srcSource, err := resolvePrimaryEntitySource(ctx, dep, fileModel)
	if err != nil {
		return err
	}

	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("failed to stat burned output: %w", err)
	}

	fileName := buildBurnedOutputFileName(fileModel.Name, lang)
	parentPhysicalDir := filepath.Dir(srcSource)
	dstPhysicalPath := filepath.Join(parentPhysicalDir, "burned", fileName)
	dstDir := filepath.Dir(dstPhysicalPath)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("failed to create burned output directory: %w", err)
	}

	_, statErr := os.Stat(dstPhysicalPath)
	dstExisted := statErr == nil

	tmpDstPhysicalPath := fmt.Sprintf("%s.tmp-%d", dstPhysicalPath, time.Now().UnixNano())
	if err := copyLocalFile(outputPath, tmpDstPhysicalPath); err != nil {
		return fmt.Errorf("failed to persist burned output file: %w", err)
	}
	if err := os.Rename(tmpDstPhysicalPath, dstPhysicalPath); err != nil {
		_ = os.Remove(tmpDstPhysicalPath)
		return fmt.Errorf("failed to move burned output file: %w", err)
	}

	persisted := false
	defer func() {
		if !persisted && !dstExisted {
			_ = os.Remove(dstPhysicalPath)
		}
	}()

	burnedFolder, err := ensureBurnedFolder(ctx, dep, fileModel)
	if err != nil {
		return err
	}

	if err := upsertBurnedFileRecord(ctx, dep, burnedFolder, fileModel, fileName, dstPhysicalPath, outputInfo.Size()); err != nil {
		return err
	}
	persisted = true

	dstURI, err := buildBurnedOutputURI(ctx, dep, burnedFolder, fileName)
	if err != nil {
		return err
	}

	state, err := ParseVideoTaskState(taskRef.State())
	if err != nil {
		return fmt.Errorf("failed to parse video task state: %w", err)
	}

	state.Dst = dstURI
	taskRef.persistState(state)
	return nil
}

func buildBurnedOutputFileName(fileName, lang string) string {
	baseName := filepath.Base(strings.TrimSpace(fileName))
	stem := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	if stem == "" {
		stem = "video"
	}
	stem = strings.ReplaceAll(stem, "/", "_")
	stem = strings.ReplaceAll(stem, "\\", "_")

	return fmt.Sprintf("%s_%s.mp4", stem, sanitizeBurnedOutputLang(lang))
}

func sanitizeBurnedOutputLang(lang string) string {
	lang = filepath.Base(strings.TrimSpace(lang))
	if lang == "" {
		return "sub"
	}

	b := strings.Builder{}
	b.Grow(len(lang))
	for _, r := range lang {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteRune('_')
	}

	cleaned := strings.Trim(b.String(), "_")
	if cleaned == "" {
		return "sub"
	}

	return cleaned
}

func resolvePrimaryEntitySource(ctx context.Context, dep videoTaskDep, fileModel *ent.File) (string, error) {
	if fileModel.PrimaryEntity <= 0 {
		return "", fmt.Errorf("file has no primary entity (%w)", CriticalErr)
	}

	for _, e := range fileModel.Edges.Entities {
		if e != nil && e.ID == fileModel.PrimaryEntity {
			source := strings.TrimSpace(e.Source)
			if source == "" {
				return "", fmt.Errorf("primary entity source is empty (%w)", CriticalErr)
			}
			return source, nil
		}
	}

	e, err := dep.DBClient().Entity.Query().Where(entity.ID(fileModel.PrimaryEntity)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", fmt.Errorf("primary entity not found: %w (%w)", err, CriticalErr)
		}
		return "", fmt.Errorf("failed to query primary entity: %w", err)
	}

	source := strings.TrimSpace(e.Source)
	if source == "" {
		return "", fmt.Errorf("primary entity source is empty (%w)", CriticalErr)
	}

	return source, nil
}

func ensureBurnedFolder(ctx context.Context, dep videoTaskDep, fileModel *ent.File) (*ent.File, error) {
	if fileModel == nil {
		return nil, fmt.Errorf("file model is nil (%w)", CriticalErr)
	}

	parent, err := dep.FileClient().GetParentFile(ctx, fileModel, false)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to load source parent folder: %w", err)
	}
	if ent.IsNotFound(err) {
		if fileModel.FileChildren > 0 {
			parent, err = dep.DBClient().File.Get(ctx, fileModel.FileChildren)
			if err != nil {
				return nil, fmt.Errorf("failed to load source parent folder: %w", err)
			}
		} else {
			parent = nil
		}
	}

	burnedFolder, err := dep.FileClient().CreateFolder(ctx, parent, &inventory.CreateFolderParameters{
		Owner: fileModel.OwnerID,
		Name:  "burned",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create burned folder: %w", err)
	}

	return burnedFolder, nil
}

func upsertBurnedFileRecord(ctx context.Context, dep videoTaskDep, burnedFolder, fileModel *ent.File, fileName, source string, size int64) error {
	if burnedFolder == nil {
		return fmt.Errorf("burned folder is nil (%w)", CriticalErr)
	}

	entityArgs := &inventory.EntityParameters{
		OwnerID:         fileModel.OwnerID,
		EntityType:      types.EntityTypeVersion,
		StoragePolicyID: fileModel.StoragePolicyFiles,
		Source:          source,
		Size:            size,
		Importing:       true,
	}

	existing, err := dep.FileClient().GetChildFile(ctx, burnedFolder, fileModel.OwnerID, fileName, false)
	if err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("failed to query burned file: %w", err)
		}

		created, entityModel, _, err := dep.FileClient().CreateFile(ctx, burnedFolder, &inventory.CreateFileParameters{
			FileType:         types.FileTypeFile,
			Name:             fileName,
			StoragePolicyID:  fileModel.StoragePolicyFiles,
			EntityParameters: entityArgs,
		})
		if err != nil {
			return fmt.Errorf("failed to create burned file: %w", err)
		}

		if entityModel != nil {
			if err := dep.FileClient().SetPrimaryEntity(ctx, created, entityModel); err != nil {
				return fmt.Errorf("failed to set burned file primary entity: %w", err)
			}
		}

		return nil
	}

	entityModel, _, err := dep.FileClient().CreateEntity(ctx, existing, entityArgs)
	if err != nil {
		return fmt.Errorf("failed to create burned file entity: %w", err)
	}

	if err := dep.FileClient().SetPrimaryEntity(ctx, existing, entityModel); err != nil {
		return fmt.Errorf("failed to update burned file primary entity: %w", err)
	}

	return nil
}

func buildBurnedOutputURI(ctx context.Context, dep videoTaskDep, burnedFolder *ent.File, fileName string) (string, error) {
	parts := []string{fileName}
	current := burnedFolder
	for current != nil {
		if !(current.FileChildren <= 0 && current.Name == inventory.RootFolderName) {
			parts = append([]string{current.Name}, parts...)
		}

		if current.FileChildren <= 0 {
			break
		}

		parent, err := dep.DBClient().File.Get(ctx, current.FileChildren)
		if err != nil {
			return "", fmt.Errorf("failed to build burn output uri: %w", err)
		}
		current = parent
	}

	path := strings.Join(parts, "/")
	if path == "" {
		return fmt.Sprintf("%s://%s", constants.CloudreveScheme, constants.FileSystemMy), nil
	}

	return fmt.Sprintf("%s://%s/%s", constants.CloudreveScheme, constants.FileSystemMy, path), nil
}

func copyLocalFile(src, dst string) error {
	srcHandle, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcHandle.Close()

	dstHandle, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstHandle.Close()

	if _, err := io.Copy(dstHandle, srcHandle); err != nil {
		return err
	}

	return dstHandle.Sync()
}

func runHLSFFMpeg(ctx context.Context, input, playlistPath, segmentPattern, audioCodec string, audioChannels int, hasAudio bool) (string, error) {
	args := []string{
		"-v", "warning",
		"-y",
		"-i", input,
		"-map", "0:v:0",
		"-c:v", "copy",
	}

	if hasAudio {
		args = append(args, "-map", "0:a:0?")
		if strings.EqualFold(audioCodec, "aac") && (audioChannels == 0 || audioChannels <= 2) {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args,
				"-c:a", "aac",
				"-ac", "2",
				"-ar", "48000",
			)
		}
	} else {
		args = append(args, "-an")
	}

	args = append(args,
		"-hls_time", "10",
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", segmentPattern,
		playlistPath,
	)

	cmd := newVideoFFMpegCommand(ctx, args)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to prepare ffmpeg stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to prepare ffmpeg stderr pipe: %w", err)
	}

	tail := newBoundedTailBuffer(ffmpegStderrTailLimit)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	var wg sync.WaitGroup
	var stdoutErr error
	var stderrErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, stdoutErr = io.Copy(io.Discard, stdoutPipe)
	}()
	go func() {
		defer wg.Done()
		_, stderrErr = io.Copy(tail, stderrPipe)
	}()

	waitErr := cmd.Wait()
	wg.Wait()

	stderrText := strings.TrimSpace(tail.String())
	if stdoutErr != nil {
		return stderrText, fmt.Errorf("failed to read ffmpeg stdout output: %w", stdoutErr)
	}
	if stderrErr != nil {
		return stderrText, fmt.Errorf("failed to read ffmpeg stderr output: %w", stderrErr)
	}
	if waitErr != nil {
		return stderrText, fmt.Errorf("failed to invoke ffmpeg: %w (stderr: %s)", waitErr, stderrText)
	}

	return stderrText, nil
}

func runVideoFFProbe(ctx context.Context, input string) (*ffprobeCodecPayload, string, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "warning",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		input,
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	stderrText := strings.TrimSpace(stderr.String())
	if err != nil {
		return nil, stderrText, fmt.Errorf("failed to invoke ffprobe: %w (stderr: %s)", err, stderrText)
	}

	var payload ffprobeCodecPayload
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return nil, stderrText, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	return &payload, stderrText, nil
}

func runSubtitleBurnFFMpeg(ctx context.Context, input, filterArg, output string, duration float64, bitrate int, onProgress func(float64)) (string, error) {
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return "", fmt.Errorf("failed to create subtitle output dir: %w", err)
	}

	args := []string{
		"-v", "warning",
		"-y",
		"-i", input,
		"-vf", filterArg,
		"-c:v", "libx264",
		"-crf", "18",
		"-preset", "medium",
	}

	if bitrate > 0 {
		bufsize := bitrate * 2
		args = append(args,
			"-maxrate", strconv.Itoa(bitrate),
			"-bufsize", strconv.Itoa(bufsize),
		)
	}

	args = append(args,
		"-c:a", "copy",
		"-movflags", "+faststart",
		"-nostats",
		"-progress", "pipe:1",
		output,
	)

	cmd := newVideoFFMpegCommand(ctx, args)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to prepare ffmpeg stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to prepare ffmpeg stderr pipe: %w", err)
	}

	tail := newBoundedTailBuffer(ffmpegStderrTailLimit)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	var wg sync.WaitGroup
	var progressEnd bool
	var stdoutErr error
	var stderrErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		progressEnd, stdoutErr = readFFMpegProgressOutput(stdoutPipe, duration, onProgress)
	}()
	go func() {
		defer wg.Done()
		_, stderrErr = io.Copy(tail, stderrPipe)
	}()

	waitErr := cmd.Wait()
	wg.Wait()

	stderrText := strings.TrimSpace(tail.String())
	if stdoutErr != nil {
		return stderrText, fmt.Errorf("failed to read ffmpeg progress output: %w", stdoutErr)
	}
	if stderrErr != nil {
		return stderrText, fmt.Errorf("failed to read ffmpeg stderr output: %w", stderrErr)
	}
	if waitErr != nil {
		return stderrText, fmt.Errorf("failed to invoke ffmpeg: %w (stderr: %s)", waitErr, stderrText)
	}

	if progressEnd && onProgress != nil && duration > 0 {
		onProgress(100)
	}

	return stderrText, nil
}

type boundedTailBuffer struct {
	limit int
	buf   []byte
}

func newBoundedTailBuffer(limit int) *boundedTailBuffer {
	if limit < 0 {
		limit = 0
	}

	return &boundedTailBuffer{limit: limit}
}

func (b *boundedTailBuffer) Write(p []byte) (int, error) {
	if b.limit == 0 {
		return len(p), nil
	}

	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
		return len(p), nil
	}

	if len(b.buf)+len(p) <= b.limit {
		b.buf = append(b.buf, p...)
		return len(p), nil
	}

	overflow := len(b.buf) + len(p) - b.limit
	copy(b.buf, b.buf[overflow:])
	b.buf = b.buf[:len(b.buf)-overflow]
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *boundedTailBuffer) String() string {
	return string(b.buf)
}

func readFFMpegProgressOutput(r io.Reader, duration float64, onProgress func(float64)) (bool, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024)

	progressEnd := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		switch strings.TrimSpace(key) {
		case "out_time_us", "out_time_ms":
			if duration <= 0 || onProgress == nil {
				continue
			}

			outTimeUS, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				continue
			}

			progress := float64(outTimeUS) / 1_000_000 / duration * 100
			onProgress(clampFFMpegProgress(progress))
		case "progress":
			if strings.EqualFold(strings.TrimSpace(value), "end") {
				progressEnd = true
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return progressEnd, err
	}

	return progressEnd, nil
}

func clampFFMpegProgress(progress float64) float64 {
	if progress < 0 {
		return 0
	}
	if progress > 100 {
		return 100
	}

	return progress
}

func newVideoFFMpegCommand(ctx context.Context, args []string) *exec.Cmd {
	threads, nice := loadVideoFFMpegRuntimeOptions(ctx)
	ffmpegArgs := injectFFMpegThreadsBeforeInput(args, threads)

	if nice > 0 {
		if _, err := exec.LookPath("nice"); err == nil {
			niceArgs := make([]string, 0, len(ffmpegArgs)+3)
			niceArgs = append(niceArgs, "-n", strconv.Itoa(nice), "ffmpeg")
			niceArgs = append(niceArgs, ffmpegArgs...)
			return exec.CommandContext(ctx, "nice", niceArgs...)
		}
	}

	return exec.CommandContext(ctx, "ffmpeg", ffmpegArgs...)
}

func injectFFMpegThreadsBeforeInput(args []string, threads int) []string {
	if threads <= 0 {
		return args
	}

	res := make([]string, 0, len(args)+2)
	inserted := false
	for _, arg := range args {
		if !inserted && arg == "-i" {
			res = append(res, "-threads", strconv.Itoa(threads))
			inserted = true
		}
		res = append(res, arg)
	}

	if inserted {
		return res
	}

	res = append([]string{"-threads", strconv.Itoa(threads)}, args...)
	return res
}

func loadVideoFFMpegRuntimeOptions(ctx context.Context) (int, int) {
	dep, ok := depFromContext(ctx).(videoTaskDep)
	if !ok {
		return videoFFMpegThreadsDefault, videoFFMpegNiceDefault
	}

	threads := readVideoFFMpegSettingInt(ctx, dep, videoFFMpegThreadsSettingName, videoFFMpegThreadsDefault)
	nice := readVideoFFMpegSettingInt(ctx, dep, videoFFMpegNiceSettingName, videoFFMpegNiceDefault)
	return threads, nice
}

func loadTempPathSetting(ctx context.Context) string {
	if dep, ok := depFromContext(ctx).(interface{ SettingProvider() settingpkg.Provider }); ok {
		if tempPath := strings.TrimSpace(dep.SettingProvider().TempPath(ctx)); tempPath != "" {
			return tempPath
		}
	}

	dep, ok := depFromContext(ctx).(videoTaskDep)
	if !ok {
		return tempPathSettingDefault
	}

	v, err := dep.DBClient().Setting.Query().Where(entsetting.Name(tempPathSettingName)).Only(ctx)
	if err != nil {
		return tempPathSettingDefault
	}

	trimmed := strings.TrimSpace(v.Value)
	if trimmed == "" {
		return tempPathSettingDefault
	}

	return trimmed
}

func readVideoFFMpegSettingInt(ctx context.Context, dep videoTaskDep, name string, defaultValue int) int {
	v, err := dep.DBClient().Setting.Query().Where(entsetting.Name(name)).Only(ctx)
	if err != nil {
		return defaultValue
	}

	parsed, err := strconv.Atoi(strings.TrimSpace(v.Value))
	if err != nil {
		return defaultValue
	}

	return parsed
}

func buildSubtitleFilterArg(input string, option *VideoSubtitleOption, videoHeight int) (string, string, error) {
	mode := VideoSubtitleModeAuto
	if option != nil && strings.TrimSpace(option.Mode) != "" {
		mode = strings.ToLower(strings.TrimSpace(option.Mode))
	}

	switch mode {
	case VideoSubtitleModeAuto:
		externalPath, err := findFirstExternalSubtitle(input)
		if err != nil {
			return "", "", err
		}

		if externalPath != "" {
			return buildExternalSubtitleFilterArg(externalPath, videoHeight), VideoSubtitleModeExternal, nil
		}

		return buildEmbeddedSubtitleFilterArg(input, 0), VideoSubtitleModeEmbedded, nil
	case VideoSubtitleModeExternal:
		if option == nil {
			return "", "", fmt.Errorf("missing subtitle option for external mode (%w)", CriticalErr)
		}

		externalPath, err := resolveExternalSubtitlePath(input, option.ExternalName)
		if err != nil {
			return "", "", err
		}

		return buildExternalSubtitleFilterArg(externalPath, videoHeight), VideoSubtitleModeExternal, nil
	case VideoSubtitleModeEmbedded:
		if option == nil || option.EmbeddedIndex == nil {
			return "", "", fmt.Errorf("missing subtitle embedded index (%w)", CriticalErr)
		}
		if *option.EmbeddedIndex < 0 {
			return "", "", fmt.Errorf("subtitle embedded index must be >= 0 (%w)", CriticalErr)
		}

		return buildEmbeddedSubtitleFilterArg(input, *option.EmbeddedIndex), VideoSubtitleModeEmbedded, nil
	default:
		return "", "", fmt.Errorf("invalid subtitle mode %q (%w)", mode, CriticalErr)
	}
}

func resolveSubtitleLanguage(option *VideoSubtitleOption, modeUsed, current string, probePayload *ffprobeCodecPayload) string {
	lang := strings.TrimSpace(current)
	if option == nil {
		if lang != "" {
			return lang
		}
		return "sub"
	}

	switch modeUsed {
	case VideoSubtitleModeExternal:
		externalName := strings.TrimSpace(option.ExternalName)
		if externalName != "" {
			ext := filepath.Ext(externalName)
			base := strings.TrimSuffix(externalName, ext)
			if lastDot := strings.LastIndex(base, "."); lastDot >= 0 && lastDot+1 < len(base) {
				resolved := strings.TrimSpace(base[lastDot+1:])
				if resolved != "" {
					return resolved
				}
			}
		}
	case VideoSubtitleModeEmbedded, VideoSubtitleModeAuto:
		idx := 0
		if option.EmbeddedIndex != nil {
			idx = *option.EmbeddedIndex
		}

		if probePayload != nil {
			for _, stream := range probePayload.Streams {
				if stream.Index != idx {
					continue
				}
				if !strings.EqualFold(strings.TrimSpace(stream.CodecType), "subtitle") {
					continue
				}
				if stream.Tags != nil {
					resolved := strings.TrimSpace(stream.Tags["language"])
					if resolved != "" {
						return resolved
					}
				}
			}
		}

		if lang != "" {
			return lang
		}
	}

	if lang != "" {
		return lang
	}

	return "sub"
}

func buildExternalSubtitleFilterArg(path string, videoHeight int) string {
	base := buildSubtitleFilenameFilterArg(path)
	if !strings.EqualFold(filepath.Ext(path), ".srt") {
		return base
	}

	return base + ":force_style='" + subtitleForceStyle(videoHeight) + "'"
}

func buildEmbeddedSubtitleFilterArg(path string, streamIndex int) string {
	return fmt.Sprintf("%s:si=%d", buildSubtitleFilenameFilterArg(path), streamIndex)
}

func buildSubtitleFilenameFilterArg(path string) string {
	return "subtitles=filename='" + escapeFFMpegSubtitlePath(path) + "'"
}

func subtitleForceStyle(videoHeight int) string {
	if videoHeight >= subtitleStyleHeightCutoff {
		return subtitleStyle1080p
	}

	return subtitleStyle720p
}

func IsSupportedExternalSubtitleName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".srt" || ext == ".ass" || ext == ".ssa"
}

func ResolveExternalSubtitlePath(input, subtitleName string) (string, error) {
	return resolveExternalSubtitlePath(input, subtitleName)
}

func resolveExternalSubtitlePath(input, subtitleName string) (string, error) {
	name := strings.TrimSpace(subtitleName)
	if name == "" {
		return "", fmt.Errorf("subtitle external_name is required (%w)", CriticalErr)
	}

	if filepath.Base(name) != name || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", fmt.Errorf("invalid subtitle external_name %q (%w)", subtitleName, CriticalErr)
	}

	paths, err := listExternalSubtitlePaths(input)
	if err != nil {
		return "", err
	}

	for _, p := range paths {
		if filepath.Base(p) == name {
			return p, nil
		}
	}

	return "", fmt.Errorf("subtitle external file not found: %s (%w)", name, CriticalErr)
}

func findFirstExternalSubtitle(input string) (string, error) {
	paths, err := listExternalSubtitlePaths(input)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}

	return paths[0], nil
}

func listExternalSubtitlePaths(input string) ([]string, error) {
	dirPath := filepath.Dir(input)
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to scan subtitle directory: %w", err)
	}

	res := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		if !IsSupportedExternalSubtitleName(entry.Name()) {
			continue
		}

		res = append(res, filepath.Join(dirPath, entry.Name()))
	}

	sort.Slice(res, func(i, j int) bool {
		return strings.ToLower(filepath.Base(res[i])) < strings.ToLower(filepath.Base(res[j]))
	})

	return res, nil
}

func escapeFFMpegSubtitlePath(input string) string {
	var escaped strings.Builder
	escaped.Grow(len(input))

	for _, r := range input {
		switch r {
		case '\\':
			escaped.WriteString(`\\`)
		case ':':
			escaped.WriteString(`\:`)
		case '\'':
			escaped.WriteString("'" + `\\\` + "''")
		default:
			escaped.WriteRune(r)
		}
	}

	return escaped.String()
}

func selectVideoExecutionNode(ctx context.Context, dep videoTaskDep, preferredNodeID int) (int, bool, error) {
	nodeQuery := dep.DBClient().Node.Query().Where(node.StatusEQ(node.StatusActive))
	masterQuery := dep.DBClient().Node.Query().Where(node.StatusEQ(node.StatusActive), node.TypeEQ(node.TypeMaster))

	if preferredNodeID > 0 {
		preferredNode, err := nodeQuery.Where(node.ID(preferredNodeID)).Only(ctx)
		if err == nil {
			if preferredNode.Type == node.TypeMaster {
				return preferredNode.ID, false, nil
			}

			fallbackMaster, fallbackErr := masterQuery.Order(ent.Asc(node.FieldID)).First(ctx)
			if fallbackErr != nil {
				if ent.IsNotFound(fallbackErr) {
					return 0, false, fmt.Errorf("preferred node %d is not executable for subtitle burn and no master fallback found (%w)", preferredNode.ID, CriticalErr)
				}
				return 0, false, fmt.Errorf("failed to resolve master fallback node: %w", fallbackErr)
			}

			return fallbackMaster.ID, true, nil
		}

		if !ent.IsNotFound(err) {
			return 0, false, fmt.Errorf("failed to query preferred node: %w", err)
		}
	}

	masterNode, err := masterQuery.Order(ent.Asc(node.FieldID)).First(ctx)
	if err == nil {
		return masterNode.ID, preferredNodeID > 0, nil
	}

	if ent.IsNotFound(err) {
		return 0, false, fmt.Errorf("no active master node found (%w)", CriticalErr)
	}

	return 0, false, fmt.Errorf("failed to query active master node: %w", err)
}

func collectHLSOutputStats(outputDir string) (int, int64, error) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read hls output dir: %w", err)
	}

	segmentCount := 0
	var totalSize int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".ts") {
			segmentCount++
		}

		info, err := entry.Info()
		if err != nil {
			return 0, 0, fmt.Errorf("failed to read hls output file info: %w", err)
		}
		totalSize += info.Size()
	}

	if segmentCount == 0 {
		return 0, 0, fmt.Errorf("hls output has no segments")
	}

	return segmentCount, totalSize, nil
}

func persistHLSResult(ctx context.Context, dep videoTaskDep, fileModel *ent.File, outputDir string, segmentCount int, totalSize int64) error {
	if dep == nil || fileModel == nil {
		return fmt.Errorf("invalid hls persist argument (%w)", CriticalErr)
	}

	fc, tx, txCtx, err := inventory.WithTx(ctx, dep.FileClient())
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}

	oldStoragePath, storageDiff, err := inventory.UpsertHLSArtifact(
		txCtx,
		fc.GetClient(),
		fileModel.ID,
		fileModel.OwnerID,
		outputDir,
		segmentCount,
		totalSize,
		hlsCodecName,
	)
	if err != nil {
		_ = inventory.Rollback(tx)
		return err
	}

	if storageDiff != 0 {
		tx.AppendStorageDiff(inventory.StorageDiff{fileModel.OwnerID: storageDiff})
	}

	if err := inventory.CommitWithStorageDiff(txCtx, tx, logging.FromContext(txCtx), dep.UserClient()); err != nil {
		return fmt.Errorf("failed to commit hls artifact persist: %w", err)
	}

	cleanOldPath := strings.TrimSpace(oldStoragePath)
	if cleanOldPath != "" && filepath.Clean(cleanOldPath) != filepath.Clean(strings.TrimSpace(outputDir)) {
		if err := inventory.RemoveHLSArtifactDir(cleanOldPath); err != nil {
			return fmt.Errorf("failed to remove old hls artifact dir %q: %w", cleanOldPath, err)
		}
	}

	return nil
}

func (t *VideoHLSSliceTask) updateProgress(current, total int64) {
	updateVideoTaskProgress(t.DBTask, current, total)
}

func (t *VideoHLSSliceTask) persistState(state *VideoTaskState) {
	persistVideoTaskState(t.DBTask, state)
}

func (t *VideoSubtitleBurnTask) updateProgress(current, total int64) {
	updateVideoTaskProgress(t.DBTask, current, total)
}

func (t *VideoSubtitleBurnTask) persistState(state *VideoTaskState) {
	persistVideoTaskState(t.DBTask, state)
}

func updateVideoTaskProgress(taskRef *DBTask, current, total int64) {
	if taskRef == nil {
		return
	}

	state, err := ParseVideoTaskState(taskRef.State())
	if err != nil {
		return
	}

	state.ProgressCurrent = current
	state.ProgressTotal = total
	persistVideoTaskState(taskRef, state)
}

func updateVideoTaskFFMpegProgress(taskRef *DBTask, pct float64) {
	if taskRef == nil {
		return
	}

	state, err := ParseVideoTaskState(taskRef.State())
	if err != nil {
		return
	}

	state.FFmpegProgress = clampFFMpegProgress(pct)
	persistVideoTaskState(taskRef, state)
}

func persistVideoTaskState(taskRef *DBTask, state *VideoTaskState) {
	if taskRef == nil || state == nil {
		return
	}

	stateBytes, err := json.Marshal(state)
	if err != nil {
		return
	}

	taskRef.Lock()
	defer taskRef.Unlock()
	if taskRef.Task != nil {
		taskRef.Task.PrivateState = string(stateBytes)
	}
}
