package setting

import (
	"context"
	"testing"
)

type testSettingAdapter struct {
	values map[string]any
}

func (s *testSettingAdapter) Get(_ context.Context, name string, defaultVal any) any {
	if val, ok := s.values[name]; ok {
		return val
	}

	return defaultVal
}

func TestProviderVideoFFMpegRuntimeOptionGetters(t *testing.T) {
	p := NewProvider(&testSettingAdapter{values: map[string]any{
		"video_ffmpeg_threads": "6",
		"video_ffmpeg_nice":    "3",
	}})

	if got := p.VideoFFMpegThreads(context.Background()); got != 6 {
		t.Fatalf("VideoFFMpegThreads mismatch, want=6 got=%d", got)
	}
	if got := p.VideoFFMpegNice(context.Background()); got != 3 {
		t.Fatalf("VideoFFMpegNice mismatch, want=3 got=%d", got)
	}
}

func TestProviderVideoFFMpegRuntimeOptionGetterDefaults(t *testing.T) {
	p := NewProvider(&testSettingAdapter{values: map[string]any{
		"video_ffmpeg_threads": "not-an-int",
	}})

	if got := p.VideoFFMpegThreads(context.Background()); got != 1 {
		t.Fatalf("VideoFFMpegThreads fallback mismatch, want=1 got=%d", got)
	}
	if got := p.VideoFFMpegNice(context.Background()); got != 10 {
		t.Fatalf("VideoFFMpegNice default mismatch, want=10 got=%d", got)
	}
}

func TestProviderRemoteFFMpegWorkerDefaults(t *testing.T) {
	p := NewProvider(&testSettingAdapter{values: map[string]any{}})

	cfg := p.RemoteFFMpegWorker(context.Background())
	if cfg.Enabled {
		t.Fatalf("RemoteFFMpegWorker should be disabled by default")
	}
	if cfg.Endpoint != "" || cfg.APIKey != "" {
		t.Fatalf("unexpected default remote worker endpoint/api key: %+v", cfg)
	}
	if cfg.Timeout.Seconds() != 21600 || cfg.PollInterval.Seconds() != 5 || cfg.SourceURLTTL.Seconds() != 1800 {
		t.Fatalf("unexpected default durations: %+v", cfg)
	}
}

func TestProviderRemoteFFMpegWorkerGetters(t *testing.T) {
	p := NewProvider(&testSettingAdapter{values: map[string]any{
		"video_ffmpeg_worker_enabled":        "1",
		"video_ffmpeg_worker_endpoint":       "https://worker.example.com/",
		"video_ffmpeg_worker_api_key":        " secret ",
		"video_ffmpeg_worker_timeout":        "60",
		"video_ffmpeg_worker_poll_interval":  "2",
		"video_ffmpeg_worker_source_url_ttl": "30",
	}})

	cfg := p.RemoteFFMpegWorker(context.Background())
	if !cfg.Enabled {
		t.Fatalf("RemoteFFMpegWorker should be enabled")
	}
	if cfg.Endpoint != "https://worker.example.com" {
		t.Fatalf("unexpected endpoint: %q", cfg.Endpoint)
	}
	if cfg.APIKey != "secret" {
		t.Fatalf("unexpected api key: %q", cfg.APIKey)
	}
	if cfg.Timeout.Seconds() != 60 || cfg.PollInterval.Seconds() != 2 || cfg.SourceURLTTL.Seconds() != 30 {
		t.Fatalf("unexpected durations: %+v", cfg)
	}
}
