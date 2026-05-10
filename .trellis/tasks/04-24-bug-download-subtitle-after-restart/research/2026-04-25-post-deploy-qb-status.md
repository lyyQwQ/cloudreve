# 2026-04-25 Post-deploy qB status check

## Scope

Read-only production diagnosis after deploying `ghcr.io/lyyqwq/cloudreve:sha-9f5296a`.

## Observations

- User clicked cancel for `DELETE /api/v4/workflow/download/V6Jpfx`; the API returned HTTP 200.
- `V6Jpfx` decodes to Cloudreve task `11380`.
- Cloudreve DB task `11380` remains `remote_download / suspending`.
- Cloudreve persisted `private_state.status` for task `11380` still shows an old downloader state: `downloading`, progress around `0.1776`.
- qBittorrent read-only API query by persisted hash `e91751132940a7a90cbc96aadae012e24dc29296` returns one torrent:
  - `state=stoppedUP`
  - `progress=1.000000`
  - `amount_left=0`
  - `completion_on=2026-04-15 04:48:50 +08`
  - tag still exists: `cr-5f145eb7-c59e-460c-92b8-8a5fc279332b`

## Interpretation

qBittorrent completed the download successfully. Cloudreve did not refresh the persisted task state from qB after restart, and the cancel request did not remove the torrent from qB.

The previous nil-guard fix prevents panic/500, but it allows a no-op success path when a restored task has a persisted handle but no live downloader instance.

## Code-level leads

- `service/explorer/workflows.go::CancelDownloadTask` calls `RemoteDownloadTask.CancelDownload` and returns success if it returns nil; it does not independently move the task to `canceled`.
- `pkg/filemanager/workflows/remote_download.go::CancelDownload` returns nil if the runtime downloader is nil, so restored tasks can report cancel success without sending qB `torrents/delete`.
- `pkg/queue/scheduler.go` stores tasks in a custom `taskHeap` but does not use `container/heap`; `Request` inspects `taskQueue[Len()-1]`, so scheduling may depend on insertion order rather than earliest `ResumeTime`. This can leave restored tasks with stale progress even when qB has completed.

## Desired behavior

- Restored remote-download tasks with persisted handle must be able to recreate downloader runtime when user actions need it, especially cancellation.
- Queue scheduler must request the earliest due task by `ResumeTime` so restored tasks get monitor iterations and refresh qB state.
- Cancel success should mean the task is actually canceled in Cloudreve and, when a qB handle exists, qB delete was attempted successfully.
