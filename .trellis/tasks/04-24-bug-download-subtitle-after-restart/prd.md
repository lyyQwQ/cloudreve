# 离线下载恢复后进度与取消修复

## Goal

排查并修复线上离线下载恢复问题：

1. 离线下载任务在 Cloudreve 容器重启后进度不再更新。
2. 点击取消返回 500，后端疑似出现 `invalid memory address` / nil pointer panic 日志。
3. 如果 qBittorrent/下载器侧已经完成，Cloudreve 应继续使用持久化 handle 刷新下载器状态，并推进 transfer / seeding / completed 后续流程。

目标是按 Trellis 流程实现最小、安全、可验证的后端修复，再由 `trellis-check` 复核。避免把线上部署、上游同步、字幕处理混成一个不可回滚的大改动。

## What I Already Know

- 当前线上 Cloudreve 容器运行在 FRP 跳板端点之后；远程操作需先得到 Estrella 确认，默认只做只读检查。
- 当前线上容器镜像来自 `ghcr.io/lyyqwq/cloudreve:latest`，实际 digest 与 `sha-06f5fe3` 对应。
- 当前本地分支是 `rewrite/backend-by-module-fixed`，HEAD 为 `06f5fe3` 之后又有本地提交 `a57d52d` 用于 ignore 规则。
- `assets` 子仓库已有 unrelated dirty changes，本任务不要混入。

## Assumptions

- 离线下载问题可能与容器重启后的队列恢复、持久化任务状态、qBittorrent/Aria2 状态同步、取消逻辑空指针处理有关。
- 字幕嵌入/烧录失败证据不足，已拆到独立调查 task，不在本 P0 修复中实现。

## Requirements

- 按 Trellis 流程执行：`trellis-implement` 修复，随后 `trellis-check` 复核。
- 本次实现优先修复本地代码中已确认的高置信问题；线上部署、容器重启、数据库写入另行确认。
- 不执行远程部署、重启、取消线上任务、修改线上文件等写操作，除非 Estrella 明确批准。
- 如需复现，优先本地单测或只读线上日志；线上写操作需要单独确认。
- 离线下载恢复后，进度查询、取消、清理、选择下载文件等用户入口不能因为恢复任务尚未初始化运行态字段而 panic。
- 离线下载恢复后，Cloudreve 必须能继续使用持久化状态里的下载器 handle，在队列下一轮 `Do()` / monitor 中请求 qBittorrent/下载器状态；如果 qB 已经完成/做种，应推进 Cloudreve 任务进入后续 transfer / seeding 流程，而不是卡在旧进度。
- 队列调度必须按 `ResumeTime` 取最早到期任务，避免大量恢复任务中某些旧任务长期拿不到 monitor 机会。
- 取消恢复态任务时，如果持久化状态里已有下载器 handle，应重新创建 downloader 并通知 qBittorrent/下载器取消；接口成功必须对应 Cloudreve 状态发生可见变化。
- Lazy state 初始化必须考虑并发：`Do()` goroutine 和用户请求 goroutine 可能同时访问 `state` / `d` / `node`，实现应使用现有 task lock 或等价同步手段避免 data race。

## Acceptance Criteria

- [x] 离线下载恢复任务的 `Progress()` 不再 nil pointer panic。
- [x] 离线下载恢复任务的 `CancelDownload()` 不再 nil pointer panic；持久化 `handle` 存在时必须重新创建 downloader 并尝试取消远端任务。
- [x] 取消接口返回成功后，Cloudreve 任务状态应变为 `canceled` 或等价终态，前端列表不能继续显示旧 `suspending` 状态。
- [x] 离线下载恢复任务保留并复用持久化 `handle`；队列执行时会继续请求 qBittorrent/下载器状态，并在 qB 已完成时推进 Cloudreve 后续阶段。
- [x] 离线下载恢复任务的 `Cleanup()` / `SetDownloadTarget()` / `Summarize()` 对恢复态安全。
- [x] 增加聚焦单测：用带 `PrivateState` 和下载器 `handle` 的 `ent.Task` 调 `NewRemoteDownloadTaskFromModel`，覆盖 `Progress()` / `CancelDownload()` 不 panic、不丢 handle；尽量覆盖 `Summarize()` / `SetDownloadTarget()` / `Cleanup()` 的恢复态行为。
- [x] 队列调度器按最早 `ResumeTime` 取任务，新增测试覆盖“后入队未来任务不能阻塞已到期任务”。
- [x] 测试或实现说明覆盖并发访问风险，避免 lazy init 引入 data race。
- [x] `go test` 覆盖相关包通过。

## Out of Scope

- 不在本任务中合并官方 upstream 更新，除非 Research 证明 bug 已由官方修复且 Estrella 批准走同步方案。
- 不直接改 VPS compose、拉镜像、重启线上容器。
- 不处理字幕嵌入/字幕烧录失败；该问题转入独立调查 task。
- 不修 unrelated `assets` dirty changes。

## Technical Notes

- Trellis task: `.trellis/tasks/04-24-bug-download-subtitle-after-restart/`
- Implementation notes: `.trellis/tasks/04-24-bug-download-subtitle-after-restart/info.md`
