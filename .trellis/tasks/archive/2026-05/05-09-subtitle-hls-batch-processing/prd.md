# brainstorm: 字幕嵌入和 HLS 批量处理

## Goal

在文件管理器中为字幕嵌入和 HLS 转码增加多选后的批量发起能力，让多个视频文件可以一次进入现有视频处理任务队列，减少重复右键单个文件的操作。

## What I already know

* 当前任务由 Estrella 提出，目标是给字幕嵌入和 HLS 增加批量选择处理功能。
* 当前后端已有单文件接口：`POST /api/v4/video/subtitle/burn` 与 `POST /api/v4/video/hls`。
* `service/video/video.go` 的 `createVideoTask` 目前只接收一个 `file_id`，字幕任务会校验字幕选项，HLS 任务会先检查编码兼容性。
* `pkg/queue/video_queue.go` 中字幕烧录任务和 HLS 切片任务都以单个 `FileID` 持久化任务状态。
* 前端右键菜单当前只在单选视频文件时显示视频处理菜单；多选时整个视频处理菜单隐藏。
* 字幕选择弹窗当前只针对一个文件加载可用字幕，并提交一个字幕烧录任务。
* HLS 管理弹窗当前只针对一个文件查询 HLS 状态、创建任务、删除已有产物。
* 已有测试覆盖单文件视频处理菜单、字幕选择弹窗和 HLS 管理弹窗。
* 相关项目规范包括 `.trellis/spec/backend/media-processing.md`、`.trellis/spec/backend/queue-task-lifecycle.md`、`.trellis/spec/assets/frontend/index.md`。

## Assumptions (temporary)

* 批量能力优先通过复用现有单文件 API 发起多个任务实现，避免新增复杂后端批量接口。
* HLS 批量处理可以对选中的视频逐个发起现有 HLS 任务，并对不兼容、已有任务、已有 HLS 的文件给出汇总提示。
* 字幕嵌入批量处理调整为批量预检模式：自动检测外置字幕和内封字幕，生成候选匹配结果，但创建任务前需要用户确认。
* 单人维护场景下，优先保持前端小改动和后端少改动，除非现有单文件接口无法满足可验证的批量行为。

## Open Questions

* None currently. 批量预检采用字幕候选交集；同一组视频只展示所有文件共同拥有的字幕候选。

## Requirements (evolving)

* 多选视频文件时，文件管理器右键菜单应提供批量视频处理入口。
* HLS 批量处理应尽量复用现有 `createHLSTask`、`getVideoInfo`、`getHLSStatus` 行为。
* 字幕嵌入批量处理先对选中文件执行预检，检测同目录外置字幕和视频内封字幕。
* 字幕嵌入批量处理应尽量复用现有 `createSubtitleBurnTask` 字幕选项结构；外置字幕提交 `external_name`，内封字幕提交 `embedded_index`。
* 每个文件仍然产生独立队列任务，继续沿用现有任务列表、进度展示、取消和恢复机制。
* 批量发起时应有成功、跳过、失败的简要反馈，避免用户只能看到第一个错误。
* 字幕批量预检应展示整组选中文件的字幕候选交集，并列出每个候选覆盖的文件数。
* 只有所有选中文件共同拥有的候选才可作为整组批量任务候选；缺失交集时提示无法批量处理，并展示各文件可用字幕摘要。
* 批量创建任务前需要用户从交集候选中确认一个字幕候选，避免静默选择错误字幕轨道。
* 实施前需要读取前端组件、状态管理、类型安全与质量规范。

## Acceptance Criteria (evolving)

* [x] 多选视频文件时能看到批量字幕嵌入和批量 HLS 入口。
* [x] HLS 批量处理能对多个兼容视频发起任务。
* [x] 字幕嵌入批量处理先展示字幕候选交集，再按确认后的共同候选创建任务，并清晰汇总跳过项。
* [x] 已有单文件字幕嵌入和 HLS 行为保持兼容。
* [x] 前端测试覆盖单选仍显示原行为、多选显示批量入口、批量发起 API 调用与提示。
* [x] 如果后端接口发生变化，Go 测试覆盖请求解析、去重、兼容性检查和部分失败场景。

## Definition of Done

* Tests added or updated for changed frontend/backend behavior.
* Relevant checks pass for touched packages.
* Trellis context manifests include implementation and check specs.
* No production deployment or remote write action occurs without Estrella approval.
* For this task only, frontend/backend Git commits are approved after diff review, secret scan, and successful relevant checks.
* Public-repository safety check completed before staging and committing.

