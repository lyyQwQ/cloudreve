package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/enttest"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/ent/metadata"
	"github.com/cloudreve/Cloudreve/v4/ent/node"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	settingpkg "github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestVideoTaskFactories(t *testing.T) {
	model := &ent.Task{
		Type:         VideoSubtitleBurnTaskType,
		Status:       "queued",
		PublicState:  &types.TaskPublicState{},
		PrivateState: `{"file_id":1}`,
	}

	tk, err := NewTaskFromModel(model)
	if err != nil {
		t.Fatalf("NewTaskFromModel: %v", err)
	}

	if tk.Type() != VideoSubtitleBurnTaskType {
		t.Fatalf("unexpected task type: %q", tk.Type())
	}
}

func TestVideoTaskDoWrapsCriticalErrOnBadState(t *testing.T) {
	model := &ent.Task{
		Type:         VideoHLSSliceTaskType,
		Status:       "queued",
		PublicState:  &types.TaskPublicState{},
		PrivateState: "not-json",
	}

	tk := NewVideoHLSSliceTaskFromModel(model)
	_, err := tk.Do(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, CriticalErr) {
		t.Fatalf("expected CriticalErr wrapper, got: %v", err)
	}
}

func TestVideoSubtitleBurnTask_DoWrapsCriticalErrOnBadState(t *testing.T) {
	model := &ent.Task{
		Type:         VideoSubtitleBurnTaskType,
		Status:       "queued",
		PublicState:  &types.TaskPublicState{},
		PrivateState: "not-json",
	}

	tk := NewVideoSubtitleBurnTaskFromModel(model)
	_, err := tk.Do(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, CriticalErr) {
		t.Fatalf("expected CriticalErr wrapper, got: %v", err)
	}
}

func TestWrapVideoTaskErr_UnsupportedCodecIsCritical(t *testing.T) {
	err := wrapVideoTaskErr(ErrUnsupportedCodec)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrUnsupportedCodec) {
		t.Fatalf("expected ErrUnsupportedCodec wrapper, got: %v", err)
	}
	if !errors.Is(err, CriticalErr) {
		t.Fatalf("expected CriticalErr wrapper, got: %v", err)
	}
}

func TestNewVideoSubtitleBurnTask_StateWithSubtitleOption(t *testing.T) {
	idx := 2
	tk, err := NewVideoSubtitleBurnTask(context.Background(), 7, nil, &VideoSubtitleOption{
		Mode:          VideoSubtitleModeEmbedded,
		EmbeddedIndex: &idx,
	})
	if err != nil {
		t.Fatalf("NewVideoSubtitleBurnTask: %v", err)
	}

	state, err := ParseVideoTaskState(tk.State())
	if err != nil {
		t.Fatalf("ParseVideoTaskState: %v", err)
	}

	if state.FileID != 7 {
		t.Fatalf("unexpected file_id: %d", state.FileID)
	}
	if state.Subtitle == nil {
		t.Fatalf("expected subtitle option in state")
	}
	if state.Subtitle.Mode != VideoSubtitleModeEmbedded {
		t.Fatalf("unexpected subtitle mode: %q", state.Subtitle.Mode)
	}
	if state.Subtitle.EmbeddedIndex == nil || *state.Subtitle.EmbeddedIndex != 2 {
		t.Fatalf("unexpected embedded index: %+v", state.Subtitle.EmbeddedIndex)
	}
}

func TestParseVideoTaskState_OldFormatCompatibility(t *testing.T) {
	state, err := ParseVideoTaskState(`{"file_id":99}`)
	if err != nil {
		t.Fatalf("ParseVideoTaskState: %v", err)
	}

	if state.FileID != 99 {
		t.Fatalf("unexpected file_id: %d", state.FileID)
	}
	if state.Subtitle != nil {
		t.Fatalf("expected nil subtitle for old format state")
	}
}

func TestVideoTaskProgressShape(t *testing.T) {
	burnTask, err := NewVideoSubtitleBurnTask(context.Background(), 17, nil, nil)
	if err != nil {
		t.Fatalf("NewVideoSubtitleBurnTask: %v", err)
	}
	hlsTask, err := NewVideoHLSSliceTask(context.Background(), 23, nil)
	if err != nil {
		t.Fatalf("NewVideoHLSSliceTask: %v", err)
	}

	for _, tc := range []struct {
		name       string
		progress   Progresses
		taskType   string
		identifier string
	}{
		{name: "subtitle_burn", progress: burnTask.Progress(context.Background()), taskType: VideoSubtitleBurnTaskType, identifier: "17"},
		{name: "hls_slice", progress: hlsTask.Progress(context.Background()), taskType: VideoHLSSliceTaskType, identifier: "23"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.progress == nil {
				t.Fatalf("expected non-nil progress")
			}
			phase, ok := tc.progress[tc.taskType]
			if !ok {
				t.Fatalf("expected progress key %q, got %#v", tc.taskType, tc.progress)
			}
			if phase == nil {
				t.Fatalf("expected non-nil progress value")
			}
			if phase.Total != 1 || phase.Current != 0 || phase.Identifier != tc.identifier {
				t.Fatalf("unexpected progress value: %+v", phase)
			}
		})
	}
}

