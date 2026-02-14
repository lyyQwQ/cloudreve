package downloader

import (
	"context"
	"net/url"
	"strings"
)

const aria2TaskIDPrefix = "a2-"

type Router struct {
	aria2 Downloader
	qb    Downloader
}

func NewRouter(aria2 Downloader, qb Downloader) Downloader {
	return &Router{aria2: aria2, qb: qb}
}

func (r *Router) CreateTask(ctx context.Context, url string, options map[string]interface{}) (*TaskHandle, error) {
	if shouldUseQBittorrent(url) {
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

func shouldUseQBittorrent(rawURL string) bool {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return false
	}

	if strings.HasPrefix(strings.ToLower(s), "magnet:") {
		return true
	}

	parsed, err := url.Parse(s)
	if err == nil {
		if strings.HasSuffix(strings.ToLower(parsed.Path), ".torrent") {
			return true
		}
	}

	return strings.HasSuffix(strings.ToLower(s), ".torrent")
}
