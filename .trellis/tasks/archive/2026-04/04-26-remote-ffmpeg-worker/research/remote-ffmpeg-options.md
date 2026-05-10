# Research: remote-ffmpeg-options

- Query: simplest maintainable design for offloading Cloudreve FFmpeg subtitle burn tasks to a stronger remote VPS; compare SSH one-shot runner, remote HTTP worker service, and shared storage approach.
- Scope: mixed
- Date: 2026-04-26

## Findings

### Files Found

- `.trellis/tasks/04-26-remote-ffmpeg-worker/prd.md` - task goal: evaluate cross-VPS FFmpeg execution for subtitle burn first, keep Cloudreve task state on the main service, and keep a local FFmpeg rollback path.
- `.trellis/spec/backend/media-processing.md` - executable contracts for FFmpeg subtitle filters, stderr preservation, `-movflags +faststart`, and required queue tests.
- `.trellis/spec/backend/queue-task-lifecycle.md` - contracts for resumable tasks, runtime-only state, cancellation, progress, and safe restored-task behavior.
- `.trellis/spec/ops/index.md` - deployment and secret-handling boundary: no real hostnames, keys, tokens, or passwords in tracked files.
- `pkg/queue/video_queue.go` - current subtitle burn, HLS, FFmpeg progress parsing, local output persistence, cleanup, and node selection code.
- `pkg/queue/video_queue_test.go` - existing focused tests for video task state, FFmpeg argument construction, stderr preservation, progress, and runtime options.
- `service/video/video.go` - task creation, subtitle request validation, duplicate-task checks, and queue submission.
- `service/explorer/workflows.go` - user-visible cancellation path for video tasks.
- `application/dependency/dependency.go` - video queue construction and resumable task registration.
- `pkg/cluster/node.go` - existing slave-node HTTP task create/get API and HMAC client wiring.
- `service/node/task.go` - slave node accepts only archive/extract/upload task types today.
- `pkg/filemanager/workflows/archive.go` - existing master-to-slave polling pattern for archive tasks.
- `pkg/filemanager/workflows/upload.go` - existing slave upload task and progress pattern.

### Current Code Patterns

