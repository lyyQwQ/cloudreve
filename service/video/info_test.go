package video

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/ffmpegworker"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gofrs/uuid"
)

type videoInfoResponse struct {
	Code  int    `json:"code"`
	Msg   string `json:"msg"`
	Error string `json:"error"`
	Data  struct {
		Codec         string  `json:"codec"`
		AudioCodec    string  `json:"audio_codec"`
		Resolution    string  `json:"resolution"`
		Duration      float64 `json:"duration"`
		Bitrate       int64   `json:"bitrate"`
		HLSCompatible bool    `json:"hls_compatible"`
		Subtitles     struct {
			External []struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"external"`
			Embedded []struct {
				Index    int    `json:"index"`
				Language string `json:"language"`
				Title    string `json:"title"`
			} `json:"embedded"`
		} `json:"subtitles"`
	} `json:"data"`
}

func TestVideoInfo_SuccessAndSubtitles(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()
	r := newTestRouter(dep, user)

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "movie.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.srt"), []byte("1"), 0600); err != nil {
		t.Fatalf("write srt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.ass"), []byte("1"), 0600); err != nil {
		t.Fatalf("write ass: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.SSA"), []byte("1"), 0600); err != nil {
		t.Fatalf("write ssa: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("1"), 0600); err != nil {
		t.Fatalf("write ignore: %v", err)
	}

	fileID := mustCreateVideoFileFixture(t, client, user.ID, videoPath)

	setFakeFFProbe(t, `
{
  "format": {"duration": "123.45", "bit_rate": "5000000"},
  "streams": [
    {"index": 0, "codec_name": "h264", "codec_type": "video", "width": 1280, "height": 720},
    {"index": 1, "codec_name": "aac", "codec_type": "audio"},
    {"index": 2, "codec_name": "subrip", "codec_type": "subtitle", "tags": {"language": "eng", "title": "English"}},
    {"index": 3, "codec_name": "ass", "codec_type": "subtitle", "tags": {"language": "chi", "title": "Chinese"}}
  ]
}
`, "warning: sample", 0)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v4/video/info", bytes.NewBufferString(fmt.Sprintf(`{"file_id":%d}`, fileID)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp videoInfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}

	if resp.Code != 0 {
		t.Fatalf("expected code=0, got %d, body=%s", resp.Code, w.Body.String())
	}
	if resp.Data.Codec != "h264" || resp.Data.AudioCodec != "aac" {
		t.Fatalf("unexpected codecs: video=%q audio=%q", resp.Data.Codec, resp.Data.AudioCodec)
	}
	if resp.Data.Resolution != "1280x720" {
		t.Fatalf("unexpected resolution: %q", resp.Data.Resolution)
	}
	if resp.Data.Duration != 123.45 {
		t.Fatalf("unexpected duration: %v", resp.Data.Duration)
	}
	if resp.Data.Bitrate != 5000000 {
		t.Fatalf("unexpected bitrate: %d", resp.Data.Bitrate)
	}
	if !resp.Data.HLSCompatible {
		t.Fatalf("expected hls_compatible=true")
	}
	if len(resp.Data.Subtitles.External) != 3 {
		t.Fatalf("expected 3 external subtitles, got %d", len(resp.Data.Subtitles.External))
	}
	if len(resp.Data.Subtitles.Embedded) != 2 {
		t.Fatalf("expected 2 embedded subtitles, got %d", len(resp.Data.Subtitles.Embedded))
	}
	if resp.Data.Subtitles.Embedded[0].Index != 0 || resp.Data.Subtitles.Embedded[0].Language != "eng" || resp.Data.Subtitles.Embedded[0].Title != "English" {
		t.Fatalf("unexpected first embedded subtitle: %+v", resp.Data.Subtitles.Embedded[0])
	}
}

func TestServeWorkerSubtitleWithSignedURL(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()
	r := newTestRouter(dep, user)

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "movie.mp4")
	subtitleName := "movie.zh.srt"
	subtitleBody := "1\n00:00:00,000 --> 00:00:01,000\nhello\n"
	if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, subtitleName), []byte(subtitleBody), 0600); err != nil {
		t.Fatalf("write subtitle: %v", err)
	}
	fileID := mustCreateVideoFileFixture(t, client, user.ID, videoPath)
	fileModel, err := client.File.Get(context.Background(), fileID)
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	stateBytes, err := json.Marshal(queue.VideoTaskState{
		FileID: fileID,
		Subtitle: &queue.VideoSubtitleOption{
			Mode:         queue.VideoSubtitleModeExternal,
			ExternalName: subtitleName,
		},
	})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	taskModel, err := client.Task.Create().
		SetType(queue.VideoSubtitleBurnTaskType).
		SetStatus(task.StatusProcessing).
		SetUserID(user.ID).
		SetCorrelationID(uuid.Must(uuid.NewV4())).
		SetPublicState(&types.TaskPublicState{}).
		SetPrivateState(string(stateBytes)).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	base, err := url.Parse("http://example.test")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	rawURL, err := ffmpegworker.BuildSubtitleURL(base, fmt.Sprintf("/api/v4/video/worker/subtitle/%d", taskModel.ID), ffmpegworker.SubtitleURLClaims{
		TaskID:       taskModel.ID,
		FileID:       fileID,
		EntityID:     fileModel.PrimaryEntity,
		SubtitleName: subtitleName,
		Expires:      timeNow().Add(time.Minute).Unix(),
		Nonce:        "nonce",
	}, "worker-secret")
	if err != nil {
		t.Fatalf("BuildSubtitleURL: %v", err)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse built: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != subtitleBody {
		t.Fatalf("body = %q", w.Body.String())
	}

	head := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodHead, parsed.RequestURI(), nil)
	r.ServeHTTP(head, req)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD status = %d, body=%q", head.Code, head.Body.String())
	}
}

func TestVideoInfo_HLSCompatibility(t *testing.T) {
	testCases := []struct {
		name       string
		videoCodec string
		audioCodec string
		hasAudio   bool
		want       bool
	}{
		{name: "hevc", videoCodec: "hevc", audioCodec: "aac", hasAudio: true, want: false},
		{name: "vp9", videoCodec: "vp9", audioCodec: "aac", hasAudio: true, want: false},
		{name: "h264+aac", videoCodec: "h264", audioCodec: "aac", hasAudio: true, want: true},
		{name: "h264+mp3", videoCodec: "h264", audioCodec: "mp3", hasAudio: true, want: true},
		{name: "h264+no-audio", videoCodec: "h264", hasAudio: false, want: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			l := &memLogger{}
			dep, client, user := newTestDep(t, l)
			defer client.Close()
			r := newTestRouter(dep, user)

			videoPath := filepath.Join(t.TempDir(), "movie.mp4")
			if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
				t.Fatalf("write video: %v", err)
			}
			fileID := mustCreateVideoFileFixture(t, client, user.ID, videoPath)

			audioStream := ""
			if tc.hasAudio {
				audioStream = fmt.Sprintf(",\n    {\"index\": 1, \"codec_name\": %q, \"codec_type\": \"audio\"}", tc.audioCodec)
			}

			setFakeFFProbe(t, fmt.Sprintf(`
{
  "format": {"duration": "60", "bit_rate": "3000000"},
  "streams": [
    {"index": 0, "codec_name": %q, "codec_type": "video", "width": 640, "height": 360}%s
  ]
}
`, tc.videoCodec, audioStream), "", 0)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v4/video/info", bytes.NewBufferString(fmt.Sprintf(`{"file_id":%d}`, fileID)))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
			}

			var resp videoInfoResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
			}
			if resp.Data.HLSCompatible != tc.want {
				t.Fatalf("expected hls_compatible=%v, got %v", tc.want, resp.Data.HLSCompatible)
			}
		})
	}
}

func TestVideoInfo_FileNotFound(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()
	r := newTestRouter(dep, user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v4/video/info", bytes.NewBufferString(`{"file_id":9999}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp videoInfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	if resp.Code != serializer.CodeNotFound {
		t.Fatalf("expected serializer code=%d, got %d", serializer.CodeNotFound, resp.Code)
	}
}

func TestVideoInfo_FFProbeFailedContainsStderr(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()
	r := newTestRouter(dep, user)

	videoPath := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
		t.Fatalf("write video: %v", err)
	}
	fileID := mustCreateVideoFileFixture(t, client, user.ID, videoPath)

	setFakeFFProbe(t, "", "ffprobe: Permission denied", 1)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v4/video/info", bytes.NewBufferString(fmt.Sprintf(`{"file_id":%d}`, fileID)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp videoInfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	if resp.Error == "" || !containsAll(resp.Error, "stderr", "Permission denied") {
		t.Fatalf("expected error to contain stderr details, got %q", resp.Error)
	}
}

