# Remote FFmpeg Worker Design

## Decision

Build a dedicated Go worker as a separate private repository, placed next to the current backend workspace during local development:

```text
/Users/estrella/ws/cloudreve/backend
/Users/estrella/ws/cloudreve/cloudreve-ffmpeg-worker
```

The first version supports embedded/container subtitles only. External `.srt/.ass/.ssa` subtitle support remains a later extension.

## Repository Shape

```text
cloudreve-ffmpeg-worker/
  cmd/worker/main.go
  internal/auth/
  internal/config/
  internal/ffmpeg/
  internal/jobs/
  internal/httpapi/
  Dockerfile
  docker-compose.example.yml
  .github/workflows/docker.yml
  README.md
```

The service is intentionally small: one binary, one container, no Redis, no database. Cloudreve remains the source of truth for user tasks. The worker only executes temporary FFmpeg jobs.

## HTTP API

### `GET /healthz`

Returns basic health information.

### `POST /v1/jobs/embedded-subtitle-burn`

Multipart form request:

| Field | Type | Required | Meaning |
|---|---|---:|---|
| `video` | file | yes | Input video file |
| `embedded_index` | integer | yes | Subtitle stream index passed to `subtitles=...:si=<index>` |
| `duration` | float | no | Duration in seconds, used for progress percentage |
| `bitrate` | integer | no | Source bitrate for `-maxrate`; `bufsize` is `bitrate * 2` |
| `style_profile` | string | no | Reserved. First version ignores style for embedded subtitles |

Response:

```json
{
  "job_id": "01HV...",
  "status": "queued"
}
```

### `GET /v1/jobs/{job_id}`

Response:

```json
{
  "job_id": "01HV...",
  "status": "running",
  "progress": 42.5,
  "error": "",
  "stderr_tail": "",
  "output_size": 0
}
```

Statuses:

```text
queued
running
completed
failed
canceled
```

### `POST /v1/jobs/{job_id}/cancel`

Cancels a running job by canceling the job context and killing the FFmpeg process.

### `GET /v1/jobs/{job_id}/output`

Returns `output.mp4` when the job is completed.

## FFmpeg Command

The worker builds the command from a fixed template:

```text
ffmpeg
  -v warning
  -y
  -i input.mp4
  -vf subtitles=filename='<input.mp4>':si=<embedded_index>
  -c:v libx264
  -crf 18
  -preset medium
  [-maxrate <bitrate> -bufsize <bitrate*2>]
  -c:a copy
  -movflags +faststart
  -nostats
  -progress pipe:1
  output.mp4
```

The API never accepts arbitrary FFmpeg arguments or arbitrary filtergraph text.

## Job Storage

Work directory example:

```text
/work/jobs/<job_id>/
  input.mp4
  output.mp4
  stderr.log
  progress.log
  state.json
```

Completed and failed jobs are retained for a configurable TTL. A simple cleanup goroutine removes expired job directories.

## Security Model

The worker uses four layers of restriction:

| Layer | Rule |
|---|---|
| Network | Bind to `127.0.0.1` when behind a reverse proxy or tunnel; if exposed, restrict source IP with `nftables` on hk3 |
| Auth | Require `Authorization: Bearer <WORKER_API_KEY>` for all `/v1/*` endpoints |
| Input | Enforce max upload size, integer subtitle index, positive bitrate/duration, and fixed file names in per-job directories |
| FFmpeg | Use fixed argument list through `exec.CommandContext`; no shell and no caller-provided arguments |


API key rules:

```text
WORKER_API_KEY must be set in production mode.
/v1/* rejects missing, malformed, or wrong Authorization headers.
Token comparison uses constant-time comparison.
Logs never print the token.
```

Recommended hk3 deployment binds the container to localhost first:

```yaml
ports:
  - "127.0.0.1:3301:3301"
```

If Cloudreve needs public access to hk3, expose the port through Caddy or Docker only after adding `nftables` rules that allow the Cloudreve VPS source IP and drop other sources.

## Cloudreve Integration Later

Cloudreve can call the worker only when a setting is enabled:

```text
video_ffmpeg_worker_enabled=false
video_ffmpeg_worker_endpoint=https://...
video_ffmpeg_worker_token=...
video_ffmpeg_worker_timeout=...
```

The local FFmpeg implementation remains the default fallback.

## Validation Plan

