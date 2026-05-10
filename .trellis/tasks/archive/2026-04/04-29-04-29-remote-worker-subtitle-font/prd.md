# remote worker subtitle font rendering

## Goal

排查远程 FFmpeg Worker 烧录中文字幕时字形看起来偏日文汉字的问题，确认 worker 容器内字体、fontconfig fallback、ASS 字幕样式和 FFmpeg `subtitles` 滤镜选择字体的真实行为。

## Context

生产远程 worker 已能完成内封字幕烧录并把 MP4 拉回 Cloudreve。Estrella 观察到输出视频里的中文字幕字形观感不像常见中文字体，更接近日文汉字字形。早期 worker smoke test 只验证了缺字警告消失，没有验证中文区域字形是否符合预期。

## Requirements

* 先只读调查，不修改生产 worker 配置。
* 检查 hk3 worker 容器已安装字体、`fc-match` 结果、fontconfig 配置顺序。
* 检查输入文件内封字幕或 ASS 样式中声明的字体名。
* 检查 FFmpeg `subtitles` 滤镜是否可以通过 `fontsdir`、`force_style` 或 fontconfig 配置指定中文字体。
* 用同一段中文字幕生成小样本，对比当前字体和候选中文字体。
* 方案保持简单，优先在 worker 镜像内固定一套中文字体和 fontconfig 配置。

## Acceptance Criteria

* [x] 明确当前输出使用的字体或 fallback 字体。
* [x] 明确为什么会出现日文字形观感。
* [x] 给出最小修复方案和回退方式。
* [x] 修复后有可重复的 smoke test，包含中文样本文字和 `fc-match` 证据。
* [x] 不泄露生产视频签名 URL、API key、SSH key 或私有路径。

## Implementation Summary 2026-04-29

Worker-only fix completed in sibling repository `../cloudreve-ffmpeg-worker`.

Changed behavior:

```text
subtitles=filename='<input>':si=<embedded_index>:force_style='FontName=Noto Sans CJK SC'
```

Reason:

* Production sample converted embedded SRT into ASS style `Default,Arial,16,...`.
* Alpine fontconfig resolved `Arial` to DejaVu first, then selected `NotoSansMonoCJKhk-Regular` as CJK fallback.
* Explicit `Noto Sans CJK SC` selected `NotoSansCJKsc-Regular`, matching Simplified Chinese glyph expectations.

Verification:

```text
go test -count=1 ./...                              # worker
git diff --check                                    # worker
docker build -t cloudreve-ffmpeg-worker:font-test . # worker
docker runtime smoke selected NotoSansCJKsc-Regular
secret grep found no production API key, signed URL, SSH key, or private token
```

## Out of Scope

* 不重构远程 worker API。
* 不引入复杂字体管理服务。
* 不处理非 CJK 字幕样式美化。
