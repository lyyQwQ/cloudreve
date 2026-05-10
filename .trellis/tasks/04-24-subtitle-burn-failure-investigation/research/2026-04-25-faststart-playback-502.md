# 2026-04-25 Subtitle Burn Output Playback 502

## Scope

Read-only production diagnosis after a subtitle-burn output file failed to play in browser with a reported 502.

## Observations

- Cloudreve container is still running image `ghcr.io/lyyqwq/cloudreve:sha-9f5296a`.
- Recent Cloudreve logs for the burned file content endpoint show HTTP `206` and `200`, not application-level `502`.
- The played file URL included entity hash `Evks7`, decoded locally to entity `981`.
- Entity `981` source is a generated subtitle-burn MP4 under a `burned/` directory.
- `ffprobe` can parse the file successfully:
  - container: `mov,mp4,m4a,3gp,3g2,mj2`
  - video: `h264`, 1920x1080, 25fps
  - audio: `aac`, 48000Hz stereo
  - duration: about 2753.58s
  - probe_score: 100
- MP4 top-level atoms:
  - `ftyp` at offset 0
  - `mdat` starts at offset 40
  - `moov` starts near file end, around offset `1682042101`
- During playback, logs show many Range-style `206` responses against the same generated MP4 in a short time window.

## Interpretation

The generated MP4 is structurally valid, but `moov` is written at the end of the file. Browser streaming generally works better when `moov` is at the beginning. With `moov` at the end, the browser/player must issue extra Range requests for metadata and media data. Over FRP/proxy/mobile network paths, this can surface as browser playback failure or a user-visible 502 even though Cloudreve returns `206`/`200`.

## Desired behavior

Subtitle-burn output MP4 files should be optimized for browser progressive playback by passing `-movflags +faststart` to ffmpeg when the output container is MP4.

## Implementation lead

- Target file: `pkg/queue/video_queue.go`
- Existing subtitle-burn ffmpeg args already write MP4 output. Add `-movflags +faststart` near output options.
- Add a focused unit test that asserts subtitle-burn ffmpeg args include `-movflags +faststart`.
