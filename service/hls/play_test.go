package hls

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/enttest"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/ent/metadata"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

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

func newTestDep(t *testing.T) (dependency.Dep, *ent.Client, *ent.User) {
	t.Helper()

	client := enttest.Open(t, "sqlite3", t.TempDir()+"/ent.db")
	grp, err := client.Group.Create().
		SetName("g").
		SetPermissions(&boolset.BooleanSet{}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	user, err := client.User.Create().
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
		dependency.WithDbClient(client),
		dependency.WithConfigProvider(cp),
		dependency.WithHashIDEncoder(h),
		dependency.WithGeneralAuth(auth.HMACAuth{SecretKey: []byte("hls-test-secret")}),
		dependency.WithSettingClient(&memSettingClient{values: map[string]string{
			"entity_url_default_ttl": "3600",
		}}),
	)

	return dep, client, user
}

func newRouter(dep dependency.Dep) *gin.Engine {
	r := gin.New()
	r.ContextWithFallback = true
	r.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), dependency.DepCtx{}, dep)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})

	hls := r.Group("/api/v4/hls")
	{
		hls.GET(":fileId", GetStatus)
		hls.DELETE(":fileId", Delete)
		hls.GET(":fileId/play/index.m3u8", PlayIndex)
		hls.GET(":fileId/play/:segment", PlaySegment)
	}

	return r
}

func TestHLSPlayIndex_RewritesSignedURLs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n#EXTINF:10,\n001.ts\n", map[string]string{
		"000.ts": "seg-000",
		"001.ts": "seg-001",
	})

	r := newRouter(dep)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v4/hls/%d/play/index.m3u8", fileID), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !regexp.MustCompile(fmt.Sprintf(`/api/v4/hls/%d/play/000\.ts\?sign=`, fileID)).MatchString(body) {
		t.Fatalf("expected rewritten signed url for 000.ts, got body=%s", body)
	}
	if !regexp.MustCompile(fmt.Sprintf(`/api/v4/hls/%d/play/001\.ts\?sign=`, fileID)).MatchString(body) {
		t.Fatalf("expected rewritten signed url for 001.ts, got body=%s", body)
	}
}

func TestHLSPlaySegment_ValidSegmentReturnsContent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	content := "segment-000-body"
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": content,
	})

	r := newRouter(dep)
	playlistResp := httptest.NewRecorder()
	playlistReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v4/hls/%d/play/index.m3u8", fileID), nil)
	r.ServeHTTP(playlistResp, playlistReq)
	if playlistResp.Code != http.StatusOK {
		t.Fatalf("expected 200 from index, got %d, body=%s", playlistResp.Code, playlistResp.Body.String())
	}

	signedSegmentPath := extractSignedSegmentPath(t, fileID, playlistResp.Body.String(), "000.ts")
	segmentResp := httptest.NewRecorder()
	segmentReq := httptest.NewRequest(http.MethodGet, signedSegmentPath, nil)
	r.ServeHTTP(segmentResp, segmentReq)

	if segmentResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", segmentResp.Code, segmentResp.Body.String())
	}
	if segmentResp.Body.String() != content {
		t.Fatalf("expected segment content %q, got %q", content, segmentResp.Body.String())
	}
}

func TestHLSPlaySegment_InvalidSegmentReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": "segment",
	})

	for _, tc := range []struct {
		name        string
		segment     string
		escapedPath string
	}{
		{name: "parent traversal", segment: "../000.ts", escapedPath: fmt.Sprintf("/api/v4/hls/%d/play/..%%2F000.ts", fileID)},
		{name: "nested path", segment: "foo/bar.ts", escapedPath: fmt.Sprintf("/api/v4/hls/%d/play/foo%%2Fbar.ts", fileID)},
		{name: "encoded slash", segment: "000.ts/1.ts", escapedPath: fmt.Sprintf("/api/v4/hls/%d/play/000.ts%%2F1.ts", fileID)},
		{name: "encoded backslash", segment: "000.ts\\1.ts", escapedPath: fmt.Sprintf("/api/v4/hls/%d/play/000.ts%%5C1.ts", fileID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, e := gin.CreateTestContext(w)
			e.ContextWithFallback = true
			req := httptest.NewRequest(http.MethodGet, tc.escapedPath, nil)
			ctx := context.WithValue(req.Context(), dependency.DepCtx{}, dep)
			c.Request = req.WithContext(ctx)
			c.Params = gin.Params{
				{Key: "fileId", Value: strconv.Itoa(fileID)},
				{Key: "segment", Value: tc.segment},
			}

			PlaySegment(c)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHLSPlay_AcceptsHashIDFileID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": "segment-000",
	})

	hashID := hashid.EncodeFileID(dep.HashIDEncoder(), fileID)
	r := newRouter(dep)

	indexResp := httptest.NewRecorder()
	indexReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v4/hls/%s/play/index.m3u8", hashID), nil)
	r.ServeHTTP(indexResp, indexReq)
	if indexResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for hashid index, got %d, body=%s", indexResp.Code, indexResp.Body.String())
	}

	signed, err := auth.SignURI(context.Background(), dep.GeneralAuth(), fmt.Sprintf("/api/v4/hls/%s/play/000.ts", hashID), nil)
	if err != nil {
		t.Fatalf("sign hashid segment path: %v", err)
	}

	segmentResp := httptest.NewRecorder()
	segmentReq := httptest.NewRequest(http.MethodGet, signed.String(), nil)
	r.ServeHTTP(segmentResp, segmentReq)
	if segmentResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for hashid segment, got %d, body=%s", segmentResp.Code, segmentResp.Body.String())
	}
}