func TestVideoTaskProgressShapeIncludesRemoteWorkerPhases(t *testing.T) {
	stateBytes, err := json.Marshal(&VideoTaskState{
		FileID:                  17,
		WorkerTransferPhase:     workerTransferPhaseOutputDownload,
		WorkerTransferProgress:  42.4,
		WorkerTranscodeProgress: 100,
		WorkerOutputSize:        2048,
		WorkerStartedAt:         1714219200,
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	tk := NewVideoSubtitleBurnTaskFromModel(&ent.Task{
		Type:         VideoSubtitleBurnTaskType,
		Status:       task.StatusProcessing,
		PublicState:  &types.TaskPublicState{},
		PrivateState: string(stateBytes),
	})

	progress := tk.Progress(context.Background())
	if got := progress[workerProgressTransfer]; got == nil || got.Current != 42 || got.Total != 100 || got.Identifier != workerTransferPhaseOutputDownload {
		t.Fatalf("unexpected worker transfer progress: %+v", got)
	}
	if got := progress[workerProgressTranscode]; got == nil || got.Current != 100 || got.Total != 100 {
		t.Fatalf("unexpected worker transcode progress: %+v", got)
	}
	if got := progress["ffmpeg"]; got != nil {
		t.Fatalf("worker task should expose transfer/transcode without duplicate ffmpeg progress: %+v", got)
	}

	summary := tk.Summarize(nil)
	if summary.Props["worker_transfer_phase"] != workerTransferPhaseOutputDownload {
		t.Fatalf("expected worker phase in summary, got %+v", summary.Props)
	}
	if summary.Props["worker_started_at"] != int64(1714219200) {
		t.Fatalf("expected worker started_at in summary, got %+v", summary.Props)
	}
}

type videoTaskTestDep struct {
	client          *ent.Client
	fileClient      inventory.FileClient
	userClient      inventory.UserClient
	settingProvider settingpkg.Provider
}

type queueTestSettingAdapter struct {
	values map[string]any
}

func (s *queueTestSettingAdapter) Get(_ context.Context, name string, defaultVal any) any {
	if val, ok := s.values[name]; ok {
		return val
	}

	return defaultVal
}

func (d *videoTaskTestDep) ForkWithLogger(ctx context.Context, l logging.Logger) context.Context {
	ctx = context.WithValue(ctx, logging.LoggerCtx{}, l)
	ctx = context.WithValue(ctx, depCtxKey{}, d)
	return ctx
}

func (d *videoTaskTestDep) FileClient() inventory.FileClient {
	return d.fileClient
}

func (d *videoTaskTestDep) DBClient() *ent.Client {
	return d.client
}

func (d *videoTaskTestDep) UserClient() inventory.UserClient {
	return d.userClient
}

func (d *videoTaskTestDep) SettingProvider() settingpkg.Provider {
	return d.settingProvider
}

func newVideoTaskTestFixture(t *testing.T) (*videoTaskTestDep, *ent.User, int) {
	t.Helper()

	client := enttest.Open(t, "sqlite3", filepath.Join(t.TempDir(), "ent.db"))
	t.Cleanup(func() { client.Close() })

	grp, err := client.Group.Create().SetName("g").SetPermissions(&boolset.BooleanSet{}).Save(context.Background())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	usr, err := client.User.Create().SetEmail("u@example.com").SetNick("u").SetGroupUsers(grp.ID).Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	hasher, err := hashid.New("salt")
	if err != nil {
		t.Fatalf("hashid.New: %v", err)
	}

	dep := &videoTaskTestDep{
		client:          client,
		fileClient:      inventory.NewFileClient(client, conf.SQLiteDB, hasher),
		userClient:      inventory.NewUserClient(client),
		settingProvider: settingpkg.NewProvider(&queueTestSettingAdapter{values: map[string]any{}}),
	}

	policy, err := client.StoragePolicy.Create().
		SetName("local").
		SetType(types.PolicyTypeLocal).
		SetSettings(&types.PolicySetting{}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}

	if _, err := client.Node.Create().
		SetStatus(node.StatusActive).
		SetName("master").
		SetType(node.TypeMaster).
		SetCapabilities(&boolset.BooleanSet{}).
		SetSettings(&types.NodeSetting{}).
		SetWeight(1).
		Save(context.Background()); err != nil {
		t.Fatalf("create master node: %v", err)
	}

	videoPath := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
		t.Fatalf("write video: %v", err)
	}

	st, err := os.Stat(videoPath)
	if err != nil {
		t.Fatalf("stat video: %v", err)
	}

	entModel, err := client.Entity.Create().
		SetType(int(types.EntityTypeVersion)).
		SetSource(videoPath).
		SetSize(st.Size()).
		SetStoragePolicyEntities(policy.ID).
		SetReferenceCount(1).
		SetCreatedBy(usr.ID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}

	fileModel, err := client.File.Create().
		SetType(int(types.FileTypeFile)).
		SetName("movie.mp4").
		SetOwnerID(usr.ID).
		SetSize(st.Size()).
		SetPrimaryEntity(entModel.ID).
		SetStoragePolicyFiles(policy.ID).
		AddEntities(entModel).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create file: %v", err)
	}

	return dep, usr, fileModel.ID
}

func prepareFakeFFProbe(t *testing.T, dir, stdout, stderr string, exitCode int) {
	t.Helper()

	scriptPath := filepath.Join(dir, "ffprobe")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	if stdout != "" {
		b.WriteString("cat <<'EOF'\n")
		b.WriteString(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("EOF\n")
	}
	if stderr != "" {
		b.WriteString("cat <<'EOF' 1>&2\n")
		b.WriteString(stderr)
		if !strings.HasSuffix(stderr, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("EOF\n")
	}
	b.WriteString(fmt.Sprintf("exit %d\n", exitCode))

	if err := os.WriteFile(scriptPath, []byte(b.String()), 0755); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}
}

func prepareFakeFFMpegSuccess(t *testing.T, dir, argsFile string) {
	t.Helper()

	script := strings.Join([]string{
		"#!/bin/sh",
		"echo \"$@\" > \"" + argsFile + "\"",
		"playlist=\"\"",
		"for arg in \"$@\"; do playlist=\"$arg\"; done",
		"segment_pattern=\"\"",
		"need_segment=0",
		"for arg in \"$@\"; do",
		"  if [ \"$need_segment\" = \"1\" ]; then segment_pattern=\"$arg\"; need_segment=0; continue; fi",
		"  if [ \"$arg\" = \"-hls_segment_filename\" ]; then need_segment=1; fi",
		"done",
		"mkdir -p \"$(dirname \"$playlist\")\"",
		"printf '#EXTM3U\\n#EXT-X-VERSION:3\\n' > \"$playlist\"",
		"if [ -n \"$segment_pattern\" ]; then",
		"  segment_file=$(printf \"$segment_pattern\" 0)",
		"  mkdir -p \"$(dirname \"$segment_file\")\"",
		"  printf 'segment\\n' > \"$segment_file\"",
		"fi",
		"printf 'fake ffmpeg warning\\n' 1>&2",
		"exit 0",
	}, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
}

func prepareFakeFFMpegFail(t *testing.T, dir, stderr string, exitCode int) {
	t.Helper()

	script := "#!/bin/sh\n" +
		"cat <<'EOF' 1>&2\n" + stderr + "\nEOF\n" +
		fmt.Sprintf("exit %d\n", exitCode)

	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
}

func newVideoTaskCtx(dep *videoTaskTestDep) context.Context {
	ctx := context.WithValue(context.Background(), depCtxKey{}, dep)
	ctx = context.WithValue(ctx, logging.LoggerCtx{}, logging.NewConsoleLogger(logging.LevelDebug))
	ctx = context.WithValue(ctx, inventory.UserCtx{}, &ent.User{ID: 1})
	return ctx
}

func TestVideoHLSSliceTask_DoUnsupportedCodecIsCritical(t *testing.T) {
	dep, user, fileID := newVideoTaskTestFixture(t)

	binDir := t.TempDir()
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"hevc"},{"codec_type":"audio","codec_name":"aac"}]}`, "", 0)
	prepareFakeFFMpegSuccess(t, binDir, filepath.Join(t.TempDir(), "unused.txt"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tk, err := NewVideoHLSSliceTask(context.Background(), fileID, user)
	if err != nil {
		t.Fatalf("NewVideoHLSSliceTask: %v", err)
	}

	status, err := tk.Do(newVideoTaskCtx(dep))
	if status != task.StatusError {
		t.Fatalf("expected status error, got %q", status)
	}
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrUnsupportedCodec) {
		t.Fatalf("expected ErrUnsupportedCodec, got %v", err)
	}
	if !errors.Is(err, CriticalErr) {
		t.Fatalf("expected CriticalErr, got %v", err)
	}
}

func TestVideoHLSSliceTask_DoRunsFFMpegAndPersistsHLS(t *testing.T) {
	dep, user, fileID := newVideoTaskTestFixture(t)

	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}`, "", 0)
	prepareFakeFFMpegSuccess(t, binDir, argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tk, err := NewVideoHLSSliceTask(context.Background(), fileID, user)
	if err != nil {
		t.Fatalf("NewVideoHLSSliceTask: %v", err)
	}

	status, err := tk.Do(newVideoTaskCtx(dep))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != task.StatusCompleted {
		t.Fatalf("expected completed, got %q", status)
	}

	argsRaw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	args := string(bytes.TrimSpace(argsRaw))
	for _, expected := range []string{"-c:v copy", "-c:a copy", "-hls_time 10", "-hls_playlist_type vod"} {
		if !strings.Contains(args, expected) {
			t.Fatalf("expected ffmpeg args to contain %q, got %q", expected, args)
		}
	}

	artifact, err := dep.client.HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(context.Background())
	if err != nil {
		t.Fatalf("query hls artifact: %v", err)
	}
	if artifact.StoragePath == "" || artifact.SegmentCount <= 0 || artifact.TotalSize <= 0 || artifact.Codec != hlsCodecName {
		t.Fatalf("unexpected hls artifact: %+v", artifact)
	}

	meta, err := dep.client.Metadata.Query().Where(metadata.FileID(fileID), metadata.Name(inventory.HLSAvailableMetadataKey)).Only(context.Background())
	if err != nil {
		t.Fatalf("query metadata: %v", err)
	}
	if meta.Value != inventory.HLSAvailableMetadataValue {
		t.Fatalf("unexpected metadata value: %q", meta.Value)
	}

	phase := tk.Progress(context.Background())[VideoHLSSliceTaskType]
	if phase == nil || phase.Identifier == "" || phase.Total <= 0 || phase.Current <= 0 {
		t.Fatalf("unexpected progress: %+v", phase)
	}
}

