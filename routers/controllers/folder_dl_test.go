package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newFolderDLContext(target, token, wildcardPath string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	c.Params = gin.Params{
		{Key: "token", Value: token},
		{Key: "path", Value: wildcardPath},
	}

	return c, w
}

func TestFolderDL_RedirectShareRoot(t *testing.T) {
	c, w := newFolderDLContext("/d/tk", "tk", "")

	called := false
	original := folderDLResolveURL
	folderDLResolveURL = func(_ *gin.Context, _, _ string) (string, int, error) {
		called = true
		return "", 0, nil
	}
	t.Cleanup(func() { folderDLResolveURL = original })

	FolderDL(c)

	if called {
		t.Fatal("resolver should not be called for root path")
	}
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/s/tk" {
		t.Fatalf("expected redirect to /s/tk, got %q", loc)
	}
}

func TestFolderDL_RedirectDownloadURL(t *testing.T) {
	c, w := newFolderDLContext("/d/tk/sub/file.txt", "tk", "/sub/file.txt")

	original := folderDLResolveURL
	folderDLResolveURL = func(_ *gin.Context, token, relPath string) (string, int, error) {
		if token != "tk" {
			t.Fatalf("unexpected token: %s", token)
		}
		if relPath != "sub/file.txt" {
			t.Fatalf("unexpected relative path: %s", relPath)
		}
		return "https://download.example/file.txt", 0, nil
	}
	t.Cleanup(func() { folderDLResolveURL = original })

	FolderDL(c)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "https://download.example/file.txt" {
		t.Fatalf("unexpected redirect location: %q", loc)
	}
}

func TestFolderDL_InvalidTokenOrExpired_Return404(t *testing.T) {
	c, w := newFolderDLContext("/d/invalid/file.txt", "invalid", "/file.txt")

	original := folderDLResolveURL
	folderDLResolveURL = func(_ *gin.Context, _, _ string) (string, int, error) {
		return "", http.StatusNotFound, errors.New("not found")
	}
	t.Cleanup(func() { folderDLResolveURL = original })

	FolderDL(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestFolderDL_DirectoryTarget_Return404(t *testing.T) {
	c, w := newFolderDLContext("/d/tk/folder", "tk", "/folder")

	original := folderDLResolveURL
	folderDLResolveURL = func(_ *gin.Context, _, _ string) (string, int, error) {
		return "", http.StatusNotFound, errors.New("directory")
	}
	t.Cleanup(func() { folderDLResolveURL = original })

	FolderDL(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestFolderDL_TraversalPath_Return403(t *testing.T) {
	c, w := newFolderDLContext("/d/tk/../secret", "tk", "/../secret")

	original := folderDLResolveURL
	folderDLResolveURL = func(_ *gin.Context, _, _ string) (string, int, error) {
		t.Fatal("resolver should not be called for traversal path")
		return "", 0, nil
	}
	t.Cleanup(func() { folderDLResolveURL = original })

	FolderDL(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestFolderDL_EncodedSeparatorPath_Return403(t *testing.T) {
	c, w := newFolderDLContext("/d/tk/%2Fetc%2Fpasswd", "tk", "/%2Fetc%2Fpasswd")

	original := folderDLResolveURL
	folderDLResolveURL = func(_ *gin.Context, _, _ string) (string, int, error) {
		t.Fatal("resolver should not be called for encoded separator path")
		return "", 0, nil
	}
	t.Cleanup(func() { folderDLResolveURL = original })

	FolderDL(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestFolderDL_NormalizePath(t *testing.T) {
	rel, err := normalizeFolderDLPath("/a/b/c.txt", "/d/tk/a/b/c.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != "a/b/c.txt" {
		t.Fatalf("expected a/b/c.txt, got %q", rel)
	}
}
