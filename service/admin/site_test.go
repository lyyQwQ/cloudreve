package admin

import "testing"

func TestNormalizeSettingPatchSkipsVirtualAndBlankWorkerAPIKey(t *testing.T) {
	settings := map[string]string{
		videoFFMpegWorkerAPIKeySetSetting:   "1",
		videoFFMpegWorkerAPIKeyClearSetting: "0",
		videoFFMpegWorkerAPIKeySetting:      "",
		"video_ffmpeg_worker_endpoint":      "https://worker.example.com",
	}

	if normalizeSettingPatch(settings) {
		t.Fatalf("clear should not be requested")
	}

	if _, ok := settings[videoFFMpegWorkerAPIKeySetSetting]; ok {
		t.Fatalf("virtual key set status should not be persisted")
	}
	if _, ok := settings[videoFFMpegWorkerAPIKeyClearSetting]; ok {
		t.Fatalf("virtual clear flag should not be persisted")
	}
	if _, ok := settings[videoFFMpegWorkerAPIKeySetting]; ok {
		t.Fatalf("blank API key patch should not overwrite existing key")
	}
	if settings["video_ffmpeg_worker_endpoint"] != "https://worker.example.com" {
		t.Fatalf("unrelated setting was changed")
	}
}

func TestNormalizeSettingPatchAllowsExplicitWorkerAPIKeyClear(t *testing.T) {
	settings := map[string]string{
		videoFFMpegWorkerAPIKeySetSetting:   "1",
		videoFFMpegWorkerAPIKeyClearSetting: "1",
		videoFFMpegWorkerAPIKeySetting:      "",
	}

	if !normalizeSettingPatch(settings) {
		t.Fatalf("clear should be requested")
	}
	if _, ok := settings[videoFFMpegWorkerAPIKeySetSetting]; ok {
		t.Fatalf("virtual key set status should not be persisted")
	}
	if _, ok := settings[videoFFMpegWorkerAPIKeyClearSetting]; ok {
		t.Fatalf("virtual clear flag should not be persisted")
	}
	if value, ok := settings[videoFFMpegWorkerAPIKeySetting]; !ok || value != "" {
		t.Fatalf("API key should be persisted as empty on explicit clear, ok=%v value=%q", ok, value)
	}
}

func TestRedactedSettingResponseMasksWorkerAPIKey(t *testing.T) {
	res := redactedSettingResponse(map[string]string{
		videoFFMpegWorkerAPIKeySetting: "secret",
	})

	if res[videoFFMpegWorkerAPIKeySetting] != "" {
		t.Fatalf("API key response should be blank")
	}
	if res[videoFFMpegWorkerAPIKeySetSetting] != "1" {
		t.Fatalf("API key set flag = %q, want 1", res[videoFFMpegWorkerAPIKeySetSetting])
	}
	if res[videoFFMpegWorkerAPIKeyClearSetting] != "0" {
		t.Fatalf("API key clear flag = %q, want 0", res[videoFFMpegWorkerAPIKeyClearSetting])
	}
}
