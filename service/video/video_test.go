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
	"strings"
	"sync"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/enttest"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	hlssvc "github.com/cloudreve/Cloudreve/v4/service/hls"
	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"
)

type memLogger struct {
	mu     sync.Mutex
	prefix string
	infos  *[]string
}

func (l *memLogger) Panic(format string, v ...any)   { panic(fmt.Sprintf(format, v...)) }
func (l *memLogger) Error(format string, v ...any)   {}
func (l *memLogger) Warning(format string, v ...any) {}
func (l *memLogger) Debug(format string, v ...any)   {}
func (l *memLogger) Info(format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.infos == nil {
		tmp := make([]string, 0)
		l.infos = &tmp
	}
	*l.infos = append(*l.infos, strings.TrimSpace(l.prefix+" ")+fmt.Sprintf(format, v...))
}
func (l *memLogger) CopyWithPrefix(prefix string) logging.Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.infos == nil {
		tmp := make([]string, 0)
		l.infos = &tmp
	}
	return &memLogger{prefix: strings.TrimSpace(l.prefix + " " + prefix), infos: l.infos}
}
func (l *memLogger) SupportColor() bool { return false }

type testConfigProvider struct {
	db    conf.Database
	sys   conf.System
	ssl   conf.SSL
	unix  conf.Unix
	slave conf.Slave
	redis conf.Redis
	cors  conf.Cors
	over  map[string]any
}

func (c *testConfigProvider) Database() *conf.Database        { return &c.db }
func (c *testConfigProvider) System() *conf.System            { return &c.sys }
func (c *testConfigProvider) SSL() *conf.SSL                  { return &c.ssl }
func (c *testConfigProvider) Unix() *conf.Unix                { return &c.unix }
func (c *testConfigProvider) Slave() *conf.Slave              { return &c.slave }
func (c *testConfigProvider) Redis() *conf.Redis              { return &c.redis }
func (c *testConfigProvider) Cors() *conf.Cors                { return &c.cors }
func (c *testConfigProvider) OptionOverwrite() map[string]any { return c.over }

type memSettingClient struct {
	values map[string]string
}

func (m *memSettingClient) SetClient(newClient *ent.Client) inventory.TxOperator { return m }
func (m *memSettingClient) GetClient() *ent.Client                               { return nil }
func (m *memSettingClient) Get(ctx context.Context, name string) (string, error) {
	if v, ok := m.values[name]; ok {
		return v, nil
	}
	return "", fmt.Errorf("setting %q not found", name)
}
func (m *memSettingClient) Gets(ctx context.Context, names []string) (map[string]string, error) {
	out := make(map[string]string)
	for _, n := range names {
		v, err := m.Get(ctx, n)
		if err != nil {
			return nil, err
		}
		out[n] = v
	}
	return out, nil
}
func (m *memSettingClient) Set(ctx context.Context, settings map[string]string) error {
	for k, v := range settings {
		m.values[k] = v
	}
	return nil
}

func newTestDep(t *testing.T, l *memLogger) (dependency.Dep, *ent.Client, *ent.User) {
	t.Helper()

	client := enttest.Open(t, "sqlite3", t.TempDir()+"/ent.db")
	grp, err := client.Group.Create().
		SetName("g").
		SetPermissions(&boolset.BooleanSet{}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	usr, err := client.User.Create().
		SetEmail("u@example.com").
		SetNick("u").
		SetGroupUsers(grp.ID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	h, err := hashid.New("salt")
	if err != nil {
		t.Fatalf("hashid.New: %v", err)
	}

	cp := &testConfigProvider{}
	cp.db.Type = conf.SQLiteDB
	cp.sys.Mode = conf.MasterMode

	dep := dependency.NewDependency(
		dependency.WithLogger(l),
		dependency.WithDbClient(client),
		dependency.WithConfigProvider(cp),
		dependency.WithHashIDEncoder(h),
		dependency.WithSettingClient(&memSettingClient{values: map[string]string{"queue_video_process_worker_num": "2"}}),
	)

	return dep, client, usr
}

func newTestRouter(dep dependency.Dep, user *ent.User) *gin.Engine {
	r := gin.New()
	r.ContextWithFallback = true
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, logging.CorrelationIDCtx{}, uuid.Must(uuid.NewV4()))
		ctx = context.WithValue(ctx, dependency.DepCtx{}, dep)
		ctx = context.WithValue(ctx, inventory.UserCtx{}, user)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})

	api := r.Group("/api/v4")
	video := api.Group("video")
	{
		video.POST("info", GetInfo)
		video.GET("subtitles", ListSubtitles)
		video.POST("subtitle/burn", BurnSubtitle)
		video.POST("batch/subtitle/preflight", BatchSubtitlePreflight)
		video.POST("batch/subtitle/burn", BatchSubtitleBurn)
		video.POST("batch/hls/preflight", BatchHLSPreflight)
		video.POST("batch/hls", BatchHLS)
		video.POST("hls", SliceHLS)
	}
	hls := api.Group("hls")
	{
		hls.GET(":fileId", hlssvc.GetStatus)
		hls.DELETE(":fileId", hlssvc.Delete)
		hls.GET(":fileId/play/index.m3u8", hlssvc.PlayIndex)
		hls.GET(":fileId/play/:segment", hlssvc.PlaySegment)
	}

	return r
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) serializer.Response {
	t.Helper()
	var resp serializer.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	return resp
}