## Out of Scope (explicit)

* 本任务不重新设计视频队列架构。
* 本任务不合并已有远程 FFmpeg worker 方案改动之外的新 worker 协议。
* 本任务不处理线上部署、容器重启或已有生产任务状态。
* 本任务不批量删除 HLS 产物，除非后续明确纳入范围。
* 本任务不支持复杂逐集编辑器；批量预检只提供候选结果、跳过项和一次确认。
* 本任务不支持手动拖拽字幕文件或跨目录选择字幕文件。

## Technical Notes

* Backend entry: `service/video/video.go`
* Queue implementation: `pkg/queue/video_queue.go`
* Frontend menu entry: `assets/src/component/FileManager/ContextMenu/ContextMenu.tsx`
* Frontend visibility rules: `assets/src/component/FileManager/ContextMenu/useActionDisplayOpt.ts`
* Subtitle dialog: `assets/src/component/FileManager/Dialogs/SubtitleSelectDialog.tsx`
* HLS dialog: `assets/src/component/FileManager/Dialogs/HLSManageDialog.tsx`
* Redux dialog state: `assets/src/redux/globalStateSlice.ts`
* API types and thunks: `assets/src/api/video.ts`, `assets/src/api/api.ts`
* Existing tests: `assets/src/component/FileManager/ContextMenu/__tests__/VideoMenuItems.test.tsx`, `assets/src/component/FileManager/Dialogs/__tests__/SubtitleSelectDialog.test.tsx`, `assets/src/component/FileManager/Dialogs/__tests__/HLSManage.test.tsx`



## Research References

* [`research/2026-05-09-production-subtitle-shapes.md`](research/2026-05-09-production-subtitle-shapes.md) — production examples for 瑞克和莫蒂 and 庆余年 have embedded subtitles and no external `.srt` / `.ass` / `.ssa` files next to the sampled videos.

## Production Findings

* 瑞克和莫蒂 sampled seasons and 庆余年 sampled seasons currently have 0 same-directory external subtitle files.
* These examples use embedded subtitle streams. Simplified Chinese appears as the first subtitle stream in the sampled files, with titles such as `简体`, `简体中文`, or `Simplified Chinese`.
* These examples support the need for batch preflight: there is no external subtitle file, but embedded Simplified Chinese candidates can be detected and shown before task creation.
* Embedded subtitle selection should be visible in the preflight result rather than hidden from the user.

## Decision (ADR-lite)

**Context**: 字幕嵌入批量处理主要服务剧集批量转换。初始设想是剧集文件旁边存在同名外置字幕；生产只读检查显示瑞克和莫蒂、庆余年样本均没有同目录外置字幕，主要依赖内嵌字幕流。

**Decision**: MVP 使用批量预检确认模式，并按字幕候选交集处理。系统自动检测同名外置字幕和内封字幕候选，只展示所有选中文件共同拥有的候选；用户确认其中一个共同候选后，再逐个创建现有单文件字幕烧录任务。

**Consequences**: 瑞克和莫蒂、庆余年这类同季内封简体字幕稳定的样本可以作为共同候选处理；如果一组文件字幕候选不一致，则阻止整组批量创建，减少误选字幕轨道。任务创建仍复用现有单文件接口，避免新增复杂后端批量队列。


## Batch Subtitle Matching Approach

1. The user selects multiple video files and opens batch subtitle burn.
2. The frontend calls existing video info loading for each selected file to collect external subtitles and embedded subtitle tracks.
3. For each video, build subtitle candidates:
   * same-directory external subtitle whose base name matches the video base name, allowing an optional language suffix;
   * embedded subtitle tracks keyed by normalized language/title, not by the full per-file track list.
4. Intersect candidates across all selected files.
5. Show a preflight dialog with the shared candidates, candidate type, per-file matched subtitle name or embedded index, and skip reason.
6. Start tasks only after confirmation, using the existing single-file subtitle burn API for each confirmed row.


## Intersection Candidate Rule

字幕候选交集按“候选语义”计算，而不是要求每个文件的完整字幕轨道列表完全一致。

* 外置字幕候选：按视频主文件名匹配同目录字幕，归一为 `external:<language-or-default>`。
* 内封字幕候选：按标题和语言归一，例如 `embedded:simplified_chinese`、`embedded:traditional_chinese`、`embedded:english`。
* 每个候选需要记录每个文件实际使用的 `external_name` 或 `embedded_index`。
* 预检列表只展示所有选中文件都存在的候选。
* 如果交集为空，批量字幕任务不创建，界面展示各文件检测摘要。


