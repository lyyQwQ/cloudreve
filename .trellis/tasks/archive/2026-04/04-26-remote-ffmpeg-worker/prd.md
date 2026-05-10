# brainstorm: remote ffmpeg worker

## Goal

评估是否将 Cloudreve 的字幕烧录和后续可能的 FFmpeg 转码任务发往另一台性能更好的 VPS 执行，转码完成后再把结果回传到当前 Cloudreve 存储位置，从而降低当前 VPS 的 CPU 压力和长任务占用。

## What I already know

* 当前需求来自生产使用场景：当前 Cloudreve 已有字幕烧录任务，FFmpeg 在本机容器内执行。
* 目标方向是跨 VPS 执行 FFmpeg：主 Cloudreve 发起任务，远端 VPS 完成转码，结果再回到主 Cloudreve。
* 当前项目为单人开发运维项目，方案应优先简单、可维护、故障时容易定位。
* 当前代码中字幕烧录和 HLS 相关逻辑集中在 `pkg/queue/video_queue.go`，已有单元测试覆盖 FFmpeg 参数构造与失败处理。

## Assumptions

* 两台 VPS 之间可以通过 SSH 或内网隧道稳定传输文件。
* 远端 VPS 可以安装 Docker、FFmpeg、字体和字幕相关依赖。
* 初期目标可以仅覆盖字幕烧录任务，暂缓覆盖全部视频处理任务。
* 初期 worker 项目目录可以放在当前 `backend` 同级，避免混入 Cloudreve 主仓库。

## Open Questions

* 已关闭：SSH one-shot runner 首版方向已取消。最终首版采用 Go 专用 worker、API key 鉴权、Docker 私有镜像、Cloudreve 临时 source URL 拉取输入、Worker 输出由 Cloudreve 断点续传拉回。

## Requirements

* 方案必须适合单人维护，优先选择依赖少、配置清楚、故障容易排查的实现。
* 首版优先支持字幕烧录任务，因为该任务当前已经存在真实性能诉求。
* 首版必须支持内封字幕烧录，通过视频容器字幕流索引选择字幕；外挂字幕作为后续扩展。
* 任务状态、失败日志、取消行为需要仍由 Cloudreve 主服务统一管理。
* 首版采用专用 Go Worker：主服务提交短时签名 source URL，worker 下载输入并执行 FFmpeg，Cloudreve 轮询 worker 进度并在完成后断点续传拉回输出。
* 默认保留本地 FFmpeg 执行，远端功能由配置开关启用，便于生产回退。
* 文件传输需要避免泄露凭据，生产连接信息只能放在本地或服务器私有配置中。
* Estrella 关注 SSH 私钥放在主 VPS 的安全性，需要把 worker 模式和 SSH key 最小权限纳入方案比较。
* 需要评估 Tdarr、FileFlows、Unmanic 这类开源远端处理系统是否适合嵌入 Cloudreve 的单个字幕烧录任务流程。

## Acceptance Criteria

* [x] 明确给出远端 FFmpeg 的推荐实现方案和替代方案。
* [x] 明确主服务、远端 VPS、文件传输、结果回写、失败处理的职责边界。
* [x] 明确最小实现范围，避免一次性改造为复杂分布式系统。
* [x] Estrella 取消 SSH one-shot 首版方向，确认改为 Go 专用 worker + API key + Docker 私有镜像部署。
* [x] 明确 worker 仓库结构、接口协议、内封字幕请求格式和验证方式。
* [x] 明确需要修改的主要后端文件和验证方式。
* [x] 后端、前端 assets、worker 镜像完成 CI 构建并部署到生产。
* [x] 生产任务完成远程 worker 端到端验证，输出文件进入 Cloudreve 数据盘与数据库记录。
* [ ] 浏览器播放体验仍有异常，拆分到后续播放排查任务处理。
* [ ] 远程 worker 中文字体渲染观感需要单独排查，优先级高于其他新问题。

## Definition of Done

* Tests added/updated where behavior changes.
* Go tests for affected queue/media paths pass.
* Docs/spec notes updated if a new remote media processing convention is adopted.
* Rollback path exists: configuration can switch back to local FFmpeg execution.

## Out of Scope

* 初期暂缓实现多 worker 调度、任务抢占、复杂重试队列、Web 管理面板。
* 初期暂缓改造所有离线下载和存储架构。

## Technical Notes

* Relevant local code discovered by search: `pkg/queue/video_queue.go`, `pkg/queue/video_queue_test.go`, `service/explorer/workflows.go`.
* Relevant specs likely include `.trellis/spec/backend/media-processing.md`, `.trellis/spec/backend/queue-task-lifecycle.md`, `.trellis/spec/ops/index.md`.
* Research artifact: `.trellis/tasks/04-26-remote-ffmpeg-worker/research/remote-ffmpeg-options.md`.
* Technical design artifact: `.trellis/tasks/04-26-remote-ffmpeg-worker/info.md`.
* 2026-04-26: Checked current open-source remote processing options. Tdarr provides server/node distributed transcoding, FileFlows provides external processing nodes, Unmanic provides linked installations and worker sharing. These are more like standalone media library processing systems than Cloudreve task worker SDKs.
* 2026-04-27: ffmpeg-rest was smoke-tested on hk3. It can run simple MP4 conversion through REST, but does not expose subtitle-burn/filtergraph parameters needed by Cloudreve without extension.
* 2026-04-27: Estrella confirmed first worker implementation should support embedded/container subtitles first, because current sources are mostly embedded or already hard-subbed. External subtitle support can be added later.
* 2026-04-27: Worker project can be created as a sibling directory of the current backend workspace, for example `../cloudreve-ffmpeg-worker`.
* 2026-04-27: Estrella confirmed API key authentication is required from the first worker version; `/v1/*` uses Bearer token from `WORKER_API_KEY`.
* 2026-04-27: Large file smoke test on hk3 completed with a 2.25GB embedded-subtitle sample. The worker output endpoint supports `Accept-Ranges: bytes`, so Cloudreve integration should pull completed output with resumable `Range` downloads into a `.part` file, verify size, then atomically rename into the existing burned-output persistence path.

## Implementation Notes 2026-04-27

Standalone worker implementation was created at `/Users/estrella/ws/cloudreve/cloudreve-ffmpeg-worker`.

Phase 2 checks passed:

* `gofmt -w ./cmd ./internal`
* `go test -count=1 ./...`
* `go vet ./...`

Trellis check fixed auth strictness, progress parsing for `out_time_ms`, failed-submit cleanup, missing-output JSON error handling, and special-character subtitle filter tests.

## Deployment Notes 2026-04-27

Worker repository and deployment completed:

* GitHub private repo: `lyyQwQ/cloudreve-ffmpeg-worker`
* Latest commit deployed: `db04264e845568ecc730c843167c002414f90b91`
* Image: `ghcr.io/lyyqwq/cloudreve-ffmpeg-worker:sha-db04264e845568ecc730c843167c002414f90b91`
* GitHub Actions run: `24989825381`, conclusion `success`
* hk3 compose path: `/root/services/cloudreve-ffmpeg-worker/docker-compose.yml`
* hk3 data path: `/data/cloudreve-ffmpeg-worker`
* Bind address: `127.0.0.1:3301`
* Health check: `GET http://127.0.0.1:3301/healthz` returned `{"status":"ok"}`

Smoke test completed on hk3:

* Generated a Matroska sample with embedded SubRip subtitle stream.
* Submitted `POST /v1/jobs/embedded-subtitle-burn` with Bearer token.
* Observed progress updates from `0` to `100`.
* Downloaded output MP4 successfully.
* `ffprobe` verified MP4 output.
* CJK font issue was fixed by adding `font-noto-cjk`; repeated smoke test had empty `stderr_tail` and no missing glyph warning.

Operational note:

* GHCR package remains private. hk3 pull requires temporary `docker login ghcr.io` using a token with package read capability.

## Final Implementation And Production Validation 2026-04-29

Final shipped shape:

* Worker repository: private Go worker at sibling workspace `../cloudreve-ffmpeg-worker`.
* Worker auth: `/v1/*` uses Bearer API key; the key is supplied only through private runtime configuration.
* Worker input mode: Cloudreve creates a short-lived signed source URL; worker downloads the source file from Cloudreve and burns the selected embedded subtitle stream.
* Worker output mode: Cloudreve downloads the worker output with `.part` files, HTTP Range resume, size verification, and atomic rename before persisting into the source-adjacent `burned/` directory.
* Frontend display: settings page supports remote worker settings; task UI shows transfer progress, transcode progress, output size, and ETA when enough progress exists.
* Release rule: Docker image uses the backend `assets` submodule for embedded static files; sibling `../frontend` remains local development and browser validation workspace.

Final deployed versions:

* Backend production image: `ghcr.io/lyyqwq/cloudreve:sha-3ab90c1`.
* Backend commit: `3ab90c1 feat: expose remote worker eta`.
* Frontend assets commit: `5df459e feat: show remote worker eta`.
* Worker image: `ghcr.io/lyyqwq/cloudreve-ffmpeg-worker:sha-0d8fa6438f9ca41f56b30d6d8313e70a4585d14d`.
* Worker commit: `0d8fa64 feat: support source url subtitle jobs`.

Production validation evidence:

* Cloudreve task `13126` completed as `video_subtitle_burn`.
* Worker job `18aab7071263d440-c0c518529f6e11247c4515e213152275` completed with source download progress `100`, transcode progress `100`, and output size `1508298774`.
* Cloudreve database contains the generated MP4 file record with matching entity size and source path under the source-adjacent `burned/` directory.
* Cloudreve data disk contains the generated MP4 with the same size.
* Browser/content endpoint logs show HTTP `206` responses for the generated MP4, meaning the content serving path can return partial media ranges.
* Cloudreve VPS did not run local `ffmpeg` for the remote worker production validation, confirming CPU work moved to the worker host.

Known follow-up tasks:

* Browser playback still reported `路径不存在` and `failed to get login user` in some requests. The generated file exists, so playback behavior needs a separate browser/network/API investigation.
* Background task page showed `/api/v4/workflow` timeout/HTTP2 failures while old remote-download tasks were repeatedly switching between `suspending` and `processing`. This belongs to a separate queue/remote-download task.
* Burned subtitle font appears visually closer to Japanese glyph forms than expected Chinese glyph forms. This needs a separate worker fontconfig/ASS font fallback investigation.
* Frontend full lint/typecheck has existing baseline failures outside this task. This task relied on targeted tests, builds, and diff checks for changed areas.

## Design Update 2026-04-27: Large File Transfer

Estrella confirmed production integration should use worker pull mode with a temporary Cloudreve source URL. Progress should be displayed as two separate fields: transfer progress and transcode progress. Multipart upload remains only for smoke tests and small-file debugging.

## Frontend Follow-up Scope 2026-04-27

后端首版继续保持现有前端兼容，前端实现安排为后续单独任务。后续前端需要在任务列表、任务详情和任务进度弹层识别远程 worker 字段，包括 `worker_transfer_phase`、`worker_transfer_progress`、`worker_transcode_progress`、`worker_output_size`；展示两行进度“传输”和“转码”，其中传输根据阶段显示“源文件下载”或“结果回传”。

系统设置页面后续需要在视频编码相关设置旁增加远程 FFmpeg Worker 配置：启用开关、endpoint、API key set/unset 状态、timeout、poll interval。API key 不在前端回显明文。

前端验收范围包括系统设置展示、任务进度展示、无 worker 字段时的旧任务兼容显示。本地前端运行可使用后端同级 `../frontend`，但生产发布必须更新后端仓库的 `assets` 子模块指针，因为 Docker 镜像只读取 `backend/assets` 并把 `assets/build` zip 到 `application/statics/assets.zip` 后通过 `go:embed` 编入后端二进制。部署验收还需要检查生产 `/data/cloudreve/data/statics` 是否覆盖内嵌静态资源。