func mustCreateVideoFileFixture(t *testing.T, client *ent.Client, userID int, videoPath string) int {
	t.Helper()

	stat, err := os.Stat(videoPath)
	if err != nil {
		t.Fatalf("stat video: %v", err)
	}

	policy, err := client.StoragePolicy.Create().
		SetName("local").
		SetType(types.PolicyTypeLocal).
		SetSettings(&types.PolicySetting{}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}

	entity, err := client.Entity.Create().
		SetType(int(types.EntityTypeVersion)).
		SetSource(videoPath).
		SetSize(stat.Size()).
		SetStoragePolicyEntities(policy.ID).
		SetReferenceCount(1).
		SetCreatedBy(userID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}

	file, err := client.File.Create().
		SetType(int(types.FileTypeFile)).
		SetName(filepath.Base(videoPath)).
		SetOwnerID(userID).
		SetSize(stat.Size()).
		SetPrimaryEntity(entity.ID).
		SetStoragePolicyFiles(policy.ID).
		AddEntities(entity).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create file: %v", err)
	}

	return file.ID
}

func setFakeFFProbe(t *testing.T, stdout, stderr string, exitCode int) {
	t.Helper()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "ffprobe")

	script := "#!/bin/sh\n"
	if stdout != "" {
		script += "cat <<'EOF'\n" + stdout
		if !strings.HasSuffix(stdout, "\n") {
			script += "\n"
		}
		script += "EOF\n"
	}
	if stderr != "" {
		script += "cat <<'EOF' 1>&2\n" + stderr
		if !strings.HasSuffix(stderr, "\n") {
			script += "\n"
		}
		script += "EOF\n"
	}
	script += fmt.Sprintf("exit %d\n", exitCode)

	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
