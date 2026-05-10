# 字幕路径特殊字符导致烧录失败修复

## Goal

修复字幕嵌入/字幕烧录任务在视频或字幕路径包含中文、方括号等特殊字符时，ffmpeg `subtitles` filter 解析失败的问题；同时修复生成后的 MP4 因 `moov` 位于文件尾部导致浏览器在线播放不稳定的问题。

## Current Understanding

- 字幕任务入口在 `service/video/video.go`。
- 字幕烧录执行主体在 `pkg/queue/video_queue.go`。
- 视频任务进度主要存储在 `PrivateState` 并按需解析，暂未发现与离线下载恢复态相同的 nil runtime-state 问题。
- 只读调查已定位失败阶段：ffmpeg filtergraph 解析 `subtitles=<path>:si=0` 时失败。
- 失败样本均为 embedded subtitle，路径包含中文、方括号和多级目录；历史上其它字幕任务可完成，说明不是整体环境不可用。
- 线上 ffmpeg 8.0.1 已确认支持 `subtitles` filter 的 `filename` / `si` named options。
- 线上嵌字后 MP4 已确认文件有效，但 `moov` 位于文件尾部，浏览器播放会产生大量 Range 请求，可能在 FRP/代理链路上表现为 502 或播放失败。

## Requirements

- 按 Trellis 流程执行：`trellis-implement` 修复，随后 `trellis-check` 复核。
- 只做最小后端代码修复，不改部署、不改容器镜像、不改队列架构。
- 修复范围聚焦 `pkg/queue/video_queue.go` 的字幕 filter 参数构造。
- embedded、auto fallback embedded、external subtitle 三条路径应统一使用安全 builder。
- 使用 `filename=` named option，避免裸拼 `subtitles=<path>:si=0` 被 filtergraph parser 错误切分。
- 保留现有 `.srt` `force_style` 行为。
- 增加覆盖中文、空格、方括号、逗号、分号、单引号等路径字符的单测。
- 字幕烧录输出 MP4 应增加 `-movflags +faststart`，让 `moov` 元数据前置，改善浏览器渐进式播放。
- 线上部署、重启、重新触发字幕任务均需 Estrella 单独确认。

## Acceptance Criteria

- [x] 明确至少一个失败样本的具体失败阶段：ffmpeg filtergraph 解析阶段。
- [x] 有可复现或可解释的错误证据，而不是泛泛猜测。
- [x] 如果是代码问题，形成单独实现计划和测试方案。
- [x] 如果是环境/部署问题，列出最小修复步骤和回滚方式；当前已排除环境/部署问题，不需要环境修复。
- [x] 修复后 embedded subtitle filter 使用 `filename=` named option，并保留 `si` 语义。
- [x] 修复后 external subtitle filter 同样使用安全 builder，并保留 `.srt` `force_style`。
- [x] 新增/更新单测覆盖特殊字符路径和现有 force_style 行为。
- [x] 字幕烧录 ffmpeg 参数包含 `-movflags +faststart`，生成 MP4 适合浏览器渐进式播放。
- [x] 相关 Go 测试通过。

## Out of Scope

- 不阻塞 `离线下载恢复后进度与取消修复` P0 task。
- 不修改视频队列架构或 ffmpeg 转码主逻辑。
- 不在本步骤部署 VPS 或重启线上容器。
