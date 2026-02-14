package hls

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/gin-gonic/gin"
)

func TestHLSPlaySegment_AuthBypassReturnsForbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n#EXTINF:10,\n001.ts\n", map[string]string{
		"000.ts": "segment-000",
		"001.ts": "segment-001",
	})

	otherFileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, otherFileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": "other-segment-000",
	})

	r := newRouter(dep)

	validSignedPath := mustSignSegmentPath(t, dep, fileID, "000.ts", nil)
	validSign := mustExtractSign(t, validSignedPath)

	expiredAt := time.Now().Add(-1 * time.Minute)
	expiredSignedPath := mustSignSegmentPath(t, dep, fileID, "000.ts", &expiredAt)

	tamperedSignedPath := mustTamperSign(t, validSignedPath)
	reusedSignPath := fmt.Sprintf("/api/v4/hls/%d/play/000.ts?%s", otherFileID, url.Values{"sign": []string{validSign}}.Encode())

	for _, tc := range []struct {
		name         string
		path         string
		expectedCode int
	}{
		{
			name:         "missing sign query",
			path:         fmt.Sprintf("/api/v4/hls/%d/play/000.ts", fileID),
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "tampered sign query",
			path:         tamperedSignedPath,
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "expired sign query",
			path:         expiredSignedPath,
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "reuse sign on different file",
			path:         reusedSignPath,
			expectedCode: http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.ServeHTTP(resp, req)

			if resp.Code != tc.expectedCode {
				t.Fatalf("expected %d, got %d, body=%s", tc.expectedCode, resp.Code, resp.Body.String())
			}
		})
	}
}

func mustSignSegmentPath(t *testing.T, dep interface{ GeneralAuth() auth.Auth }, fileID int, segment string, expires *time.Time) string {
	t.Helper()

	signed, err := auth.SignURI(context.Background(), dep.GeneralAuth(), fmt.Sprintf("/api/v4/hls/%d/play/%s", fileID, segment), expires)
	if err != nil {
		t.Fatalf("sign segment path: %v", err)
	}

	return signed.String()
}

func mustExtractSign(t *testing.T, signedPath string) string {
	t.Helper()

	u, err := url.Parse(signedPath)
	if err != nil {
		t.Fatalf("parse signed path: %v", err)
	}

	sign := u.Query().Get("sign")
	if sign == "" {
		t.Fatalf("missing sign in signed path: %s", signedPath)
	}

	return sign
}

func mustTamperSign(t *testing.T, signedPath string) string {
	t.Helper()

	u, err := url.Parse(signedPath)
	if err != nil {
		t.Fatalf("parse signed path: %v", err)
	}

	q := u.Query()
	sign := q.Get("sign")
	if sign == "" {
		t.Fatalf("missing sign in signed path: %s", signedPath)
	}

	q.Set("sign", sign+"-tampered")
	u.RawQuery = q.Encode()

	return strings.TrimSpace(u.String())
}
