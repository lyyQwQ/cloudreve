# Implementation Notes

## Current Evidence

- Offline download tasks are resumable through `RemoteDownloadQueue`.
- Queue startup restores pending DB tasks with `NewTaskFromModel`, then registers them in the in-memory task registry.
- `RemoteDownloadTask` restored from DB currently only wraps `DBTask`; its runtime fields (`state`, `node`, `d`, `progress`) are initialized later in `Do()`.
- User-facing workflow endpoints can reach a registry task before or between `Do()` iterations:
  - progress calls `TaskPhaseProgress -> t.Progress(c)`
  - cancel calls `CancelDownloadTask -> downloadTask.CancelDownload(c)`
  - selecting files calls `SetDownloadFiles -> t.Summarize(...) -> SetDownloadTarget(...)`
- `RemoteDownloadTask.Progress`, `CancelDownload`, `Cleanup`, and `SetDownloadTarget` currently assume `m.state` and sometimes `m.d/m.node` are initialized.
- This matches the user symptom after container restart: progress no longer updates and cancel returns 500 with suspected invalid memory address.
- qBittorrent/下载器完成不等于 Cloudreve 任务完成。qB 完成后，Cloudreve 仍要通过 `monitor()` 读下载器状态，再推进到 transfer / await-seeding / completed。
- 线上日志曾出现过 remote download 任务进入 seeding 的迹象，说明至少部分任务在下载器侧可能已经完成；修复必须保证恢复后的 Cloudreve 任务能继续用持久化 `handle` 请求下载器状态。
- Concurrency risk: after a restored task is registered, the queue worker goroutine and user request goroutines can access the same task at the same time. Lazy state parsing must be synchronized with the existing task lock or an equivalent mechanism.

## Expected Implementation Direction

- Keep the fix small and local to task lifecycle safety.
- Add a helper or equivalent local pattern so `RemoteDownloadTask` can lazily parse `PrivateState` when called outside `Do()`.
- Guard runtime-only dependencies (`m.d`, `m.node`) when they are unavailable after DB restore.
- Prefer returning a controlled error for operations that require a live downloader but are not initialized yet; progress should return the best available persisted progress or an empty map, not panic.
- Do not drop or overwrite the persisted downloader `Handle`. The next queue iteration must be able to recreate the downloader from the allocated node and call downloader `Info` with the restored handle.
- Add or update tests that prove a task restored from `NewRemoteDownloadTaskFromModel` keeps the serialized handle in parsed state and does not lose the ability to enter the monitor path on `Do()`.
- Do not expose partially initialized `state` without locking. If helper returns state, make clear whether caller owns lock, receives a copy, or only uses it inside the locked section.
- Avoid changing queue architecture, deployment, Docker, upstream sync, or frontend unless tests prove it is necessary.

## qBittorrent / Downloader Verification Plan

- Local code-level verification: construct a restored `RemoteDownloadTask` with a serialized `Handle` and monitor phase, then verify lazy state parsing preserves that handle.
- Behavioral expectation: after restart, user-facing progress/cancel calls may not directly refresh qB status, but they must not corrupt task state or prevent the worker from doing so.
- Worker expectation: the queue worker's next `Do()` call should allocate the node, recreate the downloader client, call downloader `Info(ctx, handle)`, and if qB reports completed/seeding, move the Cloudreve task forward.
- Optional online read-only diagnosis before deployment: query latest `remote_download` tasks for `private_state.handle` and compare with qB task state by read-only qB API/logs. Do not cancel, pause, resume, or mutate qB tasks without explicit approval.

## Reproduction / Test Construction

- Unit test setup: create an `ent.Task` with `Type: queue.RemoteDownloadTaskType`, a non-zero ID/status, and `PrivateState` JSON containing `phase: "monitor"` plus a serialized downloader `handle`.
- Restore with `NewRemoteDownloadTaskFromModel(model)`.
- Call `Progress(context.Background())` before `Do()` and assert it returns safely.
- Call `CancelDownload(context.Background())` before `Do()` and assert it returns safely with a controlled result; no nil pointer panic.
- Call `Summarize(...)` and assert the parsed state still contains the source/destination/phase and the handle was not dropped.
- If testing `SetDownloadTarget` / `Cleanup`, prefer asserting controlled error/no-op when live downloader or node is unavailable, not hidden panic.
- Run focused tests for `pkg/filemanager/workflows` and any touched queue/service package.

## 2026-04-25 Post-deploy findings

- `DELETE /api/v4/workflow/download/V6Jpfx` returned HTTP 200, but no qB `torrents/delete` request was observed.
- `V6Jpfx` maps to task `11380`.
- qB reports task `11380`'s persisted hash as complete: `state=stoppedUP`, `progress=1.0`, `amount_left=0`.
- Cloudreve still stores task `11380` as `suspending` with old `private_state.status.state=downloading` and old partial progress.
- The prior nil-guard changed the failure mode from 500/panic to no-op success; it did not make restored cancel or restored progress refresh fully correct.
- `pkg/queue/scheduler.go` uses a heap-shaped type but never calls `container/heap`; `Request()` checks `taskQueue[Len()-1]`, so a not-yet-due item can block due items depending on insertion order.

## Revised implementation direction

- Fix scheduler ordering so `Request()` returns the earliest due task by `ResumeTime`; keep the change small and covered by focused tests.
- Add a small helper on `RemoteDownloadTask` to ensure runtime downloader/node exists when a restored task with persisted handle needs a downloader from a user action such as cancel.
- `CancelDownload` should only return nil after either there is nothing to cancel or downloader cancellation has been attempted successfully.
- `CancelDownloadTask` should ensure Cloudreve task status changes to canceled when cancellation succeeds, so the frontend list does not continue showing `suspending`.
- Avoid frontend changes and avoid deployment changes in this implementation step.
