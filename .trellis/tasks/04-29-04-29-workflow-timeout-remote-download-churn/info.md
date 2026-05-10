# Implementation Plan: workflow timeout and remote download seeding cleanup

## Scope

This task changes backend remote-download lifecycle behavior only. Frontend changes are out of scope for this implementation round.

The design keeps qBittorrent as a Cloudreve-managed remote-download execution backend. Cloudreve remains the owner of torrent lifecycle for torrents created by Cloudreve remote download tasks.

## Current Evidence

Production currently has active `remote_download` tasks in post-transfer seeding state. qBittorrent still has the corresponding torrents and reports them as complete upload states. Some Cloudreve output files or directories have already been removed from the normal file tree while the task list still shows seeding.

Local code shows the cause:

* `RemoteDownloadTask.Do` sends both monitor and seeding phases through `monitor`.
* `monitor` uses the node active-download interval for seeding waits.
* File deletion currently removes Cloudreve files only and does not notify remote-download lifecycle code.
* qB cleanup already exists through `RemoteDownloadTask.CancelDownload`, which recreates downloader runtime from persisted state and calls the downloader abstraction.

## Implementation Tasks

### 1. Seeding poll throttling

Modify `pkg/filemanager/workflows/remote_download.go` so post-transfer seeding waits use a dedicated minimum polling interval.

Planned behavior:

* Active downloading keeps using node `settings.interval`.
* Transfer phase behavior stays unchanged.
* Post-transfer seeding phase uses `max(node interval, 5 minutes)`.
* Repeated seeding wait logs are reduced from info-level chatter to a lower-volume path.

### 2. Transferred output matching helpers

Add helpers on `RemoteDownloadTask` to derive transferred Cloudreve output URIs from persisted task state.

Planned behavior:

* Only seeding-phase tasks are eligible.
* Master transfer outputs are derived from `state.Dst`, `state.Status.Files`, selected file indexes, `state.Transferred`, and `sanitizeFileName`.
* Slave transfer outputs prefer `state.SlaveUploadState.Files[*].Uri` when available.
* Matching accepts exact output file URI and ancestor directory URI.
* Missing or incomplete state skips cleanup rather than guessing.

### 3. Orphan seeding self-cleanup

During post-transfer seeding checks, verify whether transferred outputs still exist in Cloudreve.

Planned behavior:

* If at least one transferred output still exists, keep the qB torrent and continue low-frequency seeding checks.
* If all transferred outputs are gone, call `CancelDownload` to delete the qB torrent and qB-side files, then return `completed`.
* If qB is already missing after transfer, treat the task as `completed`.
* If cleanup fails because qB is temporarily unreachable or returns an unexpected error, keep the task suspended for later retry.

### 4. Delete-triggered lifecycle cleanup

Add a small service helper under `service/explorer/` for post-delete remote-download cleanup.

Planned behavior:

* `DeleteFileService.Delete` keeps the file deletion responsibility unchanged.
* After successful file deletion, it calls a remote-download lifecycle helper with the deleted URIs.
* The helper queries current user's active `remote_download` tasks, restores live tasks when needed, filters to seeding phase, checks path match through task helpers, then performs safe qB cleanup.
* Cleanup failure is logged and does not roll back or block file deletion.
* The helper persists cleaned tasks as `completed` and removes live task registry entries.

### 5. Task status semantics

Planned behavior:

* Explicit user cancel remains `canceled`.
* Download failure or transfer failure remains `error`.
* Post-transfer cleanup caused by removed Cloudreve output becomes `completed`.
* Post-transfer qB already missing becomes `completed`.
* Pre-transfer qB missing keeps existing cancellation behavior.

## Safety Boundaries

The implementation must not clean qB when:

* task phase is active monitor/download;
* task phase is transfer;
* task is not owned by the current user for delete-triggered cleanup;
* deleted path is unrelated to transferred output URIs;
* only part of a multi-file transfer has been deleted and at least one transferred output remains;
* persisted state lacks enough data to build transferred output URIs safely.

## Test Plan

Add focused backend tests.

`pkg/filemanager/workflows/remote_download_test.go`:

* active monitor phase uses node interval;
* seeding phase applies the minimum poll interval;
* transferred output matching works for exact file paths;
* transferred output matching works for ancestor directory deletion;
* unrelated paths do not match;
* partial remaining outputs keep the qB torrent;
* all missing outputs trigger completed cleanup;
* qB missing before transfer keeps existing behavior;
* qB missing after transfer maps to completed.

`service/explorer/workflows_test.go` or a sibling lifecycle test file:

* delete-triggered cleanup completes a matching restored seeding task;
* delete-triggered cleanup ignores unrelated tasks;
* qB cleanup failure leaves task active;
* user ownership is enforced.

Run at minimum:

```bash
go test ./pkg/filemanager/workflows ./service/explorer
```

If package dependencies require broader validation, run the relevant backend test subset before check.

## Validation Plan

After implementation and check:

1. Run Trellis task validation.
2. Run targeted Go tests.
3. Review `git diff` for secret exposure before any Git write operation.
4. For production deployment later, inspect active `remote_download` tasks before and after startup or seeding polls, confirming orphan seeding tasks are completed and qB torrents are removed only for tasks whose transferred outputs are gone.

## Context Files

Implementation and check agents should read:

* `.trellis/spec/backend/queue-task-lifecycle.md`
* `.trellis/spec/backend/media-processing.md` only if shared queue conventions are needed; no media-processing code is expected.
* `.trellis/spec/guides/cross-layer-thinking-guide.md`
* `.trellis/tasks/04-29-04-29-workflow-timeout-remote-download-churn/research/2026-04-29-production-remote-download-churn.md`
* `.trellis/tasks/04-29-04-29-workflow-timeout-remote-download-churn/research/2026-04-29-remote-download-seeding-lifecycle-cleanup-boundary.md`