func TestVideoHandlers_ExistAndStub(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()

	r := newTestRouter(dep, user)

	for _, tc := range []struct {
		method string
		path   string
		body   string
		status int
		code   int
	}{
		{method: http.MethodPost, path: "/api/v4/video/subtitle/burn", body: `{"file_id":1}`, status: http.StatusOK, code: 0},
		{method: http.MethodPost, path: "/api/v4/video/hls", body: `{"file_id":1}`, status: http.StatusNotFound, code: serializer.CodeNotFound},
		{method: http.MethodGet, path: "/api/v4/hls/1", body: "", status: http.StatusOK, code: 0},
		{method: http.MethodDelete, path: "/api/v4/hls/1", body: "", status: http.StatusNotFound, code: serializer.CodeNotFound},
		{method: http.MethodGet, path: "/api/v4/hls/1/play/index.m3u8", body: "", status: http.StatusOK, code: 0},
		{method: http.MethodGet, path: "/api/v4/hls/1/play/seg0.ts", body: "", status: http.StatusOK, code: 0},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s %s: expected %d, got %d, body=%s", tc.method, tc.path, tc.status, w.Code, w.Body.String())
		}
		resp := decodeResp(t, w)
		if resp.Code != tc.code {
			t.Fatalf("%s %s: expected code=%d, got %d, body=%s", tc.method, tc.path, tc.code, resp.Code, w.Body.String())
		}
	}
}

func TestVideoProcessQueue_UsesSettingWorkerNum(t *testing.T) {
	l := &memLogger{}
	dep, client, _ := newTestDep(t, l)
	defer client.Close()

	q := dep.VideoProcessQueue(context.Background())
	q.Start()
	q.Shutdown()

	l.mu.Lock()
	defer l.mu.Unlock()
	joined := ""
	if l.infos != nil {
		joined = strings.Join(*l.infos, "\n")
	}
	if !strings.Contains(joined, "VideoProcessQueue") || !strings.Contains(joined, "started with 2 workers") {
		t.Fatalf("expected worker_num=2 log, got: %s", joined)
	}
}

func TestBurnSubtitle_RequestSelectionSavedToState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		assert func(t *testing.T, state *queue.VideoTaskState)
	}{
		{
			name: "external",
			body: `{"file_id":11,"subtitle":{"mode":"external","external_name":"movie.zh.srt"}}`,
			assert: func(t *testing.T, state *queue.VideoTaskState) {
				t.Helper()
				if state.Subtitle == nil {
					t.Fatalf("expected subtitle state")
				}
				if state.Subtitle.Mode != queue.VideoSubtitleModeExternal || state.Subtitle.ExternalName != "movie.zh.srt" {
					t.Fatalf("unexpected subtitle state: %+v", state.Subtitle)
				}
				if state.Subtitle.EmbeddedIndex != nil {
					t.Fatalf("expected nil embedded_index, got %+v", state.Subtitle.EmbeddedIndex)
				}
			},
		},
		{
			name: "embedded",
			body: `{"file_id":11,"subtitle":{"mode":"embedded","embedded_index":3}}`,
			assert: func(t *testing.T, state *queue.VideoTaskState) {
				t.Helper()
				if state.Subtitle == nil {
					t.Fatalf("expected subtitle state")
				}
				if state.Subtitle.Mode != queue.VideoSubtitleModeEmbedded {
					t.Fatalf("unexpected subtitle mode: %q", state.Subtitle.Mode)
				}
				if state.Subtitle.EmbeddedIndex == nil || *state.Subtitle.EmbeddedIndex != 3 {
					t.Fatalf("unexpected embedded_index: %+v", state.Subtitle.EmbeddedIndex)
				}
				if state.Subtitle.ExternalName != "" {
					t.Fatalf("expected empty external_name, got %q", state.Subtitle.ExternalName)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &memLogger{}
			dep, client, user := newTestDep(t, l)
			defer client.Close()

			r := newTestRouter(dep, user)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v4/video/subtitle/burn", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
			}

			model, err := client.Task.Query().Where(task.Type(queue.VideoSubtitleBurnTaskType)).Only(context.Background())
			if err != nil {
				t.Fatalf("query task: %v", err)
			}

			state, err := queue.ParseVideoTaskState(model.PrivateState)
			if err != nil {
				t.Fatalf("ParseVideoTaskState: %v", err)
			}
			if state.FileID != 11 {
				t.Fatalf("unexpected file_id: %d", state.FileID)
			}

			tc.assert(t, state)
		})
	}
}

