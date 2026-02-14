package controllers

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestFolderDL_PathTraversalSecurityMatrix(t *testing.T) {
	testCases := []struct {
		name           string
		target         string
		wildcardPath   string
		expectStatus   int
		expectResolver bool
	}{
		{
			name:           "plain dot-dot traversal",
			target:         "/d/tk/../etc/passwd",
			wildcardPath:   "/../etc/passwd",
			expectStatus:   http.StatusForbidden,
			expectResolver: false,
		},
		{
			name:           "encoded slash traversal",
			target:         "/d/tk/..%2F..%2Fetc%2Fpasswd",
			wildcardPath:   "/..%2F..%2Fetc%2Fpasswd",
			expectStatus:   http.StatusForbidden,
			expectResolver: false,
		},
		{
			name:           "double encoded slash traversal",
			target:         "/d/tk/..%252F..%252Fetc",
			wildcardPath:   "/..%252F..%252Fetc",
			expectStatus:   http.StatusForbidden,
			expectResolver: false,
		},
		{
			name:           "windows backslash traversal",
			target:         "/d/tk/..\\..\\windows\\system32",
			wildcardPath:   "/..\\..\\windows\\system32",
			expectStatus:   http.StatusForbidden,
			expectResolver: false,
		},
		{
			name:           "null byte with traversal",
			target:         "/d/tk/foo%00/../etc/passwd",
			wildcardPath:   "/foo%00/../etc/passwd",
			expectStatus:   http.StatusForbidden,
			expectResolver: false,
		},
		{
			name:           "overlong utf8 slash style payload",
			target:         "/d/tk/foo%c0%af../etc",
			wildcardPath:   "/foo%c0%af../etc",
			expectStatus:   http.StatusForbidden,
			expectResolver: false,
		},
		{
			name:           "normal unicode path",
			target:         "/d/tk/%E6%AD%A3%E5%B8%B8/%E5%AD%90%E7%9B%AE%E5%BD%95/%E6%96%87%E4%BB%B6.txt",
			wildcardPath:   "/正常/子目录/文件.txt",
			expectStatus:   http.StatusFound,
			expectResolver: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c, w := newFolderDLContext(tc.target, "tk", tc.wildcardPath)

			resolverCalled := false
			original := folderDLResolveURL
			folderDLResolveURL = func(_ *gin.Context, token, relPath string) (string, int, error) {
				resolverCalled = true
				if !tc.expectResolver {
					t.Fatal("resolver should not be called for blocked payload")
				}
				if token != "tk" {
					t.Fatalf("unexpected token: %s", token)
				}
				if relPath != "正常/子目录/文件.txt" {
					t.Fatalf("unexpected normalized path: %s", relPath)
				}
				return "https://download.example/unicode.txt", 0, nil
			}
			t.Cleanup(func() { folderDLResolveURL = original })

			FolderDL(c)

			if tc.expectResolver != resolverCalled {
				t.Fatalf("resolver called = %v, expected %v", resolverCalled, tc.expectResolver)
			}
			if w.Code != tc.expectStatus {
				t.Fatalf("expected status %d, got %d", tc.expectStatus, w.Code)
			}
		})
	}
}
