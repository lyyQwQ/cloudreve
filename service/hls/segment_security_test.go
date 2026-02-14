package hls

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/gin-gonic/gin"
)

func TestHLSPlaySegment_SecurityMatrixTODO12e(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dep, client, user := newTestDep(t)
	defer client.Close()

	fileID := createVideoFileFixture(t, client, user.ID)
	createHLSArtifactFixture(t, client, fileID, "#EXTM3U\n#EXTINF:10,\n000.ts\n", map[string]string{
		"000.ts": "segment-000",
	})

	r := newRouter(dep)
	validSignedPath := mustSignSegmentPath(t, dep, fileID, "000.ts", nil)

	for _, tc := range []struct {
		name         string
		input        string
		expectedCode int
		directCall   bool
		requestPath  string
		segmentParam string
	}{
		{
			name:         "000.ts => 200",
			input:        "000.ts",
			expectedCode: http.StatusOK,
			requestPath:  validSignedPath,
		},
		{
			name:         "../../../etc/passwd => 400",
			input:        "../../../etc/passwd",
			expectedCode: http.StatusBadRequest,
			directCall:   true,
			requestPath:  fmt.Sprintf("/api/v4/hls/%d/play/../../../etc/passwd", fileID),
			segmentParam: "../../../etc/passwd",
		},
		{
			name:         "foo/bar.ts => 400",
			input:        "foo/bar.ts",
			expectedCode: http.StatusBadRequest,
			directCall:   true,
			requestPath:  fmt.Sprintf("/api/v4/hls/%d/play/foo/bar.ts", fileID),
			segmentParam: "foo/bar.ts",
		},
		{
			name:         "%2e%2e%2f000.ts => 400",
			input:        "%2e%2e%2f000.ts",
			expectedCode: http.StatusBadRequest,
			directCall:   true,
			requestPath:  fmt.Sprintf("/api/v4/hls/%d/play/%%2e%%2e%%2f000.ts", fileID),
			segmentParam: "../000.ts",
		},
		{
			name:         "000.ts%00.jpg => 400",
			input:        "000.ts%00.jpg",
			expectedCode: http.StatusBadRequest,
			directCall:   true,
			requestPath:  fmt.Sprintf("/api/v4/hls/%d/play/000.ts%%00.jpg", fileID),
			segmentParam: "000.ts\x00.jpg",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := httptest.NewRecorder()

			if tc.directCall {
				c, e := gin.CreateTestContext(resp)
				e.ContextWithFallback = true
				req := httptest.NewRequest(http.MethodGet, tc.requestPath, nil)
				ctx := context.WithValue(req.Context(), dependency.DepCtx{}, dep)
				c.Request = req.WithContext(ctx)
				c.Params = gin.Params{
					{Key: "fileId", Value: strconv.Itoa(fileID)},
					{Key: "segment", Value: tc.segmentParam},
				}

				PlaySegment(c)
			} else {
				req := httptest.NewRequest(http.MethodGet, tc.requestPath, nil)
				r.ServeHTTP(resp, req)
			}

			if resp.Code != tc.expectedCode {
				t.Fatalf("input=%s expected %d, got %d, body=%s", tc.input, tc.expectedCode, resp.Code, resp.Body.String())
			}
		})
	}
}