- `pkg/queue/video_queue.go:157` starts `VideoSubtitleBurnTask.Do`, parses persisted `VideoTaskState`, logs start, probes input, builds subtitle filter, runs FFmpeg, persists the burned file, and marks completion.
- `pkg/queue/video_queue.go:174` calls `selectVideoExecutionNode`, but `pkg/queue/video_queue.go:1486` only accepts active master nodes for subtitle burn and falls back from non-master nodes to master. Remote video execution is therefore not active today.
- `pkg/queue/video_queue.go:186` resolves the source file to a local physical path through `resolveVideoTaskInput`; `pkg/queue/video_queue.go:546` depends on the primary entity source being locally readable.
- `pkg/queue/video_queue.go:198` builds the final subtitle filter using local paths. Any remote execution path must rebuild or translate the filter so FFmpeg sees remote workdir paths.
- `pkg/queue/video_queue.go:218` writes remote/local intermediate output to a temp path, and `pkg/queue/video_queue.go:640` later persists that output beside the original file under `burned/`.
- `pkg/queue/video_queue.go:222` updates FFmpeg progress through `updateVideoTaskFFMpegProgress`, and `pkg/queue/video_queue.go:329` exposes a separate `ffmpeg` progress entry when duration is known.
- `pkg/queue/video_queue.go:1013` runs local FFmpeg through `runSubtitleBurnFFMpeg`, captures stdout progress, keeps a bounded stderr tail, and returns stderr in errors.
- `pkg/queue/video_queue.go:1134` parses FFmpeg `-progress pipe:1` output by reading `out_time_us` and `progress=end`. This parser can be reused for an SSH-run command if remote stdout streams the same lines.
- `pkg/queue/video_queue.go:1188` centralizes local FFmpeg command creation and injects threads/nice settings. A remote runner can be added beside this boundary instead of modifying all task phases.
- `pkg/queue/video_queue.go:1277` resolves auto/external/embedded subtitle mode from local files. For remote execution, keep local mode resolution but build a second filter using remote input/subtitle paths.
- `pkg/queue/video_queue.go:1438` scans external `.srt`, `.ass`, and `.ssa` files in the input directory. A minimal remote transfer must include the selected external subtitle, and auto mode must transfer the selected first subtitle when present.
- `pkg/queue/video_queue.go:1600` and `pkg/queue/video_queue.go:1631` persist phase and FFmpeg progress into `PrivateState`, matching the queue lifecycle spec.
- `pkg/queue/queue.go:290` registers a context cancel callback before `Task.Do`; `pkg/queue/queue.go:339` converts a cancellation-marked task to `StatusCanceled` when the run context ends.
- `pkg/queue/registry.go:73` sets the task canceled flag and calls the registered cancel callback. Remote execution must bind this callback to the whole SSH/transfer process via `context.Context`.
- `service/explorer/workflows.go:443` cancels running video tasks via `TaskRegistry.Cancel`; queued video tasks are marked canceled in the database at `service/explorer/workflows.go:455`.
- `application/dependency/dependency.go:688` builds `VideoProcessQueue` with `WithResumeTaskType(queue.VideoSubtitleBurnTaskType, queue.VideoHLSSliceTaskType)`, so any remote state fields must remain safe after restart.
- `pkg/cluster/node.go:269` can create a task on a configured slave over HTTP, and `pkg/cluster/node.go:302` can poll it.
- `service/node/task.go:34` accepts only `SlaveUploadTaskType`, `SlaveCreateArchiveTaskType`, and `SlaveExtractArchiveType`; video remote work would need new slave task support.
- `pkg/filemanager/workflows/archive.go:256` and `pkg/filemanager/workflows/archive.go:267` show the existing create-remote-task then poll-progress pattern, with `ResumeAfter` for polling.
- `pkg/filemanager/workflows/upload.go:69` runs an upload task on a slave and reports progress through an in-memory progress map.

### Option A: SSH One-Shot Runner

Design:

1. Main Cloudreve keeps the current `VideoSubtitleBurnTask` owner, status, progress, error, output persistence, and cleanup behavior.
2. Add a small `SubtitleBurnRunner` boundary near `runSubtitleBurnFFMpeg`: local runner keeps the current code; SSH runner performs transfer, remote command execution, and result download.
3. Keep probe, subtitle-mode resolution, language detection, duplicate checks, and final `persistBurnedOutput` on the main VPS.
4. For remote execution, create a unique remote work directory such as `<remote_work_dir>/<task_id>-<unix_nano>/`, upload the input video and selected external subtitle when needed, run FFmpeg against remote workdir paths, then download only `output.mp4` to the existing local `outputPath`.
5. Stream remote FFmpeg stdout back through SSH so `readFFMpegProgressOutput` can continue parsing `-progress pipe:1`; capture remote stderr into the existing bounded stderr tail.
6. Use `context.Context` for every local subprocess: upload command, SSH command, and download command. A wrapper script on the remote side should keep FFmpeg in the foreground or kill its process group on `HUP`, `INT`, and `TERM`.
7. Store only minimal extra state in `VideoTaskState`, if needed: remote job id/workdir, execution mode, and transfer phase. Avoid storing secrets.
8. Rollback is a configuration switch: disabled remote mode calls the existing local runner unchanged.

Cancellation and progress:

- Progress behavior stays close to the current local FFmpeg path because FFmpeg progress lines can flow through SSH stdout.
- Main-service cancellation already cancels the task context; `exec.CommandContext` can terminate the SSH/rsync child process. The remote wrapper must clean child FFmpeg by process group to reduce orphaned work.
- A canceled remote task can leave a remote workdir if the network drops before cleanup. This is acceptable for the first version if all workdirs are under one configured prefix and an ops note provides a read-only/list and cleanup command with placeholders.

Failure handling:

- Transfer failure before FFmpeg starts should fail the task with a clear transfer-stage error. Runtime automatic fallback to local after partial remote transfer should be disabled by default to avoid duplicate CPU spikes and unclear user-visible behavior.
- FFmpeg failure should preserve remote stderr in the current error path, matching `.trellis/spec/backend/media-processing.md`.
- Result download failure should leave the remote workdir intact for diagnosis, fail the task, and allow manual retry.
- The safest rollback is operational: set `video_ffmpeg_remote_enabled=false` and restart/reload the queue so future tasks use local FFmpeg.

File transfer:

- Use `rsync -e ssh` or `scp` as an external command. `rsync` is preferable for large files because interrupted uploads/downloads can be resumed more naturally and output can be separated from FFmpeg progress.
- Keep transfer progress out of the first version, or expose coarse phases through `ProgressCurrent/ProgressTotal`. FFmpeg progress remains the useful user-facing signal during the expensive phase.
- Transfer only the video and the selected external subtitle for the initial subtitle-burn scope. `.ass` files can reference fonts or attached assets, so the remote VPS must have the needed font stack installed; bundled font-asset support can remain out of scope until a real sample requires it.

Operational profile:

- Smallest code change among the three options.
- No always-on custom service, no new public endpoint, and no task scheduler on the remote VPS.
- Uses standard SSH key management and one remote work directory.
- Debugging is simple: inspect main Cloudreve task error, remote workdir, and one SSH command.

Fit for single-person operations: best first implementation.

### Option B: Remote HTTP Worker Service

Design:

1. Add a remote worker process or extend Cloudreve slave mode so the main service submits a video task over HTTP.
2. Worker receives a task payload, fetches or receives files, runs FFmpeg, exposes task status/progress/logs, supports cancellation, and uploads or returns the output.
3. Main Cloudreve stores a remote task id in `PrivateState`, polls worker status with `ResumeAfter`, merges remote progress into the existing task progress, and persists the final output.

Cancellation and progress:

- Progress can be richer and survives main-service polling intervals if the worker persists progress.
- Proper cancellation needs a worker cancel endpoint plus remote process tracking. Existing generic slave task APIs expose create/get/cleanup, but code search found no generic cancel endpoint for slave tasks.
- A new remote task id must survive restart in `PrivateState`, following `.trellis/spec/backend/queue-task-lifecycle.md`.

Failure handling:

- Stronger long-running semantics than SSH if the worker persists job state.
- More failure modes: worker deployment drift, HMAC/signature config, worker queue state, polling timeout, orphaned remote task, and main/worker version mismatch.
- Existing Cloudreve slave infrastructure provides a precedent but not video support. `service/node/task.go` rejects unknown task types today, and archive/upload workflows would need to be generalized or copied.

File transfer:

- Either reuse stateless upload patterns from slave upload tasks or add worker fetch/upload endpoints.
- The worker service approach becomes more attractive when several FFmpeg task types need remote execution, multiple workers are needed, or progress/cancel must survive network interruptions cleanly.

Operational profile:

- Higher implementation and maintenance cost than SSH one-shot.
- Requires a permanently running worker, configuration lifecycle, authentication, health checks, logs, and version coordination.
- Uses existing architectural ideas from Cloudreve cluster/slave code, but turning that into video processing is a larger task than the current PRD needs.

Fit for single-person operations: good later if remote FFmpeg becomes a broader product capability; too much for the first subtitle-burn offload.

### Option C: Shared Storage Approach

Design variants:

1. Mount the same storage path on both VPS instances with identical local paths, then run FFmpeg remotely through SSH or a worker without uploading input/output through the application.
2. Put input/output on a shared network filesystem or object storage-backed mount, and have both main and remote VPS read/write there.

Cancellation and progress:

- Progress still depends on either SSH stdout streaming or a worker progress API.
- Cancellation semantics are not improved by shared storage; process management still needs SSH process-group cleanup or a worker cancel endpoint.

Failure handling:

- Storage availability becomes part of task correctness. Mount stalls, stale handles, permission drift, path mapping mistakes, and partial output files are harder to diagnose than explicit transfer steps.
- Current persistence code assumes local filesystem semantics: `os.Stat`, `copyLocalFile`, temp file, and `os.Rename` in `persistBurnedOutput`.
- Cross-host writes into the same directory increase the chance of partially visible outputs unless every write uses remote temp files and atomic rename on the same mounted filesystem.