func TestVideoHLSSliceTask_DoTranscodesNonAACAudio(t *testing.T) {
	dep, user, fileID := newVideoTaskTestFixture(t)

	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"mp3"}]}`, "", 0)
	prepareFakeFFMpegSuccess(t, binDir, argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tk, err := NewVideoHLSSliceTask(context.Background(), fileID, user)
	if err != nil {
		t.Fatalf("NewVideoHLSSliceTask: %v", err)
	}

	status, err := tk.Do(newVideoTaskCtx(dep))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != task.StatusCompleted {
		t.Fatalf("expected completed, got %q", status)
	}

	argsRaw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	args := string(bytes.TrimSpace(argsRaw))
	if !strings.Contains(args, "-c:v copy") || !strings.Contains(args, "-c:a aac") {
		t.Fatalf("expected transcode audio args, got %q", args)
	}
	for _, expected := range []string{"-ac 2", "-ar 48000"} {
		if !strings.Contains(args, expected) {
			t.Fatalf("expected ffmpeg args to contain %q, got %q", expected, args)
		}
	}
}

func TestVideoHLSSliceTask_DoTranscodesMultichannelAACToStereo(t *testing.T) {
	dep, user, fileID := newVideoTaskTestFixture(t)

	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac","channels":6}]}`, "", 0)
	prepareFakeFFMpegSuccess(t, binDir, argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tk, err := NewVideoHLSSliceTask(context.Background(), fileID, user)
	if err != nil {
		t.Fatalf("NewVideoHLSSliceTask: %v", err)
	}

	status, err := tk.Do(newVideoTaskCtx(dep))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != task.StatusCompleted {
		t.Fatalf("expected completed, got %q", status)
	}

	argsRaw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	args := string(bytes.TrimSpace(argsRaw))
	for _, expected := range []string{"-c:v copy", "-c:a aac", "-ac 2", "-ar 48000"} {
		if !strings.Contains(args, expected) {
			t.Fatalf("expected ffmpeg args to contain %q, got %q", expected, args)
		}
	}
	if strings.Contains(args, "-c:a copy") {
		t.Fatalf("expected multichannel AAC to be transcoded, got %q", args)
	}
}

func TestVideoHLSSliceTask_DoAllowsNoAudio(t *testing.T) {
	dep, user, fileID := newVideoTaskTestFixture(t)

	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"h264"}]}`, "", 0)
	prepareFakeFFMpegSuccess(t, binDir, argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tk, err := NewVideoHLSSliceTask(context.Background(), fileID, user)
	if err != nil {
		t.Fatalf("NewVideoHLSSliceTask: %v", err)
	}

	status, err := tk.Do(newVideoTaskCtx(dep))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != task.StatusCompleted {
		t.Fatalf("expected completed, got %q", status)
	}

	argsRaw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	args := string(bytes.TrimSpace(argsRaw))
	if !strings.Contains(args, "-c:v copy") || !strings.Contains(args, "-an") {
		t.Fatalf("expected no-audio args, got %q", args)
	}
	if strings.Contains(args, "-c:a") {
		t.Fatalf("unexpected audio codec args for no-audio source, got %q", args)
	}
}

func TestVideoHLSSliceTask_DoErrorIncludesStderr(t *testing.T) {
	dep, user, fileID := newVideoTaskTestFixture(t)

	binDir := t.TempDir()
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}`, "", 0)
	prepareFakeFFMpegFail(t, binDir, "ffmpeg exploded", 1)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tk, err := NewVideoHLSSliceTask(context.Background(), fileID, user)
	if err != nil {
		t.Fatalf("NewVideoHLSSliceTask: %v", err)
	}

	status, err := tk.Do(newVideoTaskCtx(dep))
	if status != task.StatusError {
		t.Fatalf("expected status error, got %q", status)
	}
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "stderr") || !strings.Contains(err.Error(), "ffmpeg exploded") {
		t.Fatalf("expected stderr details in error, got %v", err)
	}
}

func TestVideoSubtitleBurnTask_DoRunsFFMpegExternalSubtitle(t *testing.T) {
	dep, user, fileID := newVideoTaskTestFixture(t)

	_, input, err := resolveVideoTaskInput(context.Background(), dep, fileID)
	if err != nil {
		t.Fatalf("resolveVideoTaskInput: %v", err)
	}

	if err := os.WriteFile(filepath.Join(filepath.Dir(input), "movie.zh.srt"), []byte("1\n00:00:00,000 --> 00:00:01,000\nhello\n"), 0600); err != nil {
		t.Fatalf("write subtitle: %v", err)
	}

	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"h264","height":1080}]}`, "", 0)
	prepareFakeFFMpegSuccess(t, binDir, argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tk, err := NewVideoSubtitleBurnTask(context.Background(), fileID, user, &VideoSubtitleOption{
		Mode:         VideoSubtitleModeExternal,
		ExternalName: "movie.zh.srt",
	})
	if err != nil {
		t.Fatalf("NewVideoSubtitleBurnTask: %v", err)
	}

	status, err := tk.Do(newVideoTaskCtx(dep))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != task.StatusCompleted {
		t.Fatalf("expected completed, got %q", status)
	}

	argsRaw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	args := string(bytes.TrimSpace(argsRaw))
	for _, expected := range []string{"-vf subtitles=filename='", "movie.zh.srt'", "force_style='FontSize=22,MarginV=28,Outline=0.3,Shadow=1'", "-c:v libx264", "-c:a copy"} {
		if !strings.Contains(args, expected) {
			t.Fatalf("expected ffmpeg args to contain %q, got %q", expected, args)
		}
	}

	phase := tk.Progress(context.Background())[VideoSubtitleBurnTaskType]
	if phase == nil || phase.Identifier == "" || phase.Total != 4 || phase.Current != 4 {
		t.Fatalf("unexpected progress: %+v", phase)
	}
}

func TestVideoSubtitleBurnTask_DoBuildsEmbeddedArgs(t *testing.T) {
	dep, user, fileID := newVideoTaskTestFixture(t)

	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"h264","height":1080}]}`, "", 0)
	prepareFakeFFMpegSuccess(t, binDir, argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	idx := 3
	tk, err := NewVideoSubtitleBurnTask(context.Background(), fileID, user, &VideoSubtitleOption{
		Mode:          VideoSubtitleModeEmbedded,
		EmbeddedIndex: &idx,
	})
	if err != nil {
		t.Fatalf("NewVideoSubtitleBurnTask: %v", err)
	}

	status, err := tk.Do(newVideoTaskCtx(dep))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != task.StatusCompleted {
		t.Fatalf("expected completed, got %q", status)
	}

	argsRaw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	args := string(bytes.TrimSpace(argsRaw))
	if !strings.Contains(args, "subtitles=filename='") || !strings.Contains(args, "':si=3") {
		t.Fatalf("expected embedded subtitle ffmpeg arg, got %q", args)
	}
	if strings.Contains(args, "force_style=") {
		t.Fatalf("embedded subtitle should not include force_style, got %q", args)
	}
}

