package hls

import (
	"context"
	"fmt"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHLSPlaybackLifecycle(t *testing.T) {
	dep, client, owner := newTestDep(t)
	defer client.Close()
	id := createVideoFileFixture(t, client, owner.ID)
	createHLSArtifactFixture(t, client, id, "#EXTM3U\n#EXTINF:10,\nsegment_100000.ts\n", map[string]string{"segment_100000.ts": "six-digits"})
	r := gin.New()
	r.ContextWithFallback = true
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), dependency.DepCtx{}, dep))
		c.Next()
	})
	r.GET("/api/v4/hls/:fileId", GetStatus)
	r.DELETE("/api/v4/hls/:fileId", Delete)
	r.GET("/api/v4/hls/:fileId/play/index.m3u8", PlayIndex)
	r.GET("/api/v4/hls/:fileId/play/:segment", PlaySegment)
	check := func(method, path string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != want {
			t.Fatalf("%s: got %d want %d: %s", method, w.Code, want, w.Body.String())
		}
	}
	check("GET", fmt.Sprintf("/api/v4/hls/%d", id), 403)
	check("DELETE", fmt.Sprintf("/api/v4/hls/%d", id), 403)
	check("GET", fmt.Sprintf("/api/v4/hls/%d/play/index.m3u8", id), 403)
	signed, err := PlaybackURL(context.Background(), dep, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	check("GET", signed, 200)
	segment := mustSignSegmentPath(t, dep, id, "segment_100000.ts", nil)
	check("GET", segment, 200)
	expired := time.Now().Add(-time.Minute)
	check("GET", mustSignSegmentPath(t, dep, id, "segment_100000.ts", &expired), 403)
	check("GET", signed+"&v=tampered", 403)
	// 回收祖先文件夹后连旧片段 URL 都不可访问；恢复后产物仍可用。
	f, err := client.File.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	root := f.FileChildren
	if _, err := client.File.UpdateOneID(root).SetName("trashed-folder").Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	check("GET", signed, http.StatusNotFound)
	check("GET", segment, http.StatusNotFound)
	if _, err := client.File.UpdateOneID(root).SetName(inventory.RootFolderName).Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	check("GET", signed, 200)
	// 重做 HLS 后旧签名绑定的产物版本立即失效。
	a, err := client.HLSArtifact.Query().Only(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.HLSArtifact.UpdateOneID(a.ID).SetStoragePath(a.StoragePath + "-new").Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	check("GET", signed, 403)
}

func TestHLSLinkRevocation(t *testing.T) {
	ctx := context.Background()
	dep, client, owner := newTestDep(t)
	defer client.Close()
	id := createVideoFileFixture(t, client, owner.ID)
	createHLSArtifactFixture(t, client, id, "#EXTM3U\n000.ts\n", map[string]string{"000.ts": "video"})
	r := newRouter(dep)
	check := func(raw string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", raw, nil))
		if w.Code != want {
			t.Fatalf("got %d want %d", w.Code, want)
		}
	}
	dl, err := client.DirectLink.Create().SetFileID(id).SetName("test").SetDownloads(0).SetSpeed(0).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	link, err := PlaybackURL(ctx, dep, id, dl.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(link, 200)
	if err := client.DirectLink.DeleteOneID(dl.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	check(link, 404)
	// 删除绑定参数也不能使已撤销的直链重新有效。
	u, _ := url.Parse(link)
	q := u.Query()
	q.Del("link")
	u.RawQuery = q.Encode()
	check(u.String(), 403)
	expiry := time.Now().Add(5 * time.Minute)
	sh, err := client.Share.Create().SetFileID(id).SetUserID(owner.ID).SetPassword("test-secret").SetExpires(expiry).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?uri="+url.QueryEscape("cloudreve://"+hashid.EncodeShareID(dep.HashIDEncoder(), sh.ID)+":test-secret@share"), nil)
	c.Request = c.Request.WithContext(context.WithValue(ctx, dependency.DepCtx{}, dep))
	signed, err := requestPlaybackURL(c, dep, id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(signed, "test-secret") {
		t.Fatal("password leaked")
	}
	u, _ = url.Parse(signed)
	if u.Query().Get("until") != fmt.Sprint(expiry.Unix()) {
		t.Fatal("share expiry was extended")
	}
	check(signed, 200)
	if _, err := client.Share.UpdateOneID(sh.ID).SetPassword("changed-secret").Save(ctx); err != nil {
		t.Fatal(err)
	}
	check(signed, 404)
}
