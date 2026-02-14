package queue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	dep := &videoTaskTestDep{client: client, fileClient: inventory.NewFileClient(client, conf.SQLiteDB, hasher)}

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
	for _, expected := range []string{"-codec copy", "-hls_time 10", "-hls_playlist_type vod"} {
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

	meta, err := dep.client.Metadata.Query().Where(metadata.FileID(fileID), metadata.Name(hlsAvailableMetadataKey)).Only(context.Background())
	if err != nil {
		t.Fatalf("query metadata: %v", err)
	}
	if meta.Value != hlsAvailableMetadataValue {
		t.Fatalf("unexpected metadata value: %q", meta.Value)
	}

	phase := tk.Progress(context.Background())[VideoHLSSliceTaskType]
	if phase == nil || phase.Identifier == "" || phase.Total <= 0 || phase.Current <= 0 {
		t.Fatalf("unexpected progress: %+v", phase)
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
	for _, expected := range []string{"-vf subtitles=", "movie.zh.srt", "-c:v libx264", "-c:a copy"} {
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
}

func TestVideoSubtitleBurnTask_DoInvalidSubtitleSelectionIsCritical(t *testing.T) {
	dep, _, fileID := newVideoTaskTestFixture(t)

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
