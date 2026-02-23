package inventory

import "testing"

func TestDefaultSettingsVideoFFMpegRuntimeOptions(t *testing.T) {
	threads, ok := DefaultSettings["video_ffmpeg_threads"]
	if !ok {
		t.Fatalf("missing default setting: video_ffmpeg_threads")
	}
	if threads != "1" {
		t.Fatalf("unexpected default threads, want=1 got=%q", threads)
	}

	nice, ok := DefaultSettings["video_ffmpeg_nice"]
	if !ok {
		t.Fatalf("missing default setting: video_ffmpeg_nice")
	}
	if nice != "10" {
		t.Fatalf("unexpected default nice, want=10 got=%q", nice)
	}

	workerNum, ok := DefaultSettings["queue_video_process_worker_num"]
	if !ok {
		t.Fatalf("missing default setting: queue_video_process_worker_num")
	}
	if workerNum != "1" {
		t.Fatalf("unexpected default queue video process worker number, want=1 got=%q", workerNum)
	}
}