func TestVideoSubtitleBurnTask_DoInvalidSubtitleSelectionIsCritical(t *testing.T) {
	dep, _, fileID := newVideoTaskTestFixture(t)

	binDir := t.TempDir()
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"h264","height":720}]}`, "", 0)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, tc := range []struct {
		name   string
		state  string
		errorS string
	}{
		{
			name:   "invalid mode",
			state:  fmt.Sprintf(`{"file_id":%d,"subtitle":{"mode":"invalid"}}`, fileID),
			errorS: "invalid subtitle mode",
		},
		{
			name:   "external missing name",
			state:  fmt.Sprintf(`{"file_id":%d,"subtitle":{"mode":"external"}}`, fileID),
			errorS: "external_name is required",
		},
		{
			name:   "embedded missing index",
			state:  fmt.Sprintf(`{"file_id":%d,"subtitle":{"mode":"embedded"}}`, fileID),
			errorS: "embedded index",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &ent.Task{
				Type:         VideoSubtitleBurnTaskType,
				Status:       "queued",
				PublicState:  &types.TaskPublicState{},
				PrivateState: tc.state,
			}

			tk := NewVideoSubtitleBurnTaskFromModel(model)
			status, err := tk.Do(newVideoTaskCtx(dep))
			if status != task.StatusError {
				t.Fatalf("expected status error, got %q", status)
			}
			if err == nil {
				t.Fatalf("expected error")
			}
			if !errors.Is(err, CriticalErr) {
				t.Fatalf("expected CriticalErr wrapper, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.errorS) {
				t.Fatalf("expected error containing %q, got: %v", tc.errorS, err)
			}
		})
	}
}

func TestVideoSubtitleBurnTask_DoErrorIncludesStderr(t *testing.T) {
	dep, user, fileID := newVideoTaskTestFixture(t)

	binDir := t.TempDir()
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"h264","height":720}]}`, "", 0)
	prepareFakeFFMpegFail(t, binDir, "subtitle burn failed", 1)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	idx := 0
	tk, err := NewVideoSubtitleBurnTask(context.Background(), fileID, user, &VideoSubtitleOption{
		Mode:          VideoSubtitleModeEmbedded,
		EmbeddedIndex: &idx,
	})
	if err != nil {
		t.Fatalf("NewVideoSubtitleBurnTask: %v", err)
	}

	status, err := tk.Do(newVideoTaskCtx(dep))
	if status != task.StatusError {
		t.Fatalf("expected status error, got %q", status)
	}
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "stderr") || !strings.Contains(err.Error(), "subtitle burn failed") {
		t.Fatalf("expected stderr details in error, got %v", err)
	}
}

func TestResolveSubtitleLanguage_ExternalParsesToken(t *testing.T) {
	lang := resolveSubtitleLanguage(&VideoSubtitleOption{Mode: VideoSubtitleModeExternal, ExternalName: "movie.chs.srt"}, VideoSubtitleModeExternal, "", nil)
	if lang != "chs" {
		t.Fatalf("expected chs, got %q", lang)
	}
}

func TestResolveSubtitleLanguage_EmbeddedUsesProbeLanguage(t *testing.T) {
	var payload ffprobeCodecPayload
	if err := json.Unmarshal([]byte(`{"streams":[{"index":0,"codec_type":"subtitle","tags":{"language":"chi"}}]}`), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	idx := 0
	lang := resolveSubtitleLanguage(&VideoSubtitleOption{Mode: VideoSubtitleModeEmbedded, EmbeddedIndex: &idx}, VideoSubtitleModeEmbedded, "", &payload)
	if lang != "chi" {
		t.Fatalf("expected chi, got %q", lang)
	}
}

func TestResolveSubtitleLanguage_FallbackSub(t *testing.T) {
	lang := resolveSubtitleLanguage(nil, VideoSubtitleModeEmbedded, "", nil)
	if lang != "sub" {
		t.Fatalf("expected sub, got %q", lang)
	}
}

func TestBuildBurnedOutputFileName_SanitizesLanguageToken(t *testing.T) {
	name := buildBurnedOutputFileName("movie.mp4", "../../../tmp")
	if name != "movie_tmp.mp4" {
		t.Fatalf("unexpected sanitized filename: %q", name)
	}
}

func TestBuildBurnedOutputFileName_FallbackLanguage(t *testing.T) {
	name := buildBurnedOutputFileName("movie.mp4", "///")
	if name != "movie_sub.mp4" {
		t.Fatalf("unexpected fallback filename: %q", name)
	}
}

func TestSelectVideoExecutionNode_PreferredAndFallback(t *testing.T) {
	dep, _, _ := newVideoTaskTestFixture(t)

	master, err := dep.client.Node.Query().Where(node.TypeEQ(node.TypeMaster)).Only(context.Background())
	if err != nil {
		t.Fatalf("query master node: %v", err)
	}

	slave, err := dep.client.Node.Create().
		SetStatus(node.StatusActive).
		SetName("slave-1").
		SetType(node.TypeSlave).
		SetServer("http://127.0.0.1").
		SetSlaveKey("x").
		SetCapabilities(&boolset.BooleanSet{}).
		SetSettings(&types.NodeSetting{}).
		SetWeight(1).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create slave node: %v", err)
	}

	selected, fallback, err := selectVideoExecutionNode(context.Background(), dep, slave.ID)
	if err != nil {
		t.Fatalf("selectVideoExecutionNode with preferred slave: %v", err)
	}
	if !fallback {
		t.Fatalf("expected fallback for preferred slave node")
	}
	if selected != master.ID {
		t.Fatalf("expected fallback to master node %d, got %d", master.ID, selected)
	}

	selected, fallback, err = selectVideoExecutionNode(context.Background(), dep, master.ID)
	if err != nil {
		t.Fatalf("selectVideoExecutionNode with preferred master: %v", err)
	}
	if fallback {
		t.Fatalf("expected no fallback for preferred master node")
	}
	if selected != master.ID {
		t.Fatalf("expected selected master node %d, got %d", master.ID, selected)
	}
}

func setVideoFFMpegRuntimeOptions(t *testing.T, dep *videoTaskTestDep, threads, nice int) {
	t.Helper()

	ctx := context.Background()
	if _, err := dep.client.Setting.Create().SetName(videoFFMpegThreadsSettingName).SetValue(strconv.Itoa(threads)).Save(ctx); err != nil {
		t.Fatalf("set %s: %v", videoFFMpegThreadsSettingName, err)
	}
	if _, err := dep.client.Setting.Create().SetName(videoFFMpegNiceSettingName).SetValue(strconv.Itoa(nice)).Save(ctx); err != nil {
		t.Fatalf("set %s: %v", videoFFMpegNiceSettingName, err)
	}
}

