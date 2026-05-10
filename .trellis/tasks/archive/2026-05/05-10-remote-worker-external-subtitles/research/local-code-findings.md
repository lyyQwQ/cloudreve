# Local Code Findings

## Backend

* `pkg/queue/video_remote_worker.go` currently routes only explicit embedded subtitle mode to remote worker.
* `createRemoteWorkerJob` posts to `/v1/jobs/embedded-subtitle-burn-url` with `source_url`, `embedded_index`, `duration`, and `bitrate`.
* `pkg/queue/video_queue.go` already resolves external subtitle paths from the video directory and builds correct local filters for `.srt`, `.ass`, and `.ssa`.
* `service/video/info.go` scans same-directory `.srt`, `.ass`, and `.ssa` external subtitle files.
* `service/video/worker_source.go` serves only the video primary entity through a signed URL bound to task ID, file ID, entity ID, expiry, and nonce.

## Worker

* `/Users/estrella/ws/cloudreve/cloudreve-ffmpeg-worker/internal/httpapi/server.go` exposes embedded multipart and embedded URL endpoints only.
* `/Users/estrella/ws/cloudreve/cloudreve-ffmpeg-worker/internal/jobs/jobs.go` has one runner method for embedded subtitle burn and one URL download path for the video source.
* `/Users/estrella/ws/cloudreve/cloudreve-ffmpeg-worker/internal/ffmpeg/ffmpeg.go` builds only embedded subtitle FFmpeg arguments.
* Worker output serving, progress, cancellation, job persistence, and cleanup can be reused for external subtitle jobs.

## Design Constraints

* Keep fixed FFmpeg templates; do not accept raw FFmpeg flags from Cloudreve.
* Sign subtitle file access with the same care as video source access.
* Redact signed subtitle URL query strings in backend worker error paths.
* Preserve local fallback only before a worker job starts.
