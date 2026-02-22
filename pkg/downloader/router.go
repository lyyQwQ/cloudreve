package downloader

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	aria2TaskIDPrefix   = "a2-"
	torrentContentType  = "application/x-bittorrent"
	torrentProbeUA      = "Mozilla/5.0 (compatible; CloudreveDownloaderRouter/1.0)"
	torrentProbeTimeout = 10 * time.Second
)

type Router struct {
	aria2 Downloader
	qb    Downloader
}

func NewRouter(aria2 Downloader, qb Downloader) Downloader {
	return &Router{aria2: aria2, qb: qb}
}

func (r *Router) CreateTask(ctx context.Context, url string, options map[string]interface{}) (*TaskHandle, error) {
	if shouldUseQBittorrent(ctx, url) {
		return r.qb.CreateTask(ctx, url, options)
	}

	h, err := r.aria2.CreateTask(ctx, url, options)
	if h != nil {
		h.ID = aria2TaskIDPrefix + h.ID
	}
	return h, err
}

func (r *Router) Info(ctx context.Context, handle *TaskHandle) (*TaskStatus, error) {
	client, routedHandle := r.routeByHandle(handle)
	status, err := client.Info(ctx, routedHandle)
	if err != nil {
		return nil, err
	}

	if status != nil && status.FollowedBy != nil && isAria2Handle(handle) {
		status.FollowedBy.ID = aria2TaskIDPrefix + status.FollowedBy.ID
	}

	return status, nil
}

func (r *Router) Cancel(ctx context.Context, handle *TaskHandle) error {
	client, routedHandle := r.routeByHandle(handle)
	return client.Cancel(ctx, routedHandle)
}

func (r *Router) SetFilesToDownload(ctx context.Context, handle *TaskHandle, args ...*SetFileToDownloadArgs) error {
	client, routedHandle := r.routeByHandle(handle)
	return client.SetFilesToDownload(ctx, routedHandle, args...)
}

func (r *Router) Test(ctx context.Context) (string, error) {
	return r.qb.Test(ctx)
}

func (r *Router) routeByHandle(handle *TaskHandle) (Downloader, *TaskHandle) {
	if isAria2Handle(handle) {
		copied := *handle
		copied.ID = strings.TrimPrefix(copied.ID, aria2TaskIDPrefix)
		return r.aria2, &copied
	}

	return r.qb, handle
}

func isAria2Handle(handle *TaskHandle) bool {
	return handle != nil && strings.HasPrefix(handle.ID, aria2TaskIDPrefix)
}

func shouldUseQBittorrent(ctx context.Context, rawURL string) bool {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return false
	}

	if strings.HasPrefix(strings.ToLower(s), "magnet:") {
		return true
	}

	if hasTorrentSuffix(s) {
		return true
	}

	parsed, err := url.Parse(s)
	if err != nil {
		return hasTorrentSuffix(s)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}

	isTorrent, err := hasBittorrentContentType(ctx, s)
	if err != nil {
		return hasTorrentSuffix(s)
	}

	return isTorrent
}

func hasTorrentSuffix(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err == nil && strings.HasSuffix(strings.ToLower(parsed.Path), ".torrent") {
		return true
	}

	return strings.HasSuffix(strings.ToLower(rawURL), ".torrent")
}

func hasBittorrentContentType(ctx context.Context, rawURL string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, torrentProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", torrentProbeUA)

	client := &http.Client{Timeout: torrentProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return false, fmt.Errorf("head probe failed with status %d", resp.StatusCode)
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		return false, nil
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.EqualFold(contentType, torrentContentType), nil
	}

	return strings.EqualFold(mediaType, torrentContentType), nil
}
