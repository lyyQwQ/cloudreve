package video

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/gin-gonic/gin"
)

func TestBatchSubtitlePreflight_IntersectsExternalAndEmbeddedCandidates(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()
	r := newTestRouter(dep, user)

	dir := t.TempDir()
	firstPath := dir + "/S03E01.mp4"
	secondPath := dir + "/S03E02.mp4"
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
			t.Fatalf("write video: %v", err)
		}
	}
	for _, path := range []string{dir + "/S03E01.zh.srt", dir + "/S03E02.zh.srt"} {
		if err := os.WriteFile(path, []byte("subtitle"), 0600); err != nil {
			t.Fatalf("write subtitle: %v", err)
		}
	}

	firstID := mustCreateVideoFileFixture(t, client, user.ID, firstPath)
	secondID := mustCreateVideoFileFixture(t, client, user.ID, secondPath)
	setFakeFFProbe(t, batchProbeOutput("h264", "Simplified Chinese"), "", 0)

	resp := postBatchSubtitlePreflight(t, r, fmt.Sprintf(`{"file_ids":["%d","%d"]}`, firstID, secondID))
	if resp.Code != 0 {
		t.Fatalf("expected code=0, got body=%+v", resp)
	}
	if resp.Data.Summary.Ready != 2 {
		t.Fatalf("expected 2 ready rows, got %+v", resp.Data.Summary)
	}

	keys := subtitleCandidateKeys(resp.Data.Candidates)
	wantKeys := []string{"embedded:english", "embedded:simplified_chinese", "external:zh"}
	if fmt.Sprint(keys) != fmt.Sprint(wantKeys) {
		t.Fatalf("unexpected shared candidates: got %v, want %v", keys, wantKeys)
	}

	for _, row := range resp.Data.Rows {
		if row.Status != batchRowStatusReady {
			t.Fatalf("expected ready row, got %+v", row)
		}
		binding, ok := findSubtitleBinding(row.Candidates, "external:zh")
		if !ok || binding.ExternalName != row.FileName[:len(row.FileName)-4]+".zh.srt" {
			t.Fatalf("unexpected external binding for row %+v: %+v", row, binding)
		}
		binding, ok = findSubtitleBinding(row.Candidates, "embedded:simplified_chinese")
		if !ok || binding.EmbeddedIndex == nil || *binding.EmbeddedIndex != 0 {
			t.Fatalf("unexpected embedded binding for row %+v: %+v", row, binding)
		}
	}
}

func TestBatchSubtitleBurn_CreatesTasksFromSelectedCandidate(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()
	r := newTestRouter(dep, user)

	dir := t.TempDir()
	firstPath := dir + "/Episode01.mp4"
	secondPath := dir + "/Episode02.mp4"
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
			t.Fatalf("write video: %v", err)
		}
	}
	firstID := mustCreateVideoFileFixture(t, client, user.ID, firstPath)
	secondID := mustCreateVideoFileFixture(t, client, user.ID, secondPath)
	setFakeFFProbe(t, batchProbeOutput("h264", "Simplified Chinese"), "", 0)

	w := httptest.NewRecorder()
	body := fmt.Sprintf(`{"file_ids":["%d","%d"],"candidate_key":"embedded:simplified_chinese"}`, firstID, secondID)
	req := httptest.NewRequest(http.MethodPost, "/api/v4/video/batch/subtitle/burn", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Code int                 `json:"code"`
		Data batchCreateResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	if resp.Code != 0 || resp.Data.Summary.Created != 2 {
		t.Fatalf("unexpected create response: %+v", resp)
	}

	tasks, err := client.Task.Query().Where(task.Type(queue.VideoSubtitleBurnTaskType)).All(context.Background())
	if err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 subtitle tasks, got %d", len(tasks))
	}
	for _, model := range tasks {
		state, err := queue.ParseVideoTaskState(model.PrivateState)
		if err != nil {
			t.Fatalf("ParseVideoTaskState: %v", err)
		}
		if state.Subtitle == nil || state.Subtitle.Mode != queue.VideoSubtitleModeEmbedded {
			t.Fatalf("unexpected subtitle state: %+v", state.Subtitle)
		}
		if state.Subtitle.EmbeddedIndex == nil || *state.Subtitle.EmbeddedIndex != 0 {
			t.Fatalf("unexpected embedded index: %+v", state.Subtitle.EmbeddedIndex)
		}
	}
}

