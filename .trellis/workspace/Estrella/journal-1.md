# Journal - Estrella (Part 1)

> AI development session journal
> Started: 2026-04-24

---

## 2026-04-27 remote ffmpeg worker

Created and deployed dedicated Go FFmpeg worker. Private repo `lyyQwQ/cloudreve-ffmpeg-worker`, deployed image `ghcr.io/lyyqwq/cloudreve-ffmpeg-worker:sha-db04264e845568ecc730c843167c002414f90b91` on hk3 at `/root/services/cloudreve-ffmpeg-worker`, bound to `127.0.0.1:3301`. Smoke test passed for embedded subtitle burn with CJK fonts.



## Session 1: Remote FFmpeg Worker production validation

**Date**: 2026-04-29
**Task**: Remote FFmpeg Worker production validation
**Package**: assets
**Branch**: `rewrite/backend-by-module-fixed`

### Summary

Completed remote FFmpeg Worker first production version, documented source-URL worker contract, resumable output download, frontend worker progress/ETA, production deployment evidence, secret review, and follow-up tasks for font rendering, workflow timeouts, and burned-video playback.

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `a0811b3` | (see git log) |
| `698aafb` | (see git log) |
| `3ab90c1` | (see git log) |
| `5df459e` | (see git log) |
| `0d8fa64` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 2: remote worker subtitle font

**Date**: 2026-04-29
**Task**: remote worker subtitle font
**Package**: assets
**Branch**: `rewrite/backend-by-module-fixed`

### Summary

Forced remote worker embedded subtitle burns to use Noto Sans CJK SC, verified GitHub Actions Docker build, deployed hk3 worker image, and confirmed production container selects NotoSansCJKsc-Regular.

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `935a7f1bbb72aa816b53de1c7dcb706839709713` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 3: Batch subtitle and HLS processing

**Date**: 2026-05-10
**Task**: Batch subtitle and HLS processing
**Branch**: `rewrite/backend-by-module-fixed`

### Summary

Added backend batch video endpoints, frontend batch dialogs, focused tests, and local verification for subtitle burn and HLS batch processing.

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `78aa897` | (see git log) |
| `b308f50` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 4: Remote worker external subtitles

**Date**: 2026-05-10
**Task**: Remote worker external subtitles
**Branch**: `rewrite/backend-by-module-fixed`

### Summary

Added remote worker support for external subtitle burn with signed subtitle URLs, dual-download worker jobs, fixed SRT/ASS/SSA FFmpeg templates, and local tests across backend and worker.

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `d9d23f2` | (see git log) |
| `eee83fe` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete
