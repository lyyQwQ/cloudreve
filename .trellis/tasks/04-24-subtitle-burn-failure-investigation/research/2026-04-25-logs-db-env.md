# 2026-04-25 字幕烧录失败只读调查

## Scope

- 只读检查线上 Cloudreve 容器日志、Postgres task 表、Cloudreve 容器内 ffmpeg/libass/fontconfig 能力。
- 未执行重启、取消任务、写 DB、删除文件、部署或 qB/Cloudreve 状态修改。

## Evidence Summary

### Cloudreve logs

- `docker logs --since 30d cloudreve` 中按字幕/ffmpeg/libass 关键词过滤未命中可用日志。
- 说明应用日志窗口内没有保留对应业务日志，或错误主要保存在 DB task `public_state.error`。

### DB task samples

- 最近 3 个 `video_subtitle_burn` 任务均为 `error`，时间集中在 `2026-04-14 19:41-19:44 +08`。
- 3 个失败样本均为 embedded subtitle mode：
  - `{"mode": "embedded", "embedded_index": 0}`
- 失败阶段：ffmpeg filtergraph 解析阶段，不是任务创建、ffprobe、队列恢复或结果持久化阶段。
- 错误形态一致：
  - `failed to invoke ffmpeg: exit status 234`
  - stderr 包含 `Trailing garbage after a filter`
  - stderr 包含 `Error parsing filterchain 'subtitles=<video path>:si=0'`
  - stderr 里的视频路径包含中文目录和方括号 `[` / `]` 等特殊字符。
- 历史上有多个 `video_subtitle_burn` completed 样本，说明字幕烧录能力不是整体不可用，而是特定路径/文件名触发。

### Runtime environment

- Cloudreve 容器内存在 `/usr/bin/ffmpeg` 和 `/usr/bin/ffprobe`。
- ffmpeg version: `8.0.1`。
- ffmpeg build 启用了 `--enable-libass`、`--enable-libfontconfig`、`--enable-libfreetype`、`--enable-libfribidi`、`--enable-libharfbuzz`、`--enable-libx264`。
- ffmpeg filters 包含：
  - `ass`
  - `subtitles`
- encoders 包含：
  - `libx264`
  - `aac`
- fontconfig 可用，`fc-list` 存在，容器内有 Noto / Noto CJK 字体。
- settings:
  - `temp_path=temp`
  - `video_ffmpeg_threads=1`
  - `video_ffmpeg_nice=10`

### `filename=` support check

- Read-only command: `ffmpeg -hide_banner -h filter=subtitles`
- Online ffmpeg 8.0.1 reports `subtitles AVOptions` with:
  - `filename <string> set the filename of file to read`
  - `f <string> set the filename of file to read`
  - `stream_index`
  - `si`
  - `force_style`
- Conclusion: the planned named option form `subtitles=filename='<path>':si=<index>` is supported by the deployed ffmpeg.

## Root-Cause Hypothesis

高置信根因：`pkg/queue/video_queue.go` 构造 `subtitles=` filter 参数时，对 filename 的 ffmpeg filtergraph escaping 不够稳健。

当前代码形态：

```go
return fmt.Sprintf("subtitles=%s:si=%d", escapeFFMpegSubtitlePath(input), *option.EmbeddedIndex), VideoSubtitleModeEmbedded, nil
```

Current escape helper already escapes several filtergraph-sensitive characters:

```go
strings.NewReplacer(
    `\\`, `\\\\`,
    `:`, `\\:`,
    `'`, `\\'`,
    `,`, `\\,`,
    `;`, `\\;`,
    `[`, `\\[`,
    `]`, `\\]`,
)
```

This suggests the failure is not just one missing escaped character; the bare `subtitles=<path>:si=0` form itself is brittle for complex paths.

失败样本中的 filter 形态：

```text
subtitles=/cloudreve/data/uploads/.../\[...\]...mkv:si=0
```

ffmpeg 报错说明 filtergraph parser 没有把完整路径当作 filename，而是在包含方括号/特殊字符的路径中途解析中断，导致后续路径片段被当成 filtergraph 的“多余内容”。

## Suggested Fix Direction

- 不改 ffmpeg 环境、不改部署、不改队列架构。
- 最小代码修复应聚焦 `buildSubtitleFilterArg` / `buildExternalSubtitleFilterArg` / `escapeFFMpegSubtitlePath`：
  - 使用 ffmpeg subtitles filter 的 named option，例如 `subtitles=filename='<escaped path>':si=0`。
  - 对 embedded、auto fallback embedded、external subtitle 三条路径统一使用同一个安全 builder。
  - 保留 `.srt` 的 `force_style` 逻辑。
  - 增加包含中文、空格、方括号、逗号、分号、单引号等路径的单测。
- 若本地环境有 ffmpeg，可增加一个轻量集成测试或用 fake ffmpeg 捕获 `-vf` 参数，断言生成的 filter 参数使用 `filename=` 且特殊字符被安全包裹。

## Need User Re-trigger?

当前不需要 Estrella 立即触发新任务；已有失败样本足以制定代码修复计划。

如果修复后需要验收，再由 Estrella 手动触发一次同类文件名/路径的字幕烧录任务，并观察 task 状态和 ffmpeg stderr。