File transfer:

- Removes application-level transfer code, but replaces it with mount setup, monitoring, permissions, and path mapping.
- If both VPS instances already share a robust storage layer, this can be efficient. For the current PRD, that assumption is stronger than SSH connectivity and adds operational state outside Cloudreve.

Operational profile:

- Lowest application code only when shared storage already exists and is proven.
- Highest operational coupling when introduced only for FFmpeg offload.
- Rollback is harder because mounts and path assumptions affect normal Cloudreve storage behavior.

Fit for single-person operations: not recommended as the first design unless shared storage already exists and is already part of normal production operations.

### Comparison

| Area | SSH one-shot runner | Remote HTTP worker service | Shared storage approach |
|---|---|---|---|
| Initial code size | Small: runner boundary plus transfer helpers | Medium/large: API, worker task, polling, cancel, auth/health | Small in app, large in ops |
| Operations | SSH key, remote workdir, FFmpeg dependencies | Service deployment, versioning, auth, logs, health checks | Mount lifecycle, permissions, path identity, storage health |
| Progress | Reuse FFmpeg `-progress pipe:1` stream | Poll or push worker progress | Depends on SSH or worker |
| Cancellation | Context cancels SSH; wrapper kills FFmpeg process group | Best if a cancel endpoint and persisted worker state are added | Same as SSH/worker; storage does not solve cancel |
| Failure diagnosis | Task stderr + remote workdir | Main logs + worker logs + task id + transfer logs | Task logs + mount/storage state |
| File transfer | Explicit upload/download, easy to see | Worker fetch/upload or slave upload pattern | Implicit through mounted storage |
| Rollback to local FFmpeg | Simple config switch | Config switch plus worker may still have orphan tasks | Requires storage/mount assumptions cleanup |
| Single-person fit | Strong | Moderate later | Weak unless storage already exists |

### Recommended Minimal Design

Use the SSH one-shot runner for the first implementation.

Recommended implementation boundaries:

1. Add a small runner interface in `pkg/queue/video_queue.go` or a nearby `pkg/queue/video_ffmpeg_runner.go`:
   - `RunSubtitleBurn(ctx, req) (stderr string, err error)`
   - local implementation delegates to current `runSubtitleBurnFFMpeg`.
   - SSH implementation handles upload, remote FFmpeg, output download, and cleanup.
2. Keep task orchestration unchanged:
   - `VideoSubtitleBurnTask.Do` still resolves input, probes, computes duration/bitrate/lang, updates progress, builds `outputPath`, calls a runner, verifies output exists, and calls `persistBurnedOutput`.
3. Add minimal settings with safe defaults:
   - `video_ffmpeg_remote_enabled=false`
   - `video_ffmpeg_remote_host`
   - `video_ffmpeg_remote_user`
   - `video_ffmpeg_remote_port`
   - `video_ffmpeg_remote_key_path`
   - `video_ffmpeg_remote_work_dir`
   - `video_ffmpeg_remote_strict_host_key_checking=true`
   - Keep real values in DB/admin settings or ignored local config, not tracked docs.
4. Use `rsync` for input/output transfer when available; allow `scp` only if simplicity is preferred over resume support.
5. Generate remote paths deterministically inside one remote job directory and rebuild the subtitle filter with those remote paths.
6. Keep automatic local fallback limited to preflight failures only if explicitly enabled. Default behavior should fail clearly; operational rollback is the config switch.
7. Add tests around runner selection, remote path/filter construction, stderr preservation, context cancellation behavior with fake commands, and local-runner fallback when remote mode is disabled.

### Suggested Remote Flow

1. Local task resolves `input`, `modeUsed`, `duration`, `bitrate`, selected subtitle path, and `lang`.
2. Local task creates local `outputPath` under existing temp path and persists it in `VideoTaskState`.
3. SSH runner creates a remote workdir, for example `<remote_work_dir>/<task_id>-<timestamp>/`.
4. Upload `input` as `input<ext>` and selected external subtitle as `subtitle<ext>` when needed.
5. Build remote filter:
   - embedded: `subtitles=filename='<remote_input>':si=<index>`
   - external `.srt`: `subtitles=filename='<remote_subtitle>':force_style='<style>'`
   - external `.ass/.ssa`: `subtitles=filename='<remote_subtitle>'`
6. Run remote FFmpeg with the same quality flags: `libx264`, `-crf 18`, `-preset medium`, optional `-maxrate/-bufsize`, `-c:a copy`, `-movflags +faststart`, `-nostats`, `-progress pipe:1`.
7. Download remote `output.mp4` to the local `outputPath`.
8. Attempt remote cleanup after successful download. On failure, keep workdir and include the path in logs for diagnosis.
9. Existing code verifies local output and persists it into the `burned/` folder.

### Related Specs

- `.trellis/spec/backend/media-processing.md` applies because the design changes FFmpeg command/filter construction and subtitle burn execution.
- `.trellis/spec/backend/queue-task-lifecycle.md` applies because cancellation, progress, persisted `PrivateState`, and restored task behavior are involved.
- `.trellis/spec/ops/index.md` applies because SSH, VPS access, and secret handling are involved.

### External References

- FFmpeg documentation: `-progress url` writes machine-readable progress information, which matches the current parser strategy. Reference: https://ffmpeg.org/ffmpeg-doc.html
- OpenSSH manual: SSH executes a remote command over an encrypted connection and supports non-interactive command execution. Reference: https://man.openbsd.org/ssh
- OpenSSH client configuration manual: `BatchMode` disables password/passphrase prompts, which is useful for daemon-run SSH commands. Reference: https://man.openbsd.org/ssh_config.5
- rsync manual: rsync is suitable for efficient file transfer over remote shell transports and has partial-transfer options useful for large media files. Reference: https://man7.org/linux/man-pages/man1/rsync.1.html

## Caveats / Not Found

- No existing Cloudreve video slave task type was found. `service/node/task.go` currently supports archive, extract, and upload slave tasks only.
- No generic Cloudreve slave task cancellation endpoint was found during code search; remote HTTP worker cancellation would need new API support.
- Current `selectVideoExecutionNode` actively falls back to master for non-master video nodes, so existing node selection is not an offload mechanism yet.
- Automatic fallback from remote FFmpeg to local FFmpeg after a remote failure can create ambiguous partial outputs and sudden local CPU spikes. Prefer explicit failure plus a remote-enabled configuration switch for rollback.
- `.ass` subtitle files may depend on fonts or local attachments. The first version should document required remote font packages and only expand asset transfer after real samples require it.
- Shared storage was assessed as a design option, but no evidence was found that this production setup already has a shared, identical path mount between the two VPS instances.

## Follow-up: Standalone Worker Candidates

Date: 2026-04-26

Estrella asked whether a standalone remote transcoding worker already exists, to avoid keeping SSH credentials on the main VPS.

Shortlist:

- `crisog/ffmpeg-rest`: closest shape to a small standalone API worker. It wraps FFmpeg behind HTTP endpoints, uses Redis/BullMQ for async jobs, and supports stateless or S3-compatible storage modes. Fit: possible reference or fork point; check whether subtitle burn with arbitrary filtergraph is supported before adoption.
- `FFmate`: FFmpeg automation layer with REST API, task queue, presets, webhooks, and clustering. Fit: promising as a standalone FFmpeg job server; license, maturity, and arbitrary command support need verification before use.
- `FileFlows`: full file-processing pipeline system with external processing nodes and video operations including burn-in subtitles. Fit: strong processing-node concept, but product/pipeline scope is larger than Cloudreve's single business task.
- `Tdarr` and `Unmanic`: mature media-library optimizers. Fit: less suitable because they are directory/library automation systems, not a lightweight per-request Cloudreve worker.

Current judgment: if avoiding SSH private keys is the priority, evaluate `ffmpeg-rest` or `FFmate` before writing a custom HTTP worker. If minimal integration is the priority, a constrained SSH runner remains the smallest implementation.
