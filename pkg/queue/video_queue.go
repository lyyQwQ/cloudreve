package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/entity"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/ent/metadata"
	"github.com/cloudreve/Cloudreve/v4/ent/node"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

var ErrUnsupportedCodec = errors.New("unsupported codec")

const (
	VideoSubtitleBurnTaskType = "video_subtitle_burn"
	VideoHLSSliceTaskType     = "video_hls_slice"

	VideoSubtitleModeAuto     = "auto"
	VideoSubtitleModeExternal = "external"
	VideoSubtitleModeEmbedded = "embedded"

	hlsAvailableMetadataKey   = "hls:available"
	hlsAvailableMetadataValue = "1"
	hlsCodecName              = "h264/aac"

	videoFFMpegThreadsSettingName = "video_ffmpeg_threads"
	videoFFMpegNiceSettingName    = "video_ffmpeg_nice"
	videoFFMpegThreadsDefault     = 1
	videoFFMpegNiceDefault        = 10
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
	ProgressCurrent int64                `json:"progress_current,omitempty"`
	ProgressTotal   int64                `json:"progress_total,omitempty"`
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

	_, input, err := resolveVideoTaskInput(ctx, dep, state.FileID)
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}

	filterArg, modeUsed, err := buildSubtitleFilterArg(input, state.Subtitle)
	if err != nil {
		return task.StatusError, wrapVideoTaskErr(err)
	}

	t.updateProgress(2, 4)

	outputPath := buildSubtitleBurnOutputPath(state.FileID)
	ffmpegStderr, err := runSubtitleBurnFFMpeg(ctx, input, filterArg, outputPath)
	if err != nil {
		logger.Error("Video subtitle ffmpeg failed task_type=%s file_id=%d mode=%s stderr=%s err=%v", t.Type(), state.FileID, modeUsed, ffmpegStderr, err)
		return task.StatusError, wrapVideoTaskErr(err)
	}

	t.updateProgress(3, 4)

	if _, err := os.Stat(outputPath); err != nil {
		return task.StatusError, wrapVideoTaskErr(fmt.Errorf("subtitle output missing: %w", err))
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

	vCodec, aCodec, probeStderr, err := probeVideoCodecs(ctx, input)
	if err != nil {
		logger.Error("Video hls precheck failed task_type=%s file_id=%d stderr=%s err=%v", t.Type(), state.FileID, probeStderr, err)
		return task.StatusError, wrapVideoTaskErr(err)
	}
	if !strings.EqualFold(vCodec, "h264") || !strings.EqualFold(aCodec, "aac") {
		unsupported := fmt.Errorf("%w: video codec=%q, audio codec=%q", ErrUnsupportedCodec, vCodec, aCodec)
		return task.StatusError, wrapVideoTaskErr(unsupported)
	}

	outputDir := buildHLSOutputDir(state.FileID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return task.StatusError, wrapVideoTaskErr(fmt.Errorf("failed to create hls output dir: %w", err))
	}

	playlistPath := filepath.Join(outputDir, "index.m3u8")
	segmentPattern := filepath.Join(outputDir, "segment_%05d.ts")
	ffmpegStderr, err := runHLSFFMpeg(ctx, input, playlistPath, segmentPattern)
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
	if parsed, err := ParseVideoTaskState(state); err == nil {
		progress.Identifier = strconv.Itoa(parsed.FileID)
		if parsed.ProgressTotal > 0 {
			progress.Total = parsed.ProgressTotal
		}
		if parsed.ProgressCurrent > 0 {
			progress.Current = parsed.ProgressCurrent
		}
	}

	return Progresses{taskType: progress}
}

func (t *VideoSubtitleBurnTask) Progress(_ context.Context) Progresses {
	return buildVideoTaskProgress(VideoSubtitleBurnTaskType, t.State())
}

func (t *VideoHLSSliceTask) Progress(_ context.Context) Progresses {
	return buildVideoTaskProgress(VideoHLSSliceTaskType, t.State())
}

type videoTaskDep interface {
	FileClient() inventory.FileClient
	DBClient() *ent.Client
}

type ffprobeCodecPayload struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
	} `json:"streams"`
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

func probeVideoCodecs(ctx context.Context, input string) (string, string, string, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "warning",
		"-print_format", "json",
		"-show_streams",
		input,
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	stderrText := strings.TrimSpace(stderr.String())
	if err != nil {
		return "", "", stderrText, fmt.Errorf("failed to invoke ffprobe: %w (stderr: %s)", err, stderrText)
	}

	var payload ffprobeCodecPayload
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return "", "", stderrText, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	var videoCodec string
	var audioCodec string
	for _, stream := range payload.Streams {
		switch strings.ToLower(strings.TrimSpace(stream.CodecType)) {
		case "video":
			if videoCodec == "" {
				videoCodec = strings.TrimSpace(stream.CodecName)
			}
		case "audio":
			if audioCodec == "" {
				audioCodec = strings.TrimSpace(stream.CodecName)
			}
		}
	}

	return videoCodec, audioCodec, stderrText, nil
}

func buildHLSOutputDir(fileID int) string {
	return filepath.Join(os.TempDir(), "cloudreve-hls", strconv.Itoa(fileID), strconv.FormatInt(time.Now().UnixNano(), 10))
}

