# Implementation Plan

## Evidence Recap

- 线上 DB 中最近 3 个 `video_subtitle_burn` 失败样本均停在 ffmpeg 阶段。
- stderr 共同包含 `Trailing garbage after a filter` 和 `Error parsing filterchain 'subtitles=<video path>:si=0'`。
- 失败路径包含中文、方括号 `[` / `]`、多级目录等特殊字符。
- 容器内 ffmpeg 8.0.1 可用，启用了 libass/fontconfig/freetype/fribidi/harfbuzz/libx264。
- 只读验证 `ffmpeg -h filter=subtitles` 显示支持：
  - `filename`
  - `f`
  - `stream_index`
  - `si`
  - `force_style`

## Current Code Shape

`pkg/queue/video_queue.go` 当前有三条 subtitles filter 构造路径：

- auto fallback embedded:
  - `subtitles=<escaped input>:si=0`
- explicit embedded:
  - `subtitles=<escaped input>:si=<index>`
- external:
  - `subtitles=<escaped external path>` plus optional `:force_style='...'`

`escapeFFMpegSubtitlePath` 已经处理 `\\`、`:`、`'`、`,`、`;`、`[`、`]`，但线上失败说明继续 patch 单个字符不够稳健；裸拼 `subtitles=<path>:...` 仍可能被 filtergraph parser 错误切分。

## Implementation Steps

1. 在 `pkg/queue/video_queue.go` 增加一个小的 subtitles filter builder。
2. builder 使用 named option 形式：
   - embedded: `subtitles=filename='<escaped path>':si=<index>`
   - external: `subtitles=filename='<escaped path>'`
   - external `.srt`: append `:force_style='<style>'`
3. 统一替换 auto fallback embedded、explicit embedded、external 三条路径，避免分散拼接。
4. 保留现有行为：
   - external `.srt` 继续按分辨率加 `force_style`
   - external `.ass` / `.ssa` 不加 `force_style`
   - embedded subtitle 不加 `force_style`
5. 不修改 `runSubtitleBurnFFMpeg` 的转码参数，不修改队列/任务状态逻辑。

## Test Plan

- 更新或新增 `pkg/queue/video_queue_test.go` 单测：
  - embedded path with Chinese/brackets/semicolon/comma/space/single quote uses `filename=`.
  - auto fallback embedded path also uses the same builder.
  - external `.srt` path uses `filename=` and keeps `force_style`.
  - external `.ass` / `.ssa` path uses `filename=` and does not add `force_style`.
  - existing `escapeFFMpegSubtitlePath` expectations are adjusted to the named-option format.
- Run:
  - `go test -count=1 ./pkg/queue`
  - if touched service path: `go test -count=1 ./service/video`
  - ideally `go test -count=1 ./...` if runtime is acceptable.

## Deployment / Online Validation

- Do not deploy in this implementation step.
- After commit/push/deploy is explicitly approved, validate by triggering one subtitle burn on a file path similar to the failed samples.
- Expected online result: task no longer fails at ffmpeg filtergraph parsing; if it fails later, collect the new stderr as a separate issue.
