package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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

type videoTaskTestDep struct {
	client     *ent.Client
	fileClient inventory.FileClient
	userClient inventory.UserClient
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
		client:     client,
		fileClient: inventory.NewFileClient(client, conf.SQLiteDB, hasher),
		userClient: inventory.NewUserClient(client),
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
	for _, expected := range []string{"-vf subtitles=", "movie.zh.srt", "force_style='FontSize=22,MarginV=28,Outline=0.3,Shadow=1'", "-c:v libx264", "-c:a copy"} {
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
	if !strings.Contains(args, ":si=3") {
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

func TestEscapeFFMpegSubtitlePath_EscapesSemicolon(t *testing.T) {
	raw := "/tmp/a;b.srt"
	escaped := escapeFFMpegSubtitlePath(raw)
	if !strings.Contains(escaped, `a\\;b.srt`) {
		t.Fatalf("expected semicolon escaped, got %q", escaped)
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