func TestBurnSubtitle_InvalidSelectionReturns400(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "external missing name", body: `{"file_id":1,"subtitle":{"mode":"external"}}`},
		{name: "embedded missing index", body: `{"file_id":1,"subtitle":{"mode":"embedded"}}`},
		{name: "auto with extra field", body: `{"file_id":1,"subtitle":{"mode":"auto","external_name":"a.srt"}}`},
		{name: "invalid mode", body: `{"file_id":1,"subtitle":{"mode":"custom"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &memLogger{}
			dep, client, user := newTestDep(t, l)
			defer client.Close()

			r := newTestRouter(dep, user)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v4/video/subtitle/burn", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSliceHLS_UnsupportedCodecReturns400(t *testing.T) {
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
    {"index": 0, "codec_name": "hevc", "codec_type": "video", "width": 640, "height": 360},
    {"index": 1, "codec_name": "aac", "codec_type": "audio"}
  ]
}
`, "", 0)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v4/video/hls", bytes.NewBufferString(fmt.Sprintf(`{"file_id":%d}`, fileID)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}

	resp := decodeResp(t, w)
	if resp.Code != 1 || !strings.Contains(resp.Error, queue.ErrUnsupportedCodec.Error()) {
		t.Fatalf("expected unsupported codec error, got body=%s", w.Body.String())
	}

	n, err := client.Task.Query().Where(task.Type(queue.VideoHLSSliceTaskType)).Count(context.Background())
	if err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no hls task created, got %d", n)
	}
}

