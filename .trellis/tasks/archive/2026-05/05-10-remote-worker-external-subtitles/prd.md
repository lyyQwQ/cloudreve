# Remote Worker External Subtitle Support

## Goal

Enable the Cloudreve remote FFmpeg worker path to burn external subtitle files, including `.srt`, `.ass`, and `.ssa`, so batch subtitle burn jobs can offload both embedded and matched external subtitle work to the stronger worker host.

## What I Already Know

* Cloudreve already scans external subtitles from the video source directory and supports `.srt`, `.ass`, and `.ssa` in single-file and batch subtitle selection.
* Single-file external subtitle selection accepts any same-directory subtitle filename through `subtitle.external_name`.
* Batch subtitle selection currently exposes only matched external subtitle candidates such as `<video-stem>.srt` and `<video-stem>.zh.srt`, which avoids cross-episode subtitle mixups.
* Current remote worker routing only allows explicit embedded mode with `embedded_index >= 0`.
* Current worker URL API only accepts `source_url`, `embedded_index`, `duration`, and `bitrate`; it has no subtitle file input.
* External subtitle burn currently falls back to local Cloudreve FFmpeg execution.

## Requirements

* External subtitle mode should use the remote worker when remote worker settings are enabled and a same-directory external subtitle can be resolved safely.
* Supported external subtitle extensions are `.srt`, `.ass`, and `.ssa`.
* `.srt` should keep the existing Cloudreve local behavior of adding `force_style` based on video height.
* `.ass` and `.ssa` should keep their subtitle file styling and avoid adding `force_style`.
* Embedded subtitle remote worker behavior must remain compatible with the existing `/v1/jobs/embedded-subtitle-burn-url` endpoint.
* Remote worker URL jobs must continue using short-lived signed URLs and must not log worker API keys, signed source URL queries, or signed subtitle URL queries.
* Local FFmpeg fallback remains acceptable only before a remote worker job starts, consistent with current remote-worker contracts.
* The implementation should stay simple enough for single-person maintenance: no shared storage dependency, no arbitrary FFmpeg argument input, no generic job DSL.

## Acceptance Criteria

* [x] Backend can create an external-subtitle remote worker job with both `source_url` and `subtitle_url`.
* [x] Backend signs and serves an external subtitle file from the video directory through a worker-only endpoint or equivalent signed source mechanism.
* [x] Worker downloads both video and subtitle files into the job directory before invoking FFmpeg.
* [x] Worker builds a fixed FFmpeg template for external subtitles using `subtitles=filename='<subtitle-path>'`.
* [x] Worker adds `force_style` for external `.srt` and omits it for `.ass` / `.ssa`.
* [x] Existing embedded remote worker tests continue passing.
* [x] Tests cover SRT and ASS external subtitle remote worker paths, URL validation, and sensitive query redaction.

## Out of Scope

* Arbitrary FFmpeg flags or custom filtergraph input from Cloudreve.
* Uploading subtitle files from the browser as part of this feature.
* Remote worker support for unrelated HLS or video conversion modes.
* Changing the current batch subtitle matching rule.
* Cross-host shared filesystem setup.

## Technical Plan

1. Backend routing and signing
   * Add a signed worker-only subtitle source endpoint, likely `GET|HEAD /api/v4/video/worker/subtitle/:taskId`.
   * Bind the signature to task ID, video file ID, primary entity ID, subtitle filename, expiry, and nonce.
   * Resolve the subtitle by filename in the video entity directory and allow only `.srt`, `.ass`, `.ssa` with basename-only input.

2. Backend remote worker client
   * Extend remote worker eligibility so explicit external mode can use the remote path after `buildSubtitleFilterArg` has resolved mode as `external`.
   * Build both signed URLs: video `source_url` and subtitle `subtitle_url`.
   * Add a separate worker create call for external subtitle URL jobs, likely `POST /v1/jobs/external-subtitle-burn-url`.
   * Keep embedded mode on the current endpoint for compatibility.

3. Worker API and job manager
   * Add request structs for external subtitle URL jobs with `source_url`, `subtitle_url`, `subtitle_name`, `duration`, and `bitrate`.
   * Reuse the existing job lifecycle, progress fields, cancellation, output serving, and cleanup behavior.
   * Download source video to `input.mp4` and external subtitle to a fixed basename in the job directory.

4. Worker FFmpeg runner
   * Add an external subtitle burn request and fixed argument builder.
   * Preserve the current MP4 output template: `libx264`, CRF 18, optional bitrate cap, audio copy, `-movflags +faststart`, progress pipe.
   * Build external subtitle filters with the same safe filename escaping rules as embedded filters.

5. Tests and verification
   * Backend queue tests for external remote eligibility, create-job payload, fallback before job start, and signed subtitle URL redaction.
   * Backend service tests for subtitle source endpoint authorization, invalid filename rejection, GET, HEAD, and Range behavior through `http.ServeContent`.
   * Worker jobs tests for downloading both files and invoking the external runner.
   * Worker HTTP API tests for auth, validation, and external URL job creation.
   * Worker FFmpeg tests for `.srt` force style and `.ass` / `.ssa` no force style.

## Definition of Done

* Tests added or updated in both affected repositories.
* Focused Go tests pass for backend queue/video/ffmpegworker packages and worker packages.
* `git diff --check` passes in both repositories.
* Git diff and staged content are reviewed for secrets before commit.
* Backend and worker changes are committed separately or with clear repository-specific commits if implementation proceeds to Git writes.

## Technical Notes

* Backend remote worker code: `pkg/queue/video_remote_worker.go`.
* Backend subtitle filter and local external subtitle behavior: `pkg/queue/video_queue.go`.
* Backend subtitle discovery and worker source endpoint: `service/video/info.go`, `service/video/worker_source.go`.
* Backend worker URL signing: `pkg/ffmpegworker/source_url.go`.
* Backend route registration: `routers/router.go`.
* Worker HTTP API and job lifecycle: `/Users/estrella/ws/cloudreve/cloudreve-ffmpeg-worker/internal/httpapi/server.go`, `/Users/estrella/ws/cloudreve/cloudreve-ffmpeg-worker/internal/jobs/jobs.go`.
* Worker FFmpeg templates: `/Users/estrella/ws/cloudreve/cloudreve-ffmpeg-worker/internal/ffmpeg/ffmpeg.go`.

## Implementation Notes

* Backend added signed subtitle URLs at `/api/v4/video/worker/subtitle/:taskId` and external remote worker submission to `/v1/jobs/external-subtitle-burn-url`.
* Worker added external URL jobs with dual downloads and fixed FFmpeg templates for `.srt`, `.ass`, and `.ssa`.
* Verification passed with focused backend tests, full backend tests, full worker tests, and `git diff --check` in both repositories.