func prepareFakeFFMpegCapture(t *testing.T, dir, argsFile string) {
	t.Helper()

	script := strings.Join([]string{
		"#!/bin/sh",
		"echo \"$@\" > \"" + argsFile + "\"",
		"exit 0",
	}, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
}

func prepareFakeNicePassthrough(t *testing.T, dir, argsFile string) {
	t.Helper()

	script := strings.Join([]string{
		"#!/bin/sh",
		"echo \"$@\" > \"" + argsFile + "\"",
		"if [ \"$1\" = \"-n\" ]; then",
		"  shift 2",
		"fi",
		"exec \"$@\"",
	}, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(dir, "nice"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake nice: %v", err)
	}
}

func assertThreadsBeforeInput(t *testing.T, argsRaw string, threads int) {
	t.Helper()

	parts := strings.Fields(strings.TrimSpace(argsRaw))
	threadIndex := -1
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "-threads" {
			threadIndex = i
			if parts[i+1] != strconv.Itoa(threads) {
				t.Fatalf("unexpected thread value, want %d got %q from args %q", threads, parts[i+1], argsRaw)
			}
			break
		}
	}
	if threadIndex == -1 {
		t.Fatalf("expected -threads in args %q", argsRaw)
	}

	inputIndex := -1
	for i := 0; i < len(parts); i++ {
		if parts[i] == "-i" {
			inputIndex = i
			break
		}
	}
	if inputIndex == -1 {
		t.Fatalf("expected -i in args %q", argsRaw)
	}
	if threadIndex >= inputIndex {
		t.Fatalf("expected -threads before -i, args=%q", argsRaw)
	}
}

func TestRunHLSFFMpeg_InjectsThreadsAndUsesNiceWhenAvailable(t *testing.T) {
	dep, _, _ := newVideoTaskTestFixture(t)
	setVideoFFMpegRuntimeOptions(t, dep, 4, 7)

	binDir := t.TempDir()
	niceArgsFile := filepath.Join(t.TempDir(), "nice_args.txt")
	ffmpegArgsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFMpegCapture(t, binDir, ffmpegArgsFile)
	prepareFakeNicePassthrough(t, binDir, niceArgsFile)
	t.Setenv("PATH", binDir)

	playlist := filepath.Join(t.TempDir(), "index.m3u8")
	pattern := filepath.Join(t.TempDir(), "segment_%05d.ts")
	if _, err := runHLSFFMpeg(newVideoTaskCtx(dep), "input.mp4", playlist, pattern, "aac", 2, true); err != nil {
		t.Fatalf("runHLSFFMpeg: %v", err)
	}

	niceArgsRaw, err := os.ReadFile(niceArgsFile)
	if err != nil {
		t.Fatalf("read nice args: %v", err)
	}
	niceArgs := strings.TrimSpace(string(niceArgsRaw))
	if !strings.HasPrefix(niceArgs, "-n 7 ffmpeg ") {
		t.Fatalf("expected nice wrapper args, got %q", niceArgs)
	}

	ffmpegArgsRaw, err := os.ReadFile(ffmpegArgsFile)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	ffmpegArgs := strings.TrimSpace(string(ffmpegArgsRaw))
	assertThreadsBeforeInput(t, ffmpegArgs, 4)
}

func TestBuildSubtitleFilterArg_ForceStyleByResolution(t *testing.T) {
	baseDir := t.TempDir()
	input := filepath.Join(baseDir, "movie.mp4")
	if err := os.WriteFile(input, []byte("video"), 0600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	srtPath := filepath.Join(baseDir, "movie.zh.srt")
	if err := os.WriteFile(srtPath, []byte("1"), 0600); err != nil {
		t.Fatalf("write srt: %v", err)
	}

	filter1080, mode, err := buildSubtitleFilterArg(input, &VideoSubtitleOption{Mode: VideoSubtitleModeExternal, ExternalName: "movie.zh.srt"}, 1080)
	if err != nil {
		t.Fatalf("buildSubtitleFilterArg 1080: %v", err)
	}
	if mode != VideoSubtitleModeExternal {
		t.Fatalf("expected mode external, got %q", mode)
	}
	if !strings.Contains(filter1080, "force_style='FontSize=22,MarginV=28,Outline=0.3,Shadow=1'") {
		t.Fatalf("expected 1080 style, got %q", filter1080)
	}

	filter720, _, err := buildSubtitleFilterArg(input, &VideoSubtitleOption{Mode: VideoSubtitleModeExternal, ExternalName: "movie.zh.srt"}, 720)
	if err != nil {
		t.Fatalf("buildSubtitleFilterArg 720: %v", err)
	}
	if !strings.Contains(filter720, "force_style='FontSize=18,MarginV=20,Outline=0.3,Shadow=1'") {
		t.Fatalf("expected 720 style, got %q", filter720)
	}

	fallbackFilter, _, err := buildSubtitleFilterArg(input, &VideoSubtitleOption{Mode: VideoSubtitleModeExternal, ExternalName: "movie.zh.srt"}, 0)
	if err != nil {
		t.Fatalf("buildSubtitleFilterArg fallback: %v", err)
	}
	if !strings.Contains(fallbackFilter, "force_style='FontSize=18,MarginV=20,Outline=0.3,Shadow=1'") {
		t.Fatalf("expected fallback 720 style, got %q", fallbackFilter)
	}
}

func TestBuildSubtitleFilterArg_AssSubtitleDoesNotUseForceStyle(t *testing.T) {
	baseDir := t.TempDir()
	input := filepath.Join(baseDir, "movie.mp4")
	if err := os.WriteFile(input, []byte("video"), 0600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	assPath := filepath.Join(baseDir, "movie.zh.ass")
	if err := os.WriteFile(assPath, []byte("[Script Info]"), 0600); err != nil {
		t.Fatalf("write ass: %v", err)
	}

	filterArg, mode, err := buildSubtitleFilterArg(input, &VideoSubtitleOption{Mode: VideoSubtitleModeExternal, ExternalName: "movie.zh.ass"}, 1080)
	if err != nil {
		t.Fatalf("buildSubtitleFilterArg ass: %v", err)
	}
	if mode != VideoSubtitleModeExternal {
		t.Fatalf("expected mode external, got %q", mode)
	}
	if strings.Contains(filterArg, "force_style=") {
		t.Fatalf("expected ass without force_style, got %q", filterArg)
	}
}

func TestBuildSubtitleFilterArg_EmbeddedUsesSafeFilenameOption(t *testing.T) {
	baseDir := t.TempDir()
	input := filepath.Join(baseDir, "中文 path [01],semi;Estrella's.mkv")
	if err := os.WriteFile(input, []byte("video"), 0600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	idx := 2
	filterArg, mode, err := buildSubtitleFilterArg(input, &VideoSubtitleOption{
		Mode:          VideoSubtitleModeEmbedded,
		EmbeddedIndex: &idx,
	}, 720)
	if err != nil {
		t.Fatalf("buildSubtitleFilterArg embedded: %v", err)
	}
	if mode != VideoSubtitleModeEmbedded {
		t.Fatalf("expected mode embedded, got %q", mode)
	}
	assertSubtitleFilterHasSafeFilename(t, filterArg, input)
	if !strings.HasSuffix(filterArg, "':si=2") {
		t.Fatalf("expected embedded stream index, got %q", filterArg)
	}
	if strings.Contains(filterArg, "force_style=") {
		t.Fatalf("embedded subtitle should not include force_style, got %q", filterArg)
	}
}

func TestBuildSubtitleFilterArg_AutoFallbackEmbeddedUsesSafeFilenameOption(t *testing.T) {
	baseDir := t.TempDir()
	input := filepath.Join(baseDir, "中文 path [auto],semi;Estrella's.mkv")
	if err := os.WriteFile(input, []byte("video"), 0600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	filterArg, mode, err := buildSubtitleFilterArg(input, nil, 720)
	if err != nil {
		t.Fatalf("buildSubtitleFilterArg auto: %v", err)
	}
	if mode != VideoSubtitleModeEmbedded {
		t.Fatalf("expected embedded fallback, got %q", mode)
	}
	assertSubtitleFilterHasSafeFilename(t, filterArg, input)
	if !strings.HasSuffix(filterArg, "':si=0") {
		t.Fatalf("expected auto fallback stream index, got %q", filterArg)
	}
	if strings.Contains(filterArg, "force_style=") {
		t.Fatalf("auto fallback embedded subtitle should not include force_style, got %q", filterArg)
	}
}

func TestBuildSubtitleFilterArg_ExternalSRTUsesSafeFilenameAndForceStyle(t *testing.T) {
	baseDir := t.TempDir()
	input := filepath.Join(baseDir, "movie.mp4")
	if err := os.WriteFile(input, []byte("video"), 0600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	subtitleName := "字幕 中文 [v2],semi;Estrella's.srt"
	subtitlePath := filepath.Join(baseDir, subtitleName)
	if err := os.WriteFile(subtitlePath, []byte("1"), 0600); err != nil {
		t.Fatalf("write srt: %v", err)
	}

	filterArg, mode, err := buildSubtitleFilterArg(input, &VideoSubtitleOption{Mode: VideoSubtitleModeExternal, ExternalName: subtitleName}, 1080)
	if err != nil {
		t.Fatalf("buildSubtitleFilterArg srt: %v", err)
	}
	if mode != VideoSubtitleModeExternal {
		t.Fatalf("expected mode external, got %q", mode)
	}
	assertSubtitleFilterHasSafeFilename(t, filterArg, subtitlePath)
	if !strings.Contains(filterArg, "force_style='FontSize=22,MarginV=28,Outline=0.3,Shadow=1'") {
		t.Fatalf("expected srt force_style, got %q", filterArg)
	}
}

func TestBuildSubtitleFilterArg_ExternalAssAndSsaUseSafeFilenameWithoutForceStyle(t *testing.T) {
	for _, ext := range []string{".ass", ".ssa"} {
		t.Run(ext, func(t *testing.T) {
			baseDir := t.TempDir()
			input := filepath.Join(baseDir, "movie.mp4")
			if err := os.WriteFile(input, []byte("video"), 0600); err != nil {
				t.Fatalf("write input: %v", err)
			}

			subtitleName := "字幕 中文 [v2],semi;Estrella's" + ext
			subtitlePath := filepath.Join(baseDir, subtitleName)
			if err := os.WriteFile(subtitlePath, []byte("[Script Info]"), 0600); err != nil {
				t.Fatalf("write subtitle: %v", err)
			}

			filterArg, mode, err := buildSubtitleFilterArg(input, &VideoSubtitleOption{Mode: VideoSubtitleModeExternal, ExternalName: subtitleName}, 1080)
			if err != nil {
				t.Fatalf("buildSubtitleFilterArg %s: %v", ext, err)
			}
			if mode != VideoSubtitleModeExternal {
				t.Fatalf("expected mode external, got %q", mode)
			}
			assertSubtitleFilterHasSafeFilename(t, filterArg, subtitlePath)
			if strings.Contains(filterArg, "force_style=") {
				t.Fatalf("expected %s without force_style, got %q", ext, filterArg)
			}
		})
	}
}

func assertSubtitleFilterHasSafeFilename(t *testing.T, filterArg, rawPath string) {
	t.Helper()

	expected := "subtitles=filename='" + escapeFFMpegSubtitlePath(rawPath) + "'"
	if !strings.Contains(filterArg, expected) {
		t.Fatalf("expected safe filename option %q, got %q", expected, filterArg)
	}

	for _, expectedEscape := range []string{"中文", `[`, `]`, `,`, `;`, `'` + `\\\` + `''`} {
		if !strings.Contains(filterArg, expectedEscape) {
			t.Fatalf("expected filter arg to contain %q, got %q", expectedEscape, filterArg)
		}
	}
}

func TestEscapeFFMpegSubtitlePath_QuotedValueParsesWithFFMpeg(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not available")
	}

	helpCmd := exec.Command(ffmpegPath, "-hide_banner", "-h", "filter=metadata")
	if output, err := helpCmd.CombinedOutput(); err != nil || !strings.Contains(string(output), "Filter metadata") {
		t.Skip("ffmpeg metadata filter is not available")
	}

	rawPath := "/tmp/中文 path [01],semi;Estrella's:part.srt"
	filterArg := "metadata=mode=add:key=k:value='" + escapeFFMpegSubtitlePath(rawPath) + "':function=same_str,metadata=mode=print:file=-"
	cmd := exec.Command(
		ffmpegPath,
		"-hide_banner",
		"-v", "info",
		"-f", "lavfi",
		"-i", "color=s=16x16:d=0.1",
		"-vf", filterArg,
		"-frames:v", "1",
		"-f", "null",
		"-",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg metadata parse failed: %v\nfilter=%s\noutput=%s", err, filterArg, output)
	}

	if !strings.Contains(string(output), "k="+rawPath) {
		t.Fatalf("expected ffmpeg to parse subtitle path %q from filter %q, got output:\n%s", rawPath, filterArg, output)
	}
}

func TestReadFFMpegProgressOutputAcceptsOutTimeMS(t *testing.T) {
	var got []float64
	progressEnd, err := readFFMpegProgressOutput(strings.NewReader("out_time_ms=5000000\nprogress=end\n"), 10, func(pct float64) {
		got = append(got, pct)
	})
	if err != nil {
		t.Fatalf("readFFMpegProgressOutput: %v", err)
	}
	if !progressEnd {
		t.Fatalf("expected progress end")
	}
	if len(got) != 1 || got[0] != 50 {
		t.Fatalf("unexpected progress values: %#v", got)
	}
}

func TestRunSubtitleBurnFFMpeg_DisablesThreadsAndNiceWhenZero(t *testing.T) {
	dep, _, _ := newVideoTaskTestFixture(t)
	setVideoFFMpegRuntimeOptions(t, dep, 0, 0)

	binDir := t.TempDir()
	ffmpegArgsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFMpegCapture(t, binDir, ffmpegArgsFile)
	t.Setenv("PATH", binDir)

	if _, err := runSubtitleBurnFFMpeg(newVideoTaskCtx(dep), "input.mp4", "subtitles=test.srt", filepath.Join(t.TempDir(), "output.mp4"), 0, 0, nil); err != nil {
		t.Fatalf("runSubtitleBurnFFMpeg: %v", err)
	}

	ffmpegArgsRaw, err := os.ReadFile(ffmpegArgsFile)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	ffmpegArgs := strings.TrimSpace(string(ffmpegArgsRaw))
	if strings.Contains(ffmpegArgs, "-threads") {
		t.Fatalf("unexpected -threads when disabled, args=%q", ffmpegArgs)
	}
	if !strings.Contains(ffmpegArgs, "-vf subtitles=test.srt") {
		t.Fatalf("expected subtitle filter arg, got %q", ffmpegArgs)
	}
	if !strings.Contains(ffmpegArgs, "-crf 18") || !strings.Contains(ffmpegArgs, "-preset medium") {
		t.Fatalf("expected hardcoded quality args, got %q", ffmpegArgs)
	}
	if !strings.Contains(ffmpegArgs, "-movflags +faststart") {
		t.Fatalf("expected faststart movflags, got %q", ffmpegArgs)
	}
}

func TestRunSubtitleBurnFFMpeg_FallbackToFFMpegWhenNiceUnavailable(t *testing.T) {
	dep, _, _ := newVideoTaskTestFixture(t)
	setVideoFFMpegRuntimeOptions(t, dep, 2, 5)

	binDir := t.TempDir()
	ffmpegArgsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFMpegCapture(t, binDir, ffmpegArgsFile)
	t.Setenv("PATH", binDir)

	if _, err := runSubtitleBurnFFMpeg(newVideoTaskCtx(dep), "input.mp4", "subtitles=test.srt", filepath.Join(t.TempDir(), "output.mp4"), 0, 0, nil); err != nil {
		t.Fatalf("runSubtitleBurnFFMpeg: %v", err)
	}

	ffmpegArgsRaw, err := os.ReadFile(ffmpegArgsFile)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	assertThreadsBeforeInput(t, strings.TrimSpace(string(ffmpegArgsRaw)), 2)
}

func TestRunSubtitleBurnFFMpeg_AppendsVBVArgsWhenBitrateProvided(t *testing.T) {
	dep, _, _ := newVideoTaskTestFixture(t)
	setVideoFFMpegRuntimeOptions(t, dep, 0, 0)

	binDir := t.TempDir()
	ffmpegArgsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFMpegCapture(t, binDir, ffmpegArgsFile)
	t.Setenv("PATH", binDir)

	if _, err := runSubtitleBurnFFMpeg(newVideoTaskCtx(dep), "input.mp4", "subtitles=test.srt", filepath.Join(t.TempDir(), "output.mp4"), 0, 2_000_000, nil); err != nil {
		t.Fatalf("runSubtitleBurnFFMpeg: %v", err)
	}

	ffmpegArgsRaw, err := os.ReadFile(ffmpegArgsFile)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	ffmpegArgs := strings.TrimSpace(string(ffmpegArgsRaw))
	if !strings.Contains(ffmpegArgs, "-maxrate 2000000") {
		t.Fatalf("expected -maxrate to be injected, args=%q", ffmpegArgs)
	}
	if !strings.Contains(ffmpegArgs, "-bufsize 4000000") {
		t.Fatalf("expected -bufsize to be injected, args=%q", ffmpegArgs)
	}
}

func TestShouldUseRemoteSubtitleBurnSelection(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mp4")
	if err := os.WriteFile(input, []byte("video"), 0600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	cfg := &settingpkg.RemoteFFMpegWorker{Enabled: true, Endpoint: "https://worker.example.com", APIKey: "secret"}
	idx := 2
	if !shouldUseRemoteSubtitleBurn(input, &VideoSubtitleOption{Mode: VideoSubtitleModeEmbedded, EmbeddedIndex: &idx}, VideoSubtitleModeEmbedded, cfg) {
		t.Fatalf("expected embedded mode to use remote worker")
	}
	if shouldUseRemoteSubtitleBurn(input, &VideoSubtitleOption{Mode: VideoSubtitleModeExternal, ExternalName: "movie.srt"}, VideoSubtitleModeExternal, cfg) {
		t.Fatalf("expected external mode to stay local")
	}
	if shouldUseRemoteSubtitleBurn(input, nil, VideoSubtitleModeEmbedded, cfg) {
		t.Fatalf("expected auto mode without explicit embedded index to stay local")
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.srt"), []byte("subtitle"), 0600); err != nil {
		t.Fatalf("write subtitle: %v", err)
	}
	if shouldUseRemoteSubtitleBurn(input, nil, VideoSubtitleModeEmbedded, cfg) {
		t.Fatalf("expected auto mode with external subtitles to stay local")
	}
	if shouldUseRemoteSubtitleBurn(input, &VideoSubtitleOption{Mode: VideoSubtitleModeEmbedded, EmbeddedIndex: &idx}, VideoSubtitleModeEmbedded, &settingpkg.RemoteFFMpegWorker{}) {
		t.Fatalf("expected disabled worker to stay local")
	}
}

func TestPollRemoteWorkerJobRejectsUnknownStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"job_id":"job-1","status":"paused"}`))
	}))
	defer server.Close()

	taskRef := NewVideoSubtitleBurnTaskFromModel(&ent.Task{
		Type:         VideoSubtitleBurnTaskType,
		Status:       task.StatusProcessing,
		PublicState:  &types.TaskPublicState{},
		PrivateState: `{"file_id":1}`,
	})
	cfg := &settingpkg.RemoteFFMpegWorker{Endpoint: server.URL, APIKey: "secret"}
	if _, err := pollRemoteWorkerJob(context.Background(), taskRef, cfg, "job-1"); err == nil || !strings.Contains(err.Error(), "unknown status") {
		t.Fatalf("expected unknown status error, got %v", err)
	}
}

func TestDownloadRemoteWorkerOutputResumesPartFile(t *testing.T) {
	output := []byte("0123456789")
	var rangeHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(output)))
			return
		case http.MethodGet:
			rangeHeader = r.Header.Get("Range")
			w.Header().Set("Accept-Ranges", "bytes")
			if rangeHeader == "bytes=4-" {
				w.Header().Set("Content-Range", "bytes 4-9/10")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(output[4:])
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(output)))
			_, _ = w.Write(output)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	taskRef := NewVideoSubtitleBurnTaskFromModel(&ent.Task{
		Type:         VideoSubtitleBurnTaskType,
		Status:       task.StatusProcessing,
		PublicState:  &types.TaskPublicState{},
		PrivateState: `{"file_id":1}`,
	})
	outPath := filepath.Join(t.TempDir(), "remote-output.mp4")
	if err := os.WriteFile(outPath+".part", output[:4], 0600); err != nil {
		t.Fatalf("write part: %v", err)
	}

	cfg := &settingpkg.RemoteFFMpegWorker{Endpoint: server.URL, APIKey: "secret"}
	if err := downloadRemoteWorkerOutput(context.Background(), taskRef, cfg, "job-1", outPath, int64(len(output))); err != nil {
		t.Fatalf("downloadRemoteWorkerOutput: %v", err)
	}
	if rangeHeader != "bytes=4-" {
		t.Fatalf("expected resume range bytes=4-, got %q", rangeHeader)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != string(output) {
		t.Fatalf("output mismatch: %q", got)
	}
	if _, err := os.Stat(outPath + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("part file should be renamed, stat err=%v", err)
	}

	state, err := ParseVideoTaskState(taskRef.State())
	if err != nil {
		t.Fatalf("ParseVideoTaskState: %v", err)
	}
	if state.WorkerTransferPhase != workerTransferPhaseOutputDownload || state.WorkerTransferProgress != 100 {
		t.Fatalf("unexpected worker progress state: %+v", state)
	}
}