func TestBatchSubtitlePreflight_SkipsWhenNoSharedCandidate(t *testing.T) {
	info := &videoInfoData{}
	firstCandidates := buildSubtitleCandidates("S01E01.mp4", info)
	secondInfo := &videoInfoData{}
	secondInfo.Subtitles.External = []videoExternalSubtitle{{Name: "S01E02.zh.srt"}}
	secondCandidates := buildSubtitleCandidates("S01E02.mp4", secondInfo)

	firstKeys := make(map[string]bool)
	for _, candidate := range firstCandidates {
		firstKeys[candidate.CandidateKey] = true
	}
	for _, candidate := range secondCandidates {
		if firstKeys[candidate.CandidateKey] {
			t.Fatalf("expected empty intersection, got key %s", candidate.CandidateKey)
		}
	}
}

func TestBatchHLSPreflightAndCreate(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()
	r := newTestRouter(dep, user)

	dir := t.TempDir()
	firstPath := dir + "/Episode01.mp4"
	secondPath := dir + "/Episode02.mp4"
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
			t.Fatalf("write video: %v", err)
		}
	}
	firstID := mustCreateVideoFileFixture(t, client, user.ID, firstPath)
	secondID := mustCreateVideoFileFixture(t, client, user.ID, secondPath)
	setFakeFFProbe(t, batchProbeOutput("h264", "Simplified Chinese"), "", 0)

	preflight := postBatchHLSPreflight(t, r, fmt.Sprintf(`{"file_ids":["%d","%d"]}`, firstID, secondID))
	if preflight.Code != 0 || preflight.Data.Summary.Ready != 2 {
		t.Fatalf("unexpected hls preflight response: %+v", preflight)
	}
	for _, row := range preflight.Data.Rows {
		if row.Status != batchRowStatusReady || row.Codec != "h264" {
			t.Fatalf("unexpected hls row: %+v", row)
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v4/video/batch/hls", bytes.NewBufferString(fmt.Sprintf(`{"file_ids":["%d","%d"]}`, firstID, secondID)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var createResp struct {
		Code int                 `json:"code"`
		Data batchCreateResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	if createResp.Code != 0 || createResp.Data.Summary.Created != 2 {
		t.Fatalf("unexpected hls create response: %+v", createResp)
	}
	n, err := client.Task.Query().Where(task.Type(queue.VideoHLSSliceTaskType)).Count(context.Background())
	if err != nil {
		t.Fatalf("count hls tasks: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 hls tasks, got %d", n)
	}
}

func postBatchSubtitlePreflight(t *testing.T, r *gin.Engine, body string) struct {
	Code int                            `json:"code"`
	Data batchSubtitlePreflightResponse `json:"data"`
} {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v4/video/batch/subtitle/preflight", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int                            `json:"code"`
		Data batchSubtitlePreflightResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	return resp
}

func postBatchHLSPreflight(t *testing.T, r *gin.Engine, body string) struct {
	Code int                       `json:"code"`
	Data batchHLSPreflightResponse `json:"data"`
} {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v4/video/batch/hls/preflight", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int                       `json:"code"`
		Data batchHLSPreflightResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	return resp
}

func subtitleCandidateKeys(candidates []batchSubtitleCandidate) []string {
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, candidate.Key)
	}
	sort.Strings(keys)
	return keys
}

func batchProbeOutput(videoCodec string, subtitleTitle string) string {
	return fmt.Sprintf(`
{
  "format": {"duration": "60", "bit_rate": "3000000"},
  "streams": [
    {"index": 0, "codec_name": %q, "codec_type": "video", "width": 640, "height": 360},
    {"index": 1, "codec_name": "aac", "codec_type": "audio"},
    {"index": 2, "codec_name": "subrip", "codec_type": "subtitle", "tags": {"language": "chi", "title": %q}},
    {"index": 3, "codec_name": "subrip", "codec_type": "subtitle", "tags": {"language": "eng", "title": "English"}}
  ]
}
`, videoCodec, subtitleTitle)
}
