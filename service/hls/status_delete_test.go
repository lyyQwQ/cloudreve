package hls

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

func TestHLSGetStatus_AcceptsNumericFileID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": "segment-000",
	})

	r := newRouter(dep)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v4/hls/%d", fileID), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp serializer.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("expected serializer code=0, got %d, body=%s", resp.Code, w.Body.String())
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected data map, got %T", resp.Data)
	}
	if got := int(data["file_id"].(float64)); got != fileID {
		t.Fatalf("expected file_id=%d, got %v", fileID, data["file_id"])
	}
	if got, ok := data["has_hls"].(bool); !ok || !got {
		t.Fatalf("expected has_hls=true, got %v", data["has_hls"])
	}
}

func TestHLSGetStatus_AcceptsHashIDFileID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": "segment-000",
	})

	hashID := hashid.EncodeFileID(dep.HashIDEncoder(), fileID)
	r := newRouter(dep)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v4/hls/%s", hashID), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp serializer.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("expected serializer code=0, got %d, body=%s", resp.Code, w.Body.String())
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected data map, got %T", resp.Data)
	}
	if got := int(data["file_id"].(float64)); got != fileID {
		t.Fatalf("expected file_id=%d, got %v", fileID, data["file_id"])
	}
	if got, ok := data["has_hls"].(bool); !ok || !got {
		t.Fatalf("expected has_hls=true, got %v", data["has_hls"])
	}
}

func TestHLSGetStatus_InvalidFileIDReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, _ := newTestDep(t)
	defer client.Close()

	r := newRouter(dep)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v4/hls/not-valid", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestHLSDelete_AcceptsNumericFileID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": "segment-000",
	})

	art, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(context.Background())
	if err != nil {
		t.Fatalf("query artifact: %v", err)
	}

	r := newRouter(dep)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v4/hls/%d", fileID), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp serializer.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, w.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("expected serializer code=0, got %d, body=%s", resp.Code, w.Body.String())
	}

	if _, err := os.Stat(art.StoragePath); !os.IsNotExist(err) {
		t.Fatalf("expected artifact dir removed, stat err=%v", err)
	}

	_, err = client.HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(context.Background())
	if err == nil || !ent.IsNotFound(err) {
		t.Fatalf("expected artifact deleted, got err=%v", err)
	}
}

func TestHLSDelete_AcceptsHashIDFileID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": "segment-000",
	})

	art, err := client.HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(context.Background())
	if err != nil {
		t.Fatalf("query artifact: %v", err)
	}

	hashID := hashid.EncodeFileID(dep.HashIDEncoder(), fileID)
	r := newRouter(dep)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v4/hls/%s", hashID), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	if _, err := os.Stat(art.StoragePath); !os.IsNotExist(err) {
		t.Fatalf("expected artifact dir removed, stat err=%v", err)
	}

	_, err = client.HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Only(context.Background())
	if err == nil || !ent.IsNotFound(err) {
		t.Fatalf("expected artifact deleted, got err=%v", err)
	}
}

func TestHLSDelete_InvalidFileIDReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, _ := newTestDep(t)
	defer client.Close()

	r := newRouter(dep)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v4/hls/not-valid", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}