func TestDownloadRemoteWorkerOutputRestartsWhenRangeIgnored(t *testing.T) {
	output := []byte("0123456789")
	var rangeHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(output)))
			return
		case http.MethodGet:
			rangeHeader = r.Header.Get("Range")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(output)))
			_, _ = w.Write(output)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	taskRef := NewVideoSubtitleBurnTaskFromModel(&ent.Task{
		Type:         VideoSubtitleBurnTaskType,
		Status:       task.StatusProcessing,
		PublicState:  &types.TaskPublicState{},
		PrivateState: `{"file_id":1}`,
	})
	outPath := filepath.Join(t.TempDir(), "remote-output.mp4")
	if err := os.WriteFile(outPath+".part", output[:4], 0600); err != nil {
		t.Fatalf("write part: %v", err)
	}

	cfg := &settingpkg.RemoteFFMpegWorker{Endpoint: server.URL, APIKey: "secret"}
	if err := downloadRemoteWorkerOutput(context.Background(), taskRef, cfg, "job-1", outPath, int64(len(output))); err != nil {
		t.Fatalf("downloadRemoteWorkerOutput: %v", err)
	}
	if rangeHeader != "bytes=4-" {
		t.Fatalf("expected resume range bytes=4-, got %q", rangeHeader)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != string(output) {
		t.Fatalf("output should be restarted instead of appended, got %q", got)
	}
}