## Proposed Interaction Design

Batch subtitle burn uses a single preflight dialog with three states.

### State 1: Scanning

After the user selects multiple video files and chooses batch subtitle burn, the dialog opens immediately and shows scanning progress such as `Checking 3 / 10`. The dialog calls existing video info APIs for each selected file and gathers external subtitles plus embedded subtitle tracks.

The scanning state should support canceling the dialog. Canceling only stops frontend preflight work and does not create any task.

### State 2: Candidate Selection

After scanning, the dialog shows shared subtitle candidates calculated by intersection. Each candidate row shows:

1. Candidate label, for example `Simplified Chinese`, `简体中文`, `EP01.zh.srt pattern`.
2. Candidate type, for example embedded subtitle or external subtitle.
3. Matched file count, expected to equal selected file count for selectable candidates.
4. Warning badge when the same candidate uses different embedded indexes across files.

The dialog also shows a compact expandable details table with one row per file:

1. File name.
2. Matched subtitle name or embedded index.
3. Status: ready, skipped, conflict, or failed to inspect.
4. Reason when skipped.

If the shared candidate intersection is empty, the confirm button stays disabled and the details table explains why.

### State 3: Task Creation

After the user chooses one shared candidate and confirms, the dialog creates existing single-file subtitle burn tasks one by one with limited frontend concurrency. A small concurrency such as 3 keeps implementation simple and avoids bursty API requests.

When all requests finish, show a summary:

1. Created count.
2. Skipped count.
3. Conflict count.
4. Failed count.

The task list action should open `/tasks`; if exactly one task was created, it may include `task_id`, otherwise it opens the general task page.

### HLS Batch Interaction

HLS batch can use the same simpler pattern without subtitle candidate selection:

1. Scan selected videos for HLS compatibility and current HLS status.
2. Show ready, already has HLS, processing, incompatible, and failed-to-inspect counts.
3. Confirm creates HLS tasks for ready files only.
4. Summary uses the same created/skipped/conflict/failed counts.

### UI Scope

This design reuses existing dialog and snackbar patterns. It avoids a complex per-episode editor. Rows can be read-only in the MVP; manual per-file overrides remain out of scope.

## Backend vs Frontend Batch Decision

Decision confirmed: this feature uses backend batch endpoints for the main preflight and creation operations. Frontend-only fan-out is not the MVP implementation direction.

Reasons:

1. `getVideoInfo` may run `ffprobe`; calling it once per selected file from the browser can create many concurrent expensive operations.
2. Backend can apply a bounded worker pool and keep load predictable.
3. Backend can compute subtitle candidate intersection in one place, keeping frontend UI simpler.
4. Backend can perform permission checks, duplicate task checks, existing burned-output checks, HLS compatibility checks, and partial-result reporting consistently.
5. Frontend remains responsible for presentation, user confirmation, progress display, and final summary.

Recommended MVP API shape:

1. `POST /api/v4/video/batch/subtitle/preflight`
   * Input: selected `file_ids`.
   * Output: shared subtitle candidates, per-file matched candidate mapping, skipped rows, inspection failures.
2. `POST /api/v4/video/batch/subtitle/burn`
   * Input: selected `file_ids` plus chosen shared candidate key.
   * Backend recomputes or validates the candidate mapping before creating tasks.
   * Output: created task IDs, skipped rows, conflicts, failures.
3. `POST /api/v4/video/batch/hls/preflight`
   * Input: selected `file_ids`.
   * Output: ready rows, already-has-HLS rows, processing rows, incompatible rows, failed rows.
4. `POST /api/v4/video/batch/hls`
   * Input: selected `file_ids`.
   * Backend creates HLS tasks only for eligible rows.
   * Output: created task IDs, skipped rows, conflicts, failures.

Implementation note: keep these endpoints as thin wrappers around existing single-file helpers and queue constructors. Avoid introducing a new batch task type; each video still becomes an independent queue task.


## Implementation Plan

### Backend

1. Add batch request and response DTOs under the existing video service package.
2. Add subtitle batch preflight endpoint:
   * resolve and authorize each selected file;
   * inspect existing video info for external and embedded subtitles;
   * normalize candidates and calculate intersection;
   * return shared candidates plus per-file mapping and skip reasons.
3. Add subtitle batch creation endpoint:
   * accept selected file IDs and chosen shared candidate key;
   * recompute or validate candidate mapping;
   * reuse existing single-file subtitle task creation logic;
   * return created task IDs, conflicts, skips, and failures.