1. Unit test worker config, auth middleware, job state transitions, FFmpeg argument builder, progress parser, and cancellation.
2. Integration test with a generated MP4 that contains an embedded subtitle stream.
3. Smoke test on hk3 using the sample under `/tmp/cloudreve-ffmpeg-rest-test/` after generating or selecting an embedded-subtitle sample.
4. Add Cloudreve-side integration only after the standalone worker produces a valid `output.mp4` and exposes progress correctly.

## Cloudreve Integration Decision: Source URL Pull Mode

Date: 2026-04-27

Large-file smoke testing showed that piping multi-GB files through the local development machine is inefficient. For production Cloudreve integration, use worker pull mode instead of Cloudreve multipart-pushing the whole file in the create-job request.

### Decision

Cloudreve creates a short-lived, single-purpose source URL for the selected video file. The worker receives this URL, downloads the source into its work directory, then runs FFmpeg.

The existing multipart endpoint remains useful for local smoke tests and small-file debugging. The production path should use a JSON endpoint such as:

```http
POST /v1/jobs/embedded-subtitle-burn-url
Authorization: Bearer <WORKER_API_KEY>
Content-Type: application/json
```

Request shape:

```json
{
  "source_url": "https://cloudreve.example.com/api/v4/video/worker/source/<task_id>?expires=...&signature=...",
  "embedded_index": 0,
  "duration": 2772.33,
  "bitrate": 6492556
}
```

### Cloudreve temporary source URL

Add an internal download endpoint dedicated to worker pulls. It should not be a normal share link.

Suggested shape:

```http
GET /api/v4/video/worker/source/{task_id}?expires=<unix>&signature=<hmac>
```

Signature payload should bind at least:

```text
task_id
file_id
entity_id
expires
worker_nonce
```

Rules:

* Short TTL, for example 30 minutes.
* Only the selected source entity can be downloaded.
* Supports HTTP Range requests.
* Does not count as user/share download.
* Logs must avoid printing the full signed URL.
* If possible, additionally restrict source endpoint access to the worker IP.

### Progress display

Show transfer and transcode as two separate fields, instead of merging them into one number:

```text
传输：72%
转码：35%
预计剩余：12m 30s
远端 worker：hk3
```

Worker status should expose separate progress values:

```json
{
  "status": "running",
  "download_progress": 72.4,
  "transcode_progress": 35.1,
  "output_size": 123456789,
  "downloaded_bytes": 1677721600,
  "total_bytes": 2249938818
}
```

Cloudreve task state can store the same split values:

```go
WorkerDownloadProgress float64 `json:"worker_download_progress,omitempty"`
WorkerTranscodeProgress float64 `json:"worker_transcode_progress,omitempty"`
WorkerDownloadedBytes   int64   `json:"worker_downloaded_bytes,omitempty"`
WorkerTotalBytes        int64   `json:"worker_total_bytes,omitempty"`
WorkerOutputSize        int64   `json:"worker_output_size,omitempty"`
WorkerStartedAt         int64   `json:"worker_started_at,omitempty"`
```

ETA can be calculated by Cloudreve from `WorkerStartedAt` and `WorkerTranscodeProgress`. Show ETA only after transcode progress reaches a small threshold, such as 3%, to avoid noisy early estimates.

### Why not multipart push for production

Multipart push keeps the Cloudreve queue task tied to a large upload request and makes retry semantics unclear. Source URL pull separates create-job from file transfer, allows the worker to own download retry/range behavior later, and keeps Cloudreve's create-job call small and fast.

## Large File Smoke Test Result

Date: 2026-04-27

Large embedded-subtitle test completed on hk3 with the `Joy.of.Life.S01E46` sample.

```text
input_size=2249938818
input_duration=2772.33
input_bitrate=6492556
job_id=18aa30cd508a3817-9212ab9596247029741b3396b82dcd62
status=completed
progress=100
output_size=2045394458
output_duration=2772.330500
output_bitrate=5902310
stderr_tail=
```

The worker output endpoint supports resumable reads:

```text
HEAD /v1/jobs/{job_id}/output -> 200 OK, Accept-Ranges: bytes, Content-Length: 2045394458
GET  /v1/jobs/{job_id}/output with Range: bytes=0-1023 -> 206 Partial Content
```

The generated MP4 has `moov` near the beginning, which confirms the worker's `-movflags +faststart` behavior for this large output.

## Result Transfer Decision

Cloudreve should pull the completed output from the worker. The worker should not push the final MP4 back to Cloudreve in the first integration version.

Recommended result flow:

1. Cloudreve submits a URL-based worker job and stores `worker_job_id` in the queue task state.
2. Cloudreve polls `GET /v1/jobs/{job_id}` until `status=completed`.
3. Cloudreve sends `HEAD /v1/jobs/{job_id}/output` and records `Content-Length`.
4. Cloudreve downloads to a local `.part` file.
5. If the download is interrupted, Cloudreve resumes with `Range: bytes=<current_size>-`.
6. Cloudreve verifies final size equals worker `output_size` or response `Content-Length`.
7. Cloudreve atomically renames the `.part` file to the final output path, then continues the existing burned-output persistence logic.

This keeps Cloudreve in control of storage writes, avoids granting the worker upload privileges to Cloudreve, and makes retries idempotent for a single-person deployment.

Progress display can keep Estrella's requested two fields by treating transfer as a phase-aware value:

```text
传输：源文件下载 72%
转码：35%
```

After transcoding completes and Cloudreve starts pulling the result back:

```text
传输：结果回传 18%
转码：100%
```

The backend task state should add a small `worker_transfer_phase` value, for example `source_download` or `output_download`, while preserving the separate `transfer_progress` and `transcode_progress` fields.

## Cloudreve Storage Path Decision

Date: 2026-04-27

Cloudreve VPS has a small system disk and a large data disk. Current production mount layout confirms the backend container uses data-disk mounts:

```text
/data/cloudreve/data -> /cloudreve/data
/data/qbittorrent/downloads -> /downloads
```

Current subtitle-burn temporary output path is built with:

```text
util.DataPath(<temp_path>/subtitle-burn/<file_id>/burned_<timestamp>.mp4)
```

With the current container layout, this resolves under `/cloudreve/data`, backed by host `/data/cloudreve/data`, not the system disk.

Remote-worker result download must keep the same rule:

* `.part` files are written under `util.DataPath(temp_path/remote-ffmpeg/<task_id>/...)` or the existing `temp_path/subtitle-burn/...` family.
* Final burned output is copied or renamed into the existing source-adjacent `burned/` directory through `persistBurnedOutput`.
* Avoid `os.TempDir()` for multi-GB worker outputs because container `/tmp` may be backed by the system disk.
* Startup or task logs should include the resolved worker temp directory, without signed URLs or API keys.

## Cloudreve Backend Integration Plan 2026-04-27

首版后端集成保持本地 FFmpeg 为默认执行路径，远程 worker 仅在配置启用、endpoint 和 API key 均存在，并且字幕选择可以安全确定为内封字幕时使用。外部字幕继续走本地 FFmpeg；auto 模式如果目录中存在外部字幕，也继续走本地路径，避免把用户原本期望的外部字幕错误映射为内封字幕。

新增的 worker source URL 使用独立的后端 endpoint：

```http
GET  /api/v4/video/worker/source/{task_id}
HEAD /api/v4/video/worker/source/{task_id}
```

签名 payload 绑定 `task_id`、`file_id`、`entity_id`、`expires`、`nonce`，由 Cloudreve `secret_key` 派生 HMAC，短时有效。该 endpoint 只服务被签名绑定的源实体，支持 HTTP Range，不走普通分享或下载次数统计。日志和错误文本需要隐藏 API key 与 `signature` 参数。

远程结果拉回由 Cloudreve 主服务发起，写入 `util.DataPath(temp_path/subtitle-burn/<file_id>/...)` 下的 `.part` 文件。拉取前先 `HEAD /output` 获取 `Content-Length`，已有 `.part` 文件时使用 `Range: bytes=<current_size>-` 续传，最终大小与 worker `output_size` 或 `Content-Length` 一致后原子 rename，再复用 `persistBurnedOutput` 写入源文件相邻的 `burned/` 目录。

任务状态新增以下字段，旧前端可继续读取原有任务进度；后续前端可以读取这些字段展示两行进度：

```text
worker_job_id
worker_transfer_phase       source_download | output_download
worker_transfer_progress
worker_transcode_progress
worker_downloaded_bytes
worker_total_bytes
worker_output_size
worker_started_at
```

后端 `Progress` 兼容保留原任务阶段和 `ffmpeg` 项，同时增加 `worker_transfer` 与 `worker_transcode` 进度项；`Summary.Props` 增加 `worker_transfer_phase`、`worker_transfer_progress`、`worker_transcode_progress`、`worker_output_size`，供任务列表在后续前端版本中读取。

