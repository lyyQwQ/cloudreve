package video

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestVideoTaskDedup_Conflict409(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()

	r := newTestRouter(dep, user)
	videoPath := t.TempDir() + "/movie.mp4"
	if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
		t.Fatalf("write video: %v", err)
	}
	fileID := mustCreateVideoFileFixture(t, client, user.ID, videoPath)
	setFakeFFProbe(t, `
{
  "format": {"duration": "60", "bit_rate": "3000000"},
  "streams": [
    {"index": 0, "codec_name": "h264", "codec_type": "video", "width": 640, "height": 360},
    {"index": 1, "codec_name": "aac", "codec_type": "audio"}
  ]
}
`, "", 0)

	{
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v4/video/hls", bytes.NewBufferString(fmt.Sprintf(`{"file_id":%d}`, fileID)))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("first create: expected 200, got %d", w.Code)
		}
	}

	{
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v4/video/hls", bytes.NewBufferString(fmt.Sprintf(`{"file_id":%d}`, fileID)))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusConflict {
			t.Fatalf("dedup: expected 409, got %d, body=%s", w.Code, w.Body.String())
		}
	}
}

func TestVideoSubtitleBurnDedup_OnlyByFileID(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()

	r := newTestRouter(dep, user)

	{
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v4/video/subtitle/burn", bytes.NewBufferString(`{"file_id":1,"subtitle":{"mode":"external","external_name":"a.srt"}}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("first create: expected 200, got %d", w.Code)
		}
	}

	{
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v4/video/subtitle/burn", bytes.NewBufferString(`{"file_id":1,"subtitle":{"mode":"embedded","embedded_index":0}}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusConflict {
			t.Fatalf("dedup: expected 409, got %d, body=%s", w.Code, w.Body.String())
		}
	}
}