func buildSubtitleBurnOutputPath(fileID int) string {
	return filepath.Join(os.TempDir(), "cloudreve-subtitle-burn", strconv.Itoa(fileID), fmt.Sprintf("burned_%d.mp4", time.Now().UnixNano()))
}

func runHLSFFMpeg(ctx context.Context, input, playlistPath, segmentPattern string) (string, error) {
	args := []string{
		"-v", "warning",
		"-y",
		"-i", input,
		"-codec", "copy",
		"-hls_time", "10",
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", segmentPattern,
		playlistPath,
	}

	cmd := newVideoFFMpegCommand(ctx, args)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	stderrText := strings.TrimSpace(stderr.String())
	if err != nil {
		return stderrText, fmt.Errorf("failed to invoke ffmpeg: %w (stderr: %s)", err, stderrText)
	}

	return stderrText, nil
}

func runSubtitleBurnFFMpeg(ctx context.Context, input, filterArg, output string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return "", fmt.Errorf("failed to create subtitle output dir: %w", err)
	}

	args := []string{
		"-v", "warning",
		"-y",
		"-i", input,
		"-vf", filterArg,
		"-c:v", "libx264",
		"-c:a", "copy",
		output,
	}

	cmd := newVideoFFMpegCommand(ctx, args)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	stderrText := strings.TrimSpace(stderr.String())
	if err != nil {
		return stderrText, fmt.Errorf("failed to invoke ffmpeg: %w (stderr: %s)", err, stderrText)
	}

	return stderrText, nil
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

func readVideoFFMpegSettingInt(ctx context.Context, dep videoTaskDep, name string, defaultValue int) int {
	v, err := dep.DBClient().Setting.Query().Where(setting.Name(name)).Only(ctx)
	if err != nil {
		return defaultValue
	}

	parsed, err := strconv.Atoi(strings.TrimSpace(v.Value))
	if err != nil {
		return defaultValue
	}

	return parsed
}

func buildSubtitleFilterArg(input string, option *VideoSubtitleOption) (string, string, error) {
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
			return "subtitles=" + escapeFFMpegSubtitlePath(externalPath), VideoSubtitleModeExternal, nil
		}

		return "subtitles=" + escapeFFMpegSubtitlePath(input) + ":si=0", VideoSubtitleModeEmbedded, nil
	case VideoSubtitleModeExternal:
		if option == nil {
			return "", "", fmt.Errorf("missing subtitle option for external mode (%w)", CriticalErr)
		}

		externalPath, err := resolveExternalSubtitlePath(input, option.ExternalName)
		if err != nil {
			return "", "", err
		}

		return "subtitles=" + escapeFFMpegSubtitlePath(externalPath), VideoSubtitleModeExternal, nil
	case VideoSubtitleModeEmbedded:
		if option == nil || option.EmbeddedIndex == nil {
			return "", "", fmt.Errorf("missing subtitle embedded index (%w)", CriticalErr)
		}
		if *option.EmbeddedIndex < 0 {
			return "", "", fmt.Errorf("subtitle embedded index must be >= 0 (%w)", CriticalErr)
		}

		return fmt.Sprintf("subtitles=%s:si=%d", escapeFFMpegSubtitlePath(input), *option.EmbeddedIndex), VideoSubtitleModeEmbedded, nil
	default:
		return "", "", fmt.Errorf("invalid subtitle mode %q (%w)", mode, CriticalErr)
	}
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

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".srt" && ext != ".ass" && ext != ".ssa" {
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
	replacer := strings.NewReplacer(
		`\\`, `\\\\`,
		`:`, `\\:`,
		`'`, `\\'`,
		`,`, `\\,`,
		`[`, `\\[`,
		`]`, `\\]`,
	)

	return replacer.Replace(input)
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
	existing, err := dep.DBClient().HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileModel.ID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			if _, err := dep.DBClient().HLSArtifact.Create().
				SetSourceFileID(fileModel.ID).
				SetStoragePath(outputDir).
				SetSegmentCount(segmentCount).
				SetTotalSize(totalSize).
				SetCodec(hlsCodecName).
				Save(ctx); err != nil {
				return fmt.Errorf("failed to create hls artifact: %w", err)
			}
		} else {
			return fmt.Errorf("failed to query hls artifact: %w", err)
		}
	} else {
		if _, err := dep.DBClient().HLSArtifact.UpdateOne(existing).
			SetStoragePath(outputDir).
			SetSegmentCount(segmentCount).
			SetTotalSize(totalSize).
			SetCodec(hlsCodecName).
			Save(ctx); err != nil {
			return fmt.Errorf("failed to update hls artifact: %w", err)
		}
	}

	if err := dep.FileClient().UpsertMetadata(ctx, fileModel, map[string]string{hlsAvailableMetadataKey: hlsAvailableMetadataValue}, nil); err != nil {
		return fmt.Errorf("failed to persist hls metadata: %w", err)
	}

	if _, err := dep.DBClient().Metadata.Query().Where(metadata.FileID(fileModel.ID), metadata.Name(hlsAvailableMetadataKey)).Only(ctx); err != nil {
		return fmt.Errorf("failed to verify hls metadata: %w", err)
	}

	return nil
}

func (t *VideoHLSSliceTask) updateProgress(current, total int64) {
	updateVideoTaskProgress(t.DBTask, current, total)
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