func TestSliceHLS_AllowsNonAACAndNoAudio(t *testing.T) {
	testCases := []struct {
		name        string
		probeOutput string
	}{
		{
			name: "h264 with non-aac audio",
			probeOutput: `
{
  "format": {"duration": "60", "bit_rate": "3000000"},
  "streams": [
    {"index": 0, "codec_name": "h264", "codec_type": "video", "width": 640, "height": 360},
    {"index": 1, "codec_name": "mp3", "codec_type": "audio"}
  ]
}
`,
		},
		{
			name: "h264 without audio",
			probeOutput: `
{
  "format": {"duration": "60", "bit_rate": "3000000"},
  "streams": [
    {"index": 0, "codec_name": "h264", "codec_type": "video", "width": 640, "height": 360}
  ]
}
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			l := &memLogger{}
			dep, client, user := newTestDep(t, l)
			defer client.Close()
			r := newTestRouter(dep, user)

			videoPath := t.TempDir() + "/movie.mp4"
			if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
				t.Fatalf("write video: %v", err)
			}

			fileID := mustCreateVideoFileFixture(t, client, user.ID, videoPath)
			setFakeFFProbe(t, tc.probeOutput, "", 0)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v4/video/hls", bytes.NewBufferString(fmt.Sprintf(`{"file_id":%d}`, fileID)))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
			}

			resp := decodeResp(t, w)
			if resp.Code != 0 {
				t.Fatalf("expected success response, got body=%s", w.Body.String())
			}

			n, err := client.Task.Query().Where(task.Type(queue.VideoHLSSliceTaskType)).Count(context.Background())
			if err != nil {
				t.Fatalf("count tasks: %v", err)
			}
			if n != 1 {
				t.Fatalf("expected one hls task created, got %d", n)
			}
		})
	}
}

type videoSubtitleListResponse struct {
	Code int `json:"code"`
	Data struct {
		External []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"external"`
		Embedded []struct {
			Index    int    `json:"index"`
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"embedded"`
	} `json:"data"`
}

func TestListSubtitles_ReturnsSubtitleList(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()
	r := newTestRouter(dep, user)

	dir := t.TempDir()
	videoPath := dir + "/movie.mp4"
	if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := os.WriteFile(dir+"/movie.srt", []byte("1"), 0600); err != nil {
		t.Fatalf("write srt: %v", err)
	}

	fileID := mustCreateVideoFileFixture(t, client, user.ID, videoPath)
	setFakeFFProbe(t, `
{
  "format": {"duration": "10", "bit_rate": "1000"},
  "streams": [
    {"index": 0, "codec_name": "h264", "codec_type": "video"},
    {"index": 1, "codec_name": "subrip", "codec_type": "subtitle", "tags": {"language": "eng", "title": "English"}}
  ]
}
`, "", 0)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v4/video/subtitles?file_id=%d", fileID), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp videoSubtitleListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}

	if resp.Code != 0 {
		t.Fatalf("expected code=0, got %d, body=%s", resp.Code, w.Body.String())
	}
	if len(resp.Data.External) != 1 || resp.Data.External[0].Name != "movie.srt" {
		t.Fatalf("unexpected external subtitles: %+v", resp.Data.External)
	}
	if len(resp.Data.Embedded) != 1 || resp.Data.Embedded[0].Index != 0 {
		t.Fatalf("unexpected embedded subtitles: %+v", resp.Data.Embedded)
	}
}

func TestResolveFileID(t *testing.T) {
	l := &memLogger{}
	dep, client, _ := newTestDep(t, l)
	defer client.Close()

	hashID := hashid.EncodeFileID(dep.HashIDEncoder(), 123)

	for _, tc := range []struct {
		name    string
		raw     string
		wantID  int
		wantErr bool
	}{
		{name: "numeric", raw: "123", wantID: 123},
		{name: "hashid", raw: hashID, wantID: 123},
		{name: "invalid", raw: "not-a-valid-id", wantErr: true},
		{name: "zero", raw: "0", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveFileID(dep, tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got id=%d", got)
				}
				return
			}

			if err != nil {
				t.Fatalf("resolveFileID error: %v", err)
			}
			if got != tc.wantID {
				t.Fatalf("expected id=%d, got %d", tc.wantID, got)
			}
		})
	}
}

func TestVideoHandlers_AcceptsHashIDFileID(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()
	r := newTestRouter(dep, user)

	hashID := hashid.EncodeFileID(dep.HashIDEncoder(), 1)

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "video info", method: http.MethodPost, path: "/api/v4/video/info", body: fmt.Sprintf(`{"file_id":"%s"}`, hashID)},
		{name: "list subtitles", method: http.MethodGet, path: "/api/v4/video/subtitles?file_id=" + url.QueryEscape(hashID)},
		{name: "burn subtitle", method: http.MethodPost, path: "/api/v4/video/subtitle/burn", body: fmt.Sprintf(`{"file_id":"%s"}`, hashID)},
		{name: "hls", method: http.MethodPost, path: "/api/v4/video/hls", body: fmt.Sprintf(`{"file_id":"%s"}`, hashID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			if tc.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/json")
			}
			r.ServeHTTP(w, req)

			if w.Code == http.StatusBadRequest {
				t.Fatalf("expected hashid file_id accepted, got 400, body=%s", w.Body.String())
			}
		})
	}
}

func TestVideoHandlers_InvalidFileIDReturns400(t *testing.T) {
	l := &memLogger{}
	dep, client, user := newTestDep(t, l)
	defer client.Close()
	r := newTestRouter(dep, user)

	invalid := "invalid_file_id"

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "video info", method: http.MethodPost, path: "/api/v4/video/info", body: fmt.Sprintf(`{"file_id":"%s"}`, invalid)},
		{name: "list subtitles", method: http.MethodGet, path: "/api/v4/video/subtitles?file_id=" + url.QueryEscape(invalid)},
		{name: "burn subtitle", method: http.MethodPost, path: "/api/v4/video/subtitle/burn", body: fmt.Sprintf(`{"file_id":"%s"}`, invalid)},
		{name: "hls", method: http.MethodPost, path: "/api/v4/video/hls", body: fmt.Sprintf(`{"file_id":"%s"}`, invalid)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			if tc.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/json")
			}
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
			}
		})
	}
}
