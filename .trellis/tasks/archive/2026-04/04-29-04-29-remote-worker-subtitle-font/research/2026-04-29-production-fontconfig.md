# Production Fontconfig Investigation 2026-04-29

## Scope

Read-only production inspection for remote worker subtitle burn font rendering. No worker configuration or image files were changed.

## Environment

* Worker container: `cloudreve-ffmpeg-worker`
* Worker image: `ghcr.io/lyyqwq/cloudreve-ffmpeg-worker:sha-0d8fa6438f9ca41f56b30d6d8313e70a4585d14d`
* Base OS: Alpine Linux 3.22
* Installed font packages: `font-dejavu`, `font-noto-cjk`, `fontconfig`
* Worker Dockerfile installs `ffmpeg ca-certificates fontconfig ttf-dejavu font-noto-cjk` and runs `fc-cache -f`.

## Production Sample

Production worker job inspected:

```text
job_id=18aab7071263d440-c0c518529f6e11247c4515e213152275
status=completed
input_size=1651871504
output_size=1508298774
stderr.log size=0
```

Input streams:

```text
0 h264 video
1 aac audio language=chi title=Mandarin
2 subrip subtitle language=chi title=简体中文
3 subrip subtitle language=chi title=繁體中文
4 subrip subtitle language=chi title=Traditional
```

Cloudreve task used:

```text
subtitle.mode=embedded
subtitle.embedded_index=0
```

FFmpeg `subtitles` converted the SRT subtitle stream into generated ASS style:

```text
Style: Default,Arial,16,...
```

## Evidence

Current filter without explicit font override selected `Arial`, then fell back to DejaVu, then to a Noto CJK Hong Kong mono face for missing Chinese glyphs:

```text
fontselect: (Arial, 400, 0) -> /usr/share/fonts/dejavu/DejaVuSans.ttf, 0, DejaVuSans
Glyph 0x300A not found, selecting one more font for (Arial, 400, 0)
fontselect: (Arial, 400, 0) -> /usr/share/fonts/noto/NotoSansCJK-Regular.ttc, 9, NotoSansMonoCJKhk-Regular
```

Explicit `force_style=FontName=Noto Sans CJK SC` selected the Simplified Chinese face:

```text
fontselect: (Noto Sans CJK SC, 400, 0) -> /usr/share/fonts/noto/NotoSansCJK-Regular.ttc, 2, NotoSansCJKsc-Regular
```

## Interpretation

The observed Japanese/HK-like glyph shape is not caused by missing CJK fonts. The worker has Noto CJK installed. The issue is that embedded SRT subtitles become ASS style `Default,Arial,...`; fontconfig resolves `Arial` to DejaVu Sans for Latin glyphs and then falls back to a non-SC Noto CJK face for Chinese glyphs.

The simplest likely fix is to force the embedded subtitle filter to use `Noto Sans CJK SC` when burning Chinese subtitles, for example by appending a tested `force_style=FontName=Noto Sans CJK SC` option to the `subtitles` filter. A worker-level fontconfig alias for `Arial` to `Noto Sans CJK SC` is another option, but it changes global font fallback behavior in the container.

## Next Checks

* Confirm FFmpeg filter escaping for `force_style` with named `filename=` and `si=` in the worker builder.
* Add a worker unit test for embedded subtitle filter including `force_style` when configured.
* Add a smoke test that asserts `fontselect` contains `NotoSansCJKsc-Regular` for a Chinese sample.
* Decide whether font override should be always-on for embedded SRT, configurable, or inferred from subtitle stream language/title.

## Worker Implementation 2026-04-29

Implemented the minimal worker-side fix in `/Users/estrella/ws/cloudreve/cloudreve-ffmpeg-worker`.

* Embedded subtitle filters now append `force_style='FontName=Noto Sans CJK SC'` after the named `filename=` option and `si=` option.
* Existing path escaping remains in the filename option. The regression test covers a Chinese path with spaces, brackets, comma, semicolon, single quote, colon, `si`, and the full `force_style` suffix.
* Added a smoke test that builds a short embedded Chinese subtitle sample and runs FFmpeg with verbose logging when local FFmpeg has the `subtitles` filter. If `fc-match Noto Sans CJK SC` reports `NotoSansCJKsc-Regular`, the test also requires FFmpeg verbose output to include `NotoSansCJKsc-Regular`; otherwise it still verifies the filter can be parsed.
* Updated worker README to document the default Simplified Chinese font override.
* Local checks run in the worker repository: `gofmt -w ./cmd ./internal`, `go test -count=1 ./...`, `go test -count=1 -run TestEmbeddedSubtitleFilterNotoSansCJKSCFontSmoke -v ./internal/ffmpeg`, and `git diff --check`.

Local smoke detail: the current local FFmpeg binary does not expose the `subtitles` filter, so the dedicated smoke test skipped on this host. The full worker test suite still passed.

## Implementation Check 2026-04-29

Worker implementation changed `BuildEmbeddedSubtitleFilter` to append:

```text
:force_style='FontName=Noto Sans CJK SC'
```

Local worker checks:

```text
go test -count=1 ./...
git diff --check
```

Container-equivalent smoke check built the worker Docker image locally and ran FFmpeg inside the Alpine runtime image. The generated Chinese subtitle sample selected the expected Simplified Chinese Noto CJK face:

```text
fontselect: (Noto Sans CJK SC, 400, 0) -> /usr/share/fonts/noto/NotoSansCJK-Regular.ttc, 2, NotoSansCJKsc-Regular
```

No production API key, signed URL, SSH key, or private token was used in the smoke test.

## CI And Production Deployment 2026-04-29

Worker commit:

```text
935a7f1bbb72aa816b53de1c7dcb706839709713
```

GitHub Actions Docker workflow completed successfully:

```text
run 25094988986
repo lyyQwQ/cloudreve-ffmpeg-worker
branch main
```

hk3 deployment updated the worker image to:

```text
ghcr.io/lyyqwq/cloudreve-ffmpeg-worker:sha-935a7f1bbb72aa816b53de1c7dcb706839709713
```

The previous compose file was preserved before changing the image:

```text
/root/services/cloudreve-ffmpeg-worker/.bak/2026-04-29/docker-compose.yml.before-935a7f1
```

Production container checks:

```text
container state: running
healthz: {"status":"ok"}
fc-match "Noto Sans CJK SC": NotoSansCJK-Regular.ttc: "Noto Sans CJK SC" "Regular"
ffmpeg filters: subtitles filter available
```

Production container smoke generated a short Chinese subtitle sample inside the worker container and selected the expected Simplified Chinese face:

```text
fontselect: (Noto Sans CJK SC, 400, 0) -> /usr/share/fonts/noto/NotoSansCJK-Regular.ttc, 2, NotoSansCJKsc-Regular
```

No production API key, signed URL, SSH key, private token, or production media URL was printed or recorded.