## Frontend Follow-up Plan 2026-04-27

本轮后端首版优先保持现有前端兼容，前端作为单独后续实施项。前端计划如下：

1. 任务列表和任务详情识别 `summary.props.worker_transfer_phase`、`summary.props.worker_transfer_progress`、`summary.props.worker_transcode_progress`、`summary.props.worker_output_size`，并兼容无这些字段的旧任务。
2. 任务进度弹层识别 `worker_transfer` 和 `worker_transcode` 两个进度 key，展示两行进度：传输与转码。传输阶段根据 `worker_transfer_phase` 显示为“源文件下载”或“结果回传”。
3. 系统设置页面在视频编码相关设置旁增加远程 FFmpeg Worker 配置：启用开关、endpoint、API key 设置状态、timeout、poll interval。
4. API key 只允许写入或清空，前端不回显明文；后端设置响应如需展示，只返回 set/unset 状态。
5. 本地验收需要同时关注 `backend/assets` 子模块和同级 `../frontend`。本地开发可以在 `../frontend` 运行前端；生产发布以 `backend/assets` 子模块指针为准。
6. 浏览器验收至少覆盖系统设置展示、任务进度两行展示、无 worker 字段旧任务的兼容显示。

## Frontend Static Build And Release Note 2026-04-27

当前后端 Dockerfile 的真实发布关系已经确认并写入 `.trellis/spec/ops/deployment.md`：后端镜像构建读取 `backend/assets` 子模块，运行 `npm run build` 生成 `assets/build`，再 zip 到 `application/statics/assets.zip`，最终通过 `application/statics/statics.go` 的 `go:embed` 编进后端二进制。GitHub Actions Docker workflow 使用 `actions/checkout` 的 `submodules: recursive`。

同级 `../frontend` 只用于本地开发和浏览器验收。后续前端实现完成后，发布必须推送前端仓库并更新 `backend/assets` 子模块指针，再触发后端 Docker 镜像构建。生产部署时还需要检查 `/data/cloudreve/data/statics` 是否存在；该目录如果存在，会覆盖内嵌静态资源，可能导致生产镜像仍运行旧前端。

## Frontend Implementation 2026-04-27

Added the remote worker frontend follow-up in both local development frontend and the backend `assets` submodule source.

System settings now load and edit these worker settings under the media/video processing page:

```text
video_ffmpeg_worker_enabled
video_ffmpeg_worker_endpoint
video_ffmpeg_worker_api_key
video_ffmpeg_worker_api_key_set
video_ffmpeg_worker_timeout
video_ffmpeg_worker_poll_interval
```

`video_ffmpeg_worker_api_key` is redacted on admin reads and on setting save responses. The UI displays only set/unset/edited state and keeps the edit input blank unless a replacement key is being typed. Blank API key patches are ignored so an existing key is not overwritten by the blank display value.

Task UI now understands the worker progress fields from the backend task summary and phase progress API:

```text
worker_transfer_phase
worker_transfer_progress
worker_transcode_progress
worker_output_size
worker_transfer
worker_transcode
```

Task details render persistent transfer and transcode progress bars. The task row status renders a compact worker progress summary for subtitle burn tasks. The phase progress popover also renders worker transfer and worker transcode rows. Old tasks without worker fields continue through the existing task progress display.

Validation completed:

```text
npm test -- --run src/component/Pages/Tasks/__tests__/VideoTaskList.test.tsx   # ../frontend
npm run build                                                                 # ../frontend
npm test -- --run src/component/Pages/Tasks/__tests__/VideoTaskList.test.tsx   # assets
npm run build                                                               # assets
go test -count=1 ./pkg/queue ./pkg/setting ./service/admin ./inventory                                  # backend
```

Browser validation was not completed in this session because no authenticated local Cloudreve backend session was available for the admin settings page. Static checks, unit tests, and frontend builds passed for both frontend roots. Existing dirty assets files were preserved; `public/locales/*/application.json` was merged rather than replaced.

## Remaining Work 2026-04-28

Current production validation task:

```text
worker_job_id=18aa800dd725cb0d-8271de855d698c624fdac088dc9d17d6
state_at=2026-04-28 19:02 CST
status=running
download_progress=100
transcode_progress=18.84
output_size=290455600
```

Remaining checklist before closing the remote FFmpeg worker task:

1. Complete production end-to-end validation for the active subtitle-burn job:
   - wait for hk3 worker status to become `completed`;
   - verify Cloudreve enters `output_download` and pulls the worker output;
   - verify the burned MP4 is persisted into the source-adjacent `burned/` directory;
   - verify browser playback works and does not regress into 502/range playback issues.
2. Add ETA display for remote worker tasks:
   - display ETA only after transcode progress reaches a small threshold such as 3%;
   - prefer backend-provided `worker_started_at` for stable estimates across refreshes;
   - frontend fallback can estimate from `task.duration` and `worker_transcode_progress` if backend timestamp is not yet available;
   - update task row, task details, and tests in both `../frontend` and `backend/assets` as needed.
3. Decide how production settings should appear in the admin UI when runtime environment variables override database settings:
   - current production execution works through `CR_SETTING_video_ffmpeg_worker_*` environment overrides;
   - admin UI may still show database values such as disabled;
   - if fixed, ensure API key remains write-only and environment-provided secrets are never returned.
4. Clean up Trellis task documents:
   - remove or mark obsolete SSH one-shot runner text in `prd.md`;
   - keep final design as Go dedicated worker + API key + Docker private image + public endpoint with IP allowlist;
   - record final worker image, backend image, hk3 firewall, Cloudreve env override, and production validation result.
5. Finish Trellis task lifecycle:
   - run final checks after any ETA/settings UI changes;
   - update specs if new contracts are added;
   - use `/finish-work` when implementation, deployment, validation, and docs are complete.

Operational reminder: backend Docker production uses the `backend/assets` submodule, not sibling `../frontend`; frontend-visible releases must update and deploy the backend image after the assets pointer changes.

## Final Check And Scope Split 2026-04-29

Trellis check result:

* Remote FFmpeg Worker first production version satisfies the core task scope.
* Backend production is running `ghcr.io/lyyqwq/cloudreve:sha-3ab90c1`.
* Worker production is running `ghcr.io/lyyqwq/cloudreve-ffmpeg-worker:sha-0d8fa6438f9ca41f56b30d6d8313e70a4585d14d`.
* Production task `13126` completed through the remote worker path.
* Generated file record and entity size match the worker output size `1508298774`.
* Generated MP4 exists under the Cloudreve data disk `burned/` directory.
* Cloudreve content endpoint returned HTTP `206` for the generated MP4.
* Cloudreve VPS had no local `ffmpeg` process during the remote worker production validation.

Quality evidence:

```text
backend go test -count=1 ./...
worker go test -count=1 ./...
backend git diff --check
assets git diff --check
../frontend git diff --check
assets targeted VideoTaskList tests
```

Frontend full lint/typecheck note:

* `assets npm run lint` has an existing baseline failure in `src/component/FileManager/TreeView/TreeFile.tsx` from an older change.
* `npx tsc --noEmit` has many existing baseline type errors.
* Remote worker frontend changes were validated with targeted task UI tests and production Docker build rather than requiring unrelated historical frontend baseline cleanup.

Browser validation boundary:

* Settings UI had been viewed after deployment and showed the remote worker fields.
* Production worker settings were configured through private runtime environment overrides.
* Task progress and ETA shipped in the embedded `assets` frontend through the backend image.
* Browser playback produced new errors after the worker task completed; because the generated file exists and content range serving returned `206`, this is split into a separate playback investigation task rather than blocking the worker infrastructure task.

Follow-up task candidates:

1. Remote worker subtitle font rendering: inspect worker fontconfig, installed CJK fonts, ASS style font names, FFmpeg subtitles filter font fallback, and output glyph region preference.
2. Background task page and workflow API timeout: inspect `/api/v4/workflow`, old remote-download task queue behavior, and high-frequency `suspending`/`processing` transitions.
3. Browser playback after burned output: inspect Network requests for `路径不存在`, `failed to get login user`, signed content URL freshness, frontend navigation state, and static asset version.
4. Frontend API contract cleanup: inspect `/api/v4/user/setting/policies` returning `404` in production and decide whether to remove the frontend request or add backend compatibility.

Secret handling note:

* Real API keys, Bearer tokens, signed URLs, SSH keys, and cookies were not printed in final summaries or Trellis notes.
* A supplemental scan of recent backend, assets, and worker commits found only code identifiers, test placeholders, README examples, and standard GitHub Actions `secrets.GITHUB_TOKEN`; no real VPS endpoint, deploy key path, `.env`, `.ssh`, or private key file was tracked.
* `AGENTS.md` now includes a project rule requiring diff and secret checks before staging, committing, or pushing.