4. Add HLS batch preflight endpoint:
   * resolve files;
   * reuse existing HLS compatibility and status checks;
   * classify ready, already available, processing, incompatible, and failed rows.
5. Add HLS batch creation endpoint:
   * create existing HLS tasks for ready rows only;
   * return created task IDs, conflicts, skips, and failures.
6. Keep each video as an independent queue task. Do not add a new batch task type.

### Frontend

1. Update video context menu visibility:
   * single video keeps existing video info, subtitle burn, and HLS manage actions;
   * multiple videos show batch subtitle burn and batch HLS actions.
2. Add Redux dialog state for selected batch files.
3. Add `BatchSubtitleBurnDialog`:
   * scanning state;
   * shared candidate selection state;
   * per-file detail table;
   * task creation summary.
4. Add `BatchHLSDialog` with the same preflight and summary pattern, without subtitle candidate selection.
5. Add API types and thunks for the four batch endpoints.
6. Add or update frontend tests for context menu entries, preflight rendering, candidate selection, and summary states.

### Validation

1. Backend focused tests for batch subtitle candidate normalization, intersection, preflight classification, and partial creation results.
2. Backend focused tests for HLS batch preflight and creation classification.
3. Frontend focused tests for menu behavior and dialogs.
4. Run focused Go tests for `service/video` and relevant queue tests if shared helpers are changed.
5. Run focused frontend tests for changed dialog and context menu files.


## Test Plan

### Backend Tests

1. Subtitle candidate normalization:
   * external subtitles with exact stem match;
   * external subtitles with language suffix such as `.zh`, `.chs`, `.sc`;
   * embedded subtitles normalized from `简体中文`, `简体`, `Simplified Chinese`, `Traditional Chinese(Hong Kong)`, `Traditional Chinese(Taiwan)`, `English`;
   * duplicated normalized candidates choose a deterministic candidate and report enough detail for the preflight response.
2. Subtitle candidate intersection:
   * all files share Simplified Chinese at the same embedded index;
   * files share the same normalized candidate at different embedded indexes;
   * one file lacks a candidate, so the candidate is excluded from the shared list;
   * empty intersection returns no creatable candidate and includes per-file detected subtitle summaries.
3. Subtitle batch preflight classification:
   * ready rows with external subtitle mapping;
   * ready rows with embedded subtitle mapping;
   * file not found or permission failure row;
   * non-video or ffprobe failure row;
   * existing burned output and pending task classification when helper behavior is available.
4. Subtitle batch creation:
   * validates or recomputes selected shared candidate before queueing;
   * creates existing single-file subtitle burn tasks for eligible rows;
   * returns created task IDs and partial failures without aborting the whole batch unnecessarily;
   * handles duplicate pending task and burned-output conflict as row-level results.
5. HLS batch preflight:
   * ready H264 video;
   * incompatible video codec;
   * already has HLS artifact;
   * active HLS task in queue;
   * inspection failure row.
6. HLS batch creation:
   * creates existing single-file HLS tasks for eligible rows;
   * skips already available, processing, incompatible, and failed-inspection rows;
   * reports conflicts and queue errors per row.

### Frontend Tests

1. Context menu behavior:
   * single video keeps current video info, subtitle burn, and HLS manage entries;
   * multiple selected videos show batch subtitle burn and batch HLS entries;
   * mixed non-video selection hides or disables batch video entries according to the display rule.
2. Batch subtitle dialog:
   * scanning state renders progress;
   * shared candidate list renders intersection candidates;
   * details table renders per-file matched external name or embedded index;
   * empty intersection disables confirmation and shows detected summaries;
   * confirmation calls batch subtitle creation endpoint with chosen candidate key;
   * result summary renders created, skipped, conflict, and failed counts.
3. Batch HLS dialog:
   * preflight summary renders ready, existing, processing, incompatible, and failed counts;
   * confirmation calls batch HLS creation endpoint;
   * result summary renders created, skipped, conflict, and failed counts.
4. API types and thunks:
   * request and response shapes for the four batch endpoints;
   * error and conflict handling follows nearby API patterns.

### Manual Verification

1. Local development: select a small set of video fixtures and run subtitle batch preflight.
2. Verify candidate intersection display for embedded Simplified Chinese and for same-name external subtitles.
3. Confirm task creation creates independent normal subtitle burn tasks.
4. Run HLS batch preflight and creation on a small compatible sample set.
5. Check task list navigation after one created task and after multiple created tasks.
6. No production deployment or production write operation during local verification unless Estrella separately approves it.

### Commands

Backend focused checks:

```bash
go test -count=1 ./service/video ./pkg/queue
```