func TestDownloadRemoteWorkerOutputRequiresKnownSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		if r.Method != http.MethodHead {
			t.Fatalf("unexpected method %s", r.Method)
		}
		w.Header().Set("Accept-Ranges", "bytes")
	}))
	defer server.Close()

	taskRef := NewVideoSubtitleBurnTaskFromModel(&ent.Task{
		Type:         VideoSubtitleBurnTaskType,
		Status:       task.StatusProcessing,
		PublicState:  &types.TaskPublicState{},
		PrivateState: `{"file_id":1}`,
	})
	outPath := filepath.Join(t.TempDir(), "remote-output.mp4")
	cfg := &settingpkg.RemoteFFMpegWorker{Endpoint: server.URL, APIKey: "secret"}

	err := downloadRemoteWorkerOutput(context.Background(), taskRef, cfg, "job-1", outPath, 0)
	if err == nil || !strings.Contains(err.Error(), "output size is unknown") {
		t.Fatalf("expected unknown size error, got %v", err)
	}
}

func TestDownloadRemoteWorkerOutputRetriesInterruptedStream(t *testing.T) {
	oldDelays := remoteWorkerOutputDownloadRetryDelays
	remoteWorkerOutputDownloadRetryDelays = []time.Duration{0}
	t.Cleanup(func() { remoteWorkerOutputDownloadRetryDelays = oldDelays })

	output := []byte("0123456789")
	var getCount int
	var ranges []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(output)))
			return
		case http.MethodGet:
			getCount++
			ranges = append(ranges, r.Header.Get("Range"))
			w.Header().Set("Accept-Ranges", "bytes")
			if getCount == 1 {
				w.Header().Set("Content-Length", strconv.Itoa(len(output)))
				_, _ = w.Write(output[:4])
				return
			}
			if r.Header.Get("Range") != "bytes=4-" {
				t.Fatalf("expected resumed range bytes=4-, got %q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 4-9/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(output[4:])
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	taskRef := NewVideoSubtitleBurnTaskFromModel(&ent.Task{
		Type:         VideoSubtitleBurnTaskType,
		Status:       task.StatusProcessing,
		PublicState:  &types.TaskPublicState{},
		PrivateState: `{"file_id":1}`,
	})
	outPath := filepath.Join(t.TempDir(), "remote-output.mp4")
	cfg := &settingpkg.RemoteFFMpegWorker{Endpoint: server.URL, APIKey: "secret"}

	if err := downloadRemoteWorkerOutput(context.Background(), taskRef, cfg, "job-1", outPath, int64(len(output))); err != nil {
		t.Fatalf("downloadRemoteWorkerOutput: %v", err)
	}
	if getCount != 2 {
		t.Fatalf("expected two GET attempts, got %d ranges=%v", getCount, ranges)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != string(output) {
		t.Fatalf("output mismatch: %q", got)
	}
}

func TestRemoteWorkerOutputDownloadDefaultAttemptsAtLeastTen(t *testing.T) {
	if got := len(remoteWorkerOutputDownloadRetryDelays) + 1; got < 10 {
		t.Fatalf("remote worker output download should allow at least 10 total attempts, got %d", got)
	}
}

func TestVideoSubtitleBurnTask_DoRemoteOutputFailureDoesNotFallbackLocal(t *testing.T) {
	oldDelays := remoteWorkerOutputDownloadRetryDelays
	remoteWorkerOutputDownloadRetryDelays = []time.Duration{0, 0}
	t.Cleanup(func() { remoteWorkerOutputDownloadRetryDelays = oldDelays })

	var outputGetCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") && r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs/embedded-subtitle-burn-url":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/job-1":
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"completed","progress":100,"output_size":10}`))
		case r.Method == http.MethodHead && r.URL.Path == "/v1/jobs/job-1/output":
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", "10")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/job-1/output":
			outputGetCount++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("temporary output download error"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	dep, user, fileID := newVideoTaskTestFixture(t)
	dep.settingProvider = settingpkg.NewProvider(&queueTestSettingAdapter{values: map[string]any{
		"siteURL":                           server.URL,
		"secret_key":                        "source-secret",
		"video_ffmpeg_worker_enabled":       "1",
		"video_ffmpeg_worker_endpoint":      server.URL,
		"video_ffmpeg_worker_api_key":       "secret",
		"video_ffmpeg_worker_timeout":       "60",
		"video_ffmpeg_worker_poll_interval": "0",
	}})

	binDir := t.TempDir()
	ffmpegArgsFile := filepath.Join(t.TempDir(), "ffmpeg_args.txt")
	prepareFakeFFProbe(t, binDir, `{"streams":[{"codec_type":"video","codec_name":"h264","height":1080}],"format":{"duration":"10","bit_rate":"1000000"}}`, "", 0)
	prepareFakeFFMpegSuccess(t, binDir, ffmpegArgsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	idx := 0
	tk, err := NewVideoSubtitleBurnTask(context.Background(), fileID, user, &VideoSubtitleOption{
		Mode:          VideoSubtitleModeEmbedded,
		EmbeddedIndex: &idx,
	})
	if err != nil {
		t.Fatalf("NewVideoSubtitleBurnTask: %v", err)
	}

	status, err := tk.Do(newVideoTaskCtx(dep))
	if status != task.StatusError {
		t.Fatalf("expected status error, got %q", status)
	}
	if err == nil || !strings.Contains(err.Error(), "remote worker output download failed") {
		t.Fatalf("expected remote output download error, got %v", err)
	}
	if outputGetCount != 3 {
		t.Fatalf("expected three output GET attempts, got %d", outputGetCount)
	}
	if _, err := os.Stat(ffmpegArgsFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local ffmpeg should not run after remote job start, stat err=%v", err)
	}
}

func TestRedactRemoteWorkerTextRemovesSensitiveValues(t *testing.T) {
	cfg := &settingpkg.RemoteFFMpegWorker{APIKey: "secret-token"}
	input := "Authorization: Bearer secret-token failed https://cloudreve.example.com/api/v4/video/worker/source/1?signature=abc123&file_id=2"
	got := redactRemoteWorkerText(cfg, input)
	if strings.Contains(got, "secret-token") || strings.Contains(got, "abc123") || strings.Contains(got, "file_id=2") {
		t.Fatalf("sensitive value was not redacted: %s", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("expected redaction marker, got %s", got)
	}

	escapedInput := `{"source_url":"https:\/\/cloudreve.example.com\/api\/v4\/video\/worker\/source\/1?expires=1\u0026signature=abc123\u0026file_id=2","error":"Authorization: Bearer secret-token"}`
	got = redactRemoteWorkerText(cfg, escapedInput)
	if strings.Contains(got, "secret-token") || strings.Contains(got, "abc123") || strings.Contains(got, "file_id=2") {
		t.Fatalf("escaped sensitive value was not redacted: %s", got)
	}
}
