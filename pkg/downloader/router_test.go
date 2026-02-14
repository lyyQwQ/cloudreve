package downloader

import (
	"context"
	"strings"
	"testing"
)

type spyDownloader struct {
	createCalled bool
}

func (s *spyDownloader) CreateTask(ctx context.Context, url string, options map[string]interface{}) (*TaskHandle, error) {
	s.createCalled = true
	return &TaskHandle{ID: "id"}, nil
}

func (s *spyDownloader) Info(ctx context.Context, handle *TaskHandle) (*TaskStatus, error) {
	return nil, nil
}

func (s *spyDownloader) Cancel(ctx context.Context, handle *TaskHandle) error {
	return nil
}

func (s *spyDownloader) SetFilesToDownload(ctx context.Context, handle *TaskHandle, args ...*SetFileToDownloadArgs) error {
	return nil
}

func (s *spyDownloader) Test(ctx context.Context) (string, error) {
	return "", nil
}

func TestRouter_CreateTaskRouting(t *testing.T) {
	ctx := context.Background()

	type tc struct {
		name       string
		url        string
		expectQB   bool
		expectA2ID bool
	}

	tests := []tc{
		{name: "magnet routes to qbittorrent", url: "magnet:?xt=urn:btih:abcdef", expectQB: true},
		{name: "torrent routes to qbittorrent (case-insensitive)", url: "https://example.com/a.TORRENT?x=1", expectQB: true},
		{name: "http routes to aria2", url: "http://example.com/a.iso", expectQB: false, expectA2ID: true},
		{name: "https routes to aria2", url: "https://example.com/a.iso", expectQB: false, expectA2ID: true},
		{name: "ftp routes to aria2", url: "ftp://example.com/a.iso", expectQB: false, expectA2ID: true},
		{name: "empty routes to aria2", url: "", expectQB: false, expectA2ID: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a2 := &spyDownloader{}
			qb := &spyDownloader{}
			r := NewRouter(a2, qb)

			h, err := r.CreateTask(ctx, tt.url, nil)
			if err != nil {
				t.Fatalf("CreateTask returned error: %v", err)
			}
			if h == nil {
				t.Fatalf("CreateTask returned nil handle")
			}

			if tt.expectQB {
				if !qb.createCalled || a2.createCalled {
					t.Fatalf("expected qb CreateTask called only; got qb=%v a2=%v", qb.createCalled, a2.createCalled)
				}
				if strings.HasPrefix(h.ID, aria2TaskIDPrefix) {
					t.Fatalf("unexpected aria2 prefix on qb handle: %q", h.ID)
				}
			} else {
				if !a2.createCalled || qb.createCalled {
					t.Fatalf("expected aria2 CreateTask called only; got qb=%v a2=%v", qb.createCalled, a2.createCalled)
				}
				if tt.expectA2ID && !strings.HasPrefix(h.ID, aria2TaskIDPrefix) {
					t.Fatalf("expected aria2 prefix on aria2 handle: %q", h.ID)
				}
			}
		})
	}
}