func TestHLSPlay_InvalidFileIDReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": "segment-000",
	})

	r := newRouter(dep)
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "index", path: "/api/v4/hls/not-valid/play/index.m3u8"},
		{name: "segment", path: "/api/v4/hls/not-valid/play/000.ts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHLSDelete_SubtractsUserStorageAndRemovesRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": "segment-000",
	})

	ctx := context.Background()
	artifact, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(ctx)
	if err != nil {
		t.Fatalf("query artifact: %v", err)
	}

	if _, err := client.Metadata.Create().
		SetFileID(fileID).
		SetName(inventory.HLSAvailableMetadataKey).
		SetValue(inventory.HLSAvailableMetadataValue).
		SetIsPublic(true).
		Save(ctx); err != nil {
		t.Fatalf("create metadata: %v", err)
	}

	initialStorage := artifact.TotalSize + 128
	if _, err := client.User.UpdateOneID(user.ID).SetStorage(initialStorage).Save(ctx); err != nil {
		t.Fatalf("seed user storage: %v", err)
	}

	r := newRouter(dep)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v4/hls/%d", fileID), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	if _, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(ctx); err == nil || !ent.IsNotFound(err) {
		t.Fatalf("expected artifact deleted, got err=%v", err)
	}

	metaCount, err := client.Metadata.Query().Where(metadata.FileID(fileID), metadata.Name(inventory.HLSAvailableMetadataKey)).Count(ctx)
	if err != nil {
		t.Fatalf("count metadata: %v", err)
	}
	if metaCount != 0 {
		t.Fatalf("expected metadata removed, got count=%d", metaCount)
	}

	updatedUser, err := client.User.Get(ctx, user.ID)
	if err != nil {
		t.Fatalf("query user: %v", err)
	}
	if want := initialStorage - artifact.TotalSize; updatedUser.Storage != want {
		t.Fatalf("expected user storage=%d, got %d", want, updatedUser.Storage)
	}

	if _, err := os.Stat(artifact.StoragePath); !os.IsNotExist(err) {
		t.Fatalf("expected artifact dir removed, stat err=%v", err)
	}
}

func TestHLSDelete_FileNotFoundReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, _ := newTestDep(t)
	defer client.Close()

	r := newRouter(dep)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v4/hls/999999", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp serializer.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	if resp.Code != serializer.CodeNotFound {
		t.Fatalf("expected serializer code=%d, got %d", serializer.CodeNotFound, resp.Code)
	}
}

func TestHLSDelete_ArtifactNotFoundReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)

	r := newRouter(dep)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v4/hls/%d", fileID), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp serializer.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	if resp.Code != serializer.CodeNotFound {
		t.Fatalf("expected serializer code=%d, got %d", serializer.CodeNotFound, resp.Code)
	}
}

func TestHLSPlay_NoArtifactReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	r := newRouter(dep)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v4/hls/%d/play/index.m3u8", fileID), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp serializer.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	if resp.Code != serializer.CodeNotFound {
		t.Fatalf("expected serializer code=%d, got %d", serializer.CodeNotFound, resp.Code)
	}
}

func createVideoFileFixture(t *testing.T, client *ent.Client, userID int) int {
	t.Helper()

	videoPath := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
		t.Fatalf("write video: %v", err)
	}

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

func createHLSArtifactFixture(t *testing.T, client *ent.Client, fileID int, playlist string, segments map[string]string) {
	t.Helper()

	base := filepath.Join(os.TempDir(), "cloudreve-hls")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatalf("create hls base dir: %v", err)
	}

	dir, err := os.MkdirTemp(base, "test-*")
	if err != nil {
		t.Fatalf("create hls artifact dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(playlist), 0600); err != nil {
		t.Fatalf("write playlist: %v", err)
	}

	var total int64
	for name, content := range segments {
		segmentPath := filepath.Join(dir, name)
		if err := os.WriteFile(segmentPath, []byte(content), 0600); err != nil {
			t.Fatalf("write segment %s: %v", name, err)
		}
		total += int64(len(content))
	}

	if _, err := client.HLSArtifact.Create().
		SetSourceFileID(fileID).
		SetStoragePath(dir).
		SetSegmentCount(len(segments)).
		SetTotalSize(total).
		SetCodec("h264/aac").
		Save(context.Background()); err != nil {
		t.Fatalf("create hls artifact: %v", err)
	}
}

func extractSignedSegmentPath(t *testing.T, fileID int, playlist, segment string) string {
	t.Helper()

	pattern := regexp.MustCompile(fmt.Sprintf(`/api/v4/hls/%d/play/%s\?sign=[^\s\r\n]+`, fileID, regexp.QuoteMeta(segment)))
	match := pattern.FindString(playlist)
	if match == "" {
		t.Fatalf("failed to find signed segment URL for %s in playlist: %s", segment, playlist)
	}

	return match
}
