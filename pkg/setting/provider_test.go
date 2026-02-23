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