Frontend focused checks:

```bash
cd assets && npm test -- --run src/component/FileManager/ContextMenu/__tests__/VideoMenuItems.test.tsx src/component/FileManager/Dialogs/__tests__
```

Build/type check to choose after inspecting current package scripts:

```bash
cd assets && npm run build
```

## Git And Secret Safety Plan

Estrella approved Git commits for this task's frontend and backend changes. The approval covers staging and committing the implementation changes for this task after local review and relevant checks. It does not cover production deployment, remote writes, push, merge, rebase, reset, or unrelated file changes.

Before any staging or commit:

1. Inspect `git status --short` and separate unrelated existing dirty files from this task's changes.
2. Review `git diff` for all task files.
3. Review `git diff --cached` immediately before commit.
4. Search changed and staged content for private keys, tokens, passwords, cookies, signed URLs, real hostnames, IP addresses, private infrastructure notes, and API keys.
5. Keep production research notes under `.trellis/tasks/.../research/` free of secrets and avoid committing private access files such as `.ssh/`, `.trellis/private/`, `.sisyphus/`, local databases, or runtime data.
6. Commit message must mention product behavior only, without real endpoints or private file paths.

Suggested secret scan command before commit:

```bash
changed_files=$(git diff --name-only && git diff --cached --name-only)
printf '%s
' "$changed_files" | sort -u | xargs rg -n -i "BEGIN .*PRIVATE KEY|authorization|bearer|api[_-]?key|token|password|passwd|cookie|sign=|signature=|ssh-rsa|ed25519|root@|([0-9]{1,3}\.){3}[0-9]{1,3}" --
```

The scan output must be reviewed carefully because test fixtures can contain harmless placeholders, while real credentials or private infrastructure details must be removed or moved to ignored local files.

## Implementation Summary

Backend:

1. Added `service/video/batch.go` with four batch endpoints:
   * `POST /api/v4/video/batch/subtitle/preflight`
   * `POST /api/v4/video/batch/subtitle/burn`
   * `POST /api/v4/video/batch/hls/preflight`
   * `POST /api/v4/video/batch/hls`
2. Batch subtitle preflight resolves files, checks pending tasks, reads existing video info, normalizes same-stem external subtitles and embedded subtitle language/title candidates, then returns shared candidates plus per-row details.
3. Batch subtitle creation recomputes preflight, maps the selected shared candidate to each file's actual `external_name` or `embedded_index`, then queues existing single-file subtitle burn tasks.
4. Batch HLS preflight classifies rows as ready, existing, processing, incompatible, skipped, or failed.
5. Batch HLS creation queues existing single-file HLS slice tasks for ready rows only.
6. Router registration and service tests were updated.

Frontend:

1. Multi-selected video files now show batch subtitle burn and batch HLS actions under the video processing context menu.
2. Added Redux dialog state for batch subtitle and batch HLS selected files.
3. Added batch API types and thunks for the four backend endpoints.
4. Added `BatchSubtitleBurnDialog` with preflight loading, shared candidate radio selection, row details, and task creation notification.
5. Added `BatchHLSDialog` with preflight summary, row details, and task creation notification.
6. Added English and Chinese UI strings for the batch actions and summaries.

## Verification Results

Completed local checks:

```bash
go test -count=1 ./service/video ./pkg/queue
cd assets && npm test -- --run src/api/__tests__/video_api.test.ts src/component/FileManager/ContextMenu/__tests__/VideoMenuItems.test.tsx src/component/FileManager/Dialogs/__tests__/BatchVideoDialogs.test.tsx
cd assets && npx eslint src/api/video.ts src/api/api.ts src/api/__tests__/video_api.test.ts src/redux/globalStateSlice.ts src/component/FileManager/ContextMenu/useActionDisplayOpt.ts src/component/FileManager/ContextMenu/ContextMenu.tsx src/component/FileManager/ContextMenu/__tests__/VideoMenuItems.test.tsx src/component/FileManager/Dialogs/Dialogs.tsx src/component/FileManager/Dialogs/BatchSubtitleBurnDialog.tsx src/component/FileManager/Dialogs/BatchHLSDialog.tsx src/component/FileManager/Dialogs/__tests__/BatchVideoDialogs.test.tsx
cd assets && npm run build
```

Additional attempted check:

```bash
cd assets && npm run build-prod
```

`build-prod` stops in `tsc` because the existing frontend tree currently has broad unrelated TypeScript errors across admin, file manager, captcha, task, uploader, and viewer modules. The regular Vite build completed successfully.
