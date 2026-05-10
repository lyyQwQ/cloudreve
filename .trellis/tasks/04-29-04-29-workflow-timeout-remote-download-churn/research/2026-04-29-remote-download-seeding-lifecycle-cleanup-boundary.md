# Research: remote_download seeding lifecycle cleanup boundary

- Query: Architecture and implementation boundary for `remote_download` seeding lifecycle cleanup in `.trellis/tasks/04-29-04-29-workflow-timeout-remote-download-churn`.
- Scope: internal
- Date: 2026-04-29

## Findings

### Files found

`pkg/filemanager/workflows/remote_download.go` is the core `remote_download` task implementation. It owns task phases, persisted private state, downloader handle restore, qBittorrent operations through the downloader abstraction, transfer to Cloudreve storage, progress, summary, cancellation, and cleanup.

`pkg/downloader/downloader.go` defines the remote downloader abstraction. Its boundary is `CreateTask`, `Info`, `Cancel`, and `SetFilesToDownload`; Cloudreve task code should keep using this interface rather than importing a qB-specific client.

`pkg/downloader/qbittorrent/qbittorrent.go` implements qBittorrent as one downloader backend. It tags created torrents with `cr-<task-id>`, reads qB state through Web API, and deletes torrents with `deleteFiles=true` when `Cancel` is called.

`pkg/queue/task.go`, `pkg/queue/queue.go`, `pkg/queue/scheduler.go`, and `pkg/queue/registry.go` implement persistence, state transitions, scheduling, and live task registry behavior. These files explain why a suspended task currently writes `suspending -> processing -> suspending` every poll.

`service/explorer/workflows.go` is the service layer for creating, listing, canceling, and updating workflow tasks. It is the right place for user-facing remote download lifecycle orchestration because it already owns `CancelDownloadTask` and has access to `dependency.Dep`, task registry, task client, current user, and hasher.

`service/explorer/file.go` and `pkg/filemanager/manager/operation.go` are the delete entrypoints from the file manager API into the file manager. `pkg/filemanager/fs/dbfs/manage.go` performs soft delete and hard delete and emits file delete events for browser subscribers.

`pkg/filemanager/eventhub/*` is a browser-oriented event hub for file UI events. It currently lacks a server-side subscriber contract suitable for lifecycle cleanup, so using it for qB cleanup would add indirection without much benefit.

`.trellis/spec/backend/queue-task-lifecycle.md` is the relevant spec. It requires restored remote-download tasks to preserve `PrivateState.Handle`, recreate runtime clients before remote mutations, and persist visible terminal status after successful cancellation.

### Code patterns

`RemoteDownloadTaskState` persists `Dst`, `Handle`, `Status`, phase, slave upload state, and transferred indexes in `PrivateState`; this gives enough information to conservatively match transferred Cloudreve output paths without adding a new table for the first version. See `pkg/filemanager/workflows/remote_download.go:45`.

`RemoteDownloadTask.Do` routes both `monitor` and `seeding` phases into the same `monitor` function. This is the root implementation point for seeding poll throttling because `RemoteDownloadTaskPhaseAwaitSeeding` currently uses the active-download monitor path. See `pkg/filemanager/workflows/remote_download.go:153`.

`monitor` derives the next poll interval from `m.node.Settings(ctx).Interval` for all states. In seeding phase this reuses an active-download setting for a passive wait state. See `pkg/filemanager/workflows/remote_download.go:283`.

`monitor` treats qB seeding as `transfer` before Cloudreve transfer, and as a repeated suspended wait after transfer when `WaitForSeeding` is enabled. Repeated waits call `ResumeAfter(resumeAfter)` and return `suspending`. See `pkg/filemanager/workflows/remote_download.go:326`.

`masterTransfer` writes selected files into Cloudreve storage, records transferred indexes in `state.Transferred`, sets phase to `RemoteDownloadTaskPhaseAwaitSeeding`, and returns `suspending`. See `pkg/filemanager/workflows/remote_download.go:467`.

`slaveTransfer` records slave upload state and sets phase to `RemoteDownloadTaskPhaseAwaitSeeding` after the slave upload completes. Successful slave uploads may need matching from `SlaveUploadState.Files` rather than only `state.Transferred`. See `pkg/filemanager/workflows/remote_download.go:431`.

`Cleanup` currently cancels the remote downloader only when a runtime downloader is already present. Restored tasks without initialized runtime need the newer `CancelDownload` path, which calls `ensureDownloaderForState`. See `pkg/filemanager/workflows/remote_download.go:634` and `pkg/filemanager/workflows/remote_download.go:684`.

`ensureDownloaderForState` is the correct restored-runtime boundary. It rebuilds node/downloader runtime from persisted `NodeState` and handle state before remote mutation. See `pkg/filemanager/workflows/remote_download.go:196`.

`qbittorrent.Cancel` deletes the qB torrent and downloaded files with `deleteFiles=true`, then deletes the Cloudreve tag. This confirms qB is already treated as an execution backend owned by Cloudreve when cancellation or cleanup runs. See `pkg/downloader/qbittorrent/qbittorrent.go:104`.

`qbittorrent.Info` maps qB upload states such as `queuedUP` and `stalledUP` to Cloudreve `StatusSeeding`, while `pausedUP` and `stoppedUP` map to `StatusCompleted`. See `pkg/downloader/qbittorrent/qbittorrent.go:190`.

The queue persists every transition into `processing` and every return to `suspending`, and logs each transition. This explains the observed churn: one passive seeding poll causes at least two task-row updates and status-change logs. See `pkg/queue/task.go:461`, `pkg/queue/task.go:471`, and `pkg/queue/queue.go:363`.

`RemoteDownloadQueue` resumes persisted `remote_download` tasks and polls the scheduler every 10 seconds. This queue-level setting is not the main problem once individual tasks set a far future `ResumeTime`. See `application/dependency/dependency.go:726`.

`CancelDownloadTask` already persists `canceled` and removes the task from the registry after `RemoteDownloadTask.CancelDownload` succeeds. This is the existing service-level pattern for terminal remote-download cleanup. See `service/explorer/workflows.go:398`.

`DeleteFileService.Delete` currently calls only `m.Delete` and returns. There is no remote-download cleanup hook after user-visible file deletion. See `service/explorer/file.go:539`.

`manager.Delete` soft-deletes by default and hard-deletes only when skip-soft-delete options are set. Cleanup should run after both cases because the output disappears from normal Cloudreve file view in both cases. See `pkg/filemanager/manager/operation.go:160`.

`DBFS.SoftDelete` and `DBFS.Delete` emit browser file delete events after DB changes. These events are not presently consumed by backend lifecycle logic. See `pkg/filemanager/fs/dbfs/manage.go:250` and `pkg/filemanager/fs/dbfs/manage.go:335`.

### Architecture answer: Cloudreve should own qB torrent lifecycle

Cloudreve should manage qB torrent lifecycle for torrents created by Cloudreve remote downloads. The code already supports this direction: qB torrents are created with a Cloudreve-generated tag, the handle is persisted in `PrivateState`, and `Cancel` deletes the torrent and files. qBittorrent is therefore an execution backend, not an independent long-term seeding manager for Cloudreve-owned torrents.

The boundary should remain narrow:

1. `pkg/downloader/*` owns backend-specific remote calls.
2. `pkg/filemanager/workflows/remote_download.go` owns remote download task state, qB cleanup calls through `downloader.Downloader`, and matching transferred outputs from task state.
3. `service/explorer/workflows.go` owns user-facing lifecycle orchestration and DB status mutation.
4. `service/explorer/file.go` may notify the lifecycle service after successful deletion, but should not know task registry internals, qB handles, queue transitions, or task private-state details.

This keeps the implementation understandable for a single maintainer. It avoids a new daemon, a new table, a generic backend event bus, or qB-specific logic in the file deletion path.

### Minimal design: seeding poll throttling

Add a small helper in `pkg/filemanager/workflows/remote_download.go`:

```go
const remoteDownloadAwaitSeedingMinPollInterval = 5 * time.Minute

func (m *RemoteDownloadTask) monitorPollInterval(ctx context.Context) time.Duration {
    interval := time.Duration(m.node.Settings(ctx).Interval) * time.Second
    if m.state != nil && m.state.Phase == RemoteDownloadTaskPhaseAwaitSeeding && interval < remoteDownloadAwaitSeedingMinPollInterval {
        return remoteDownloadAwaitSeedingMinPollInterval
    }
    return interval
}
```

Then replace the current `resumeAfter := time.Duration(m.node.Settings(ctx).Interval) * time.Second` in `monitor` with the helper.

Active downloading keeps the node interval. Passive post-transfer seeding is floored to 5 minutes. For 23 seeding tasks this reduces a 5-minute window from hundreds of processing/suspending transitions to roughly one cycle per task, while preserving cancellation because suspended tasks remain in the registry and keep their handle in `PrivateState`.

Repeated `Download task seeding` info logs should also move to debug level when `state.Phase == RemoteDownloadTaskPhaseAwaitSeeding`, or be emitted only when the phase first changes into seeding wait. This reduces log volume without changing behavior.

### Minimal design: detecting transferred outputs missing from Cloudreve

Add state-only matching helpers on `RemoteDownloadTask`:

```go
func (m *RemoteDownloadTask) TransferredOutputURIs() ([]*fs.URI, error)
func (m *RemoteDownloadTask) AffectedByDeletedURIs(deleted []*fs.URI) (bool, error)
func (m *RemoteDownloadTask) TransferredOutputsExist(ctx context.Context, dep dependency.Dep) (bool, error)
```

The helpers should only operate when `Phase == RemoteDownloadTaskPhaseAwaitSeeding` and `Status != nil`. Output URI construction should follow existing transfer logic:

1. Start from `state.Dst`.
2. For master transfer, include selected `state.Status.Files` whose index is in `state.Transferred`; if all selected files have been transferred and `Transferred` is absent in older state, use selected files conservatively only after phase is `seeding`.
3. For slave transfer, prefer `state.SlaveUploadState.Files[*].Uri` because successful slave path may not copy all indexes into `state.Transferred`.
4. Sanitize names with the same `sanitizeFileName` used by `masterTransfer` and `slaveTransfer`.
5. Match deletion if a deleted URI equals one output URI, or if a deleted URI is an ancestor of one output URI. This covers deleting a downloaded file and deleting a containing directory.

`TransferredOutputsExist` can use `manager.NewFileManager(dep, owner).Get(ctx, outputURI)` for each output. If any output is missing, the task is no longer a valid active seeding owner because Cloudreve no longer has the transferred result associated with the qB torrent.

This avoids matching by file name alone and avoids touching unrelated qB torrents in the same qB instance.

### Minimal design: cleaning qB torrent safely

Use the existing downloader abstraction and restored-runtime path:

1. Restore or use the live `RemoteDownloadTask`.
2. Confirm phase is `RemoteDownloadTaskPhaseAwaitSeeding`.
3. Confirm the deleted URI matches one of the transferred output URIs, or confirm `TransferredOutputsExist` returns false during a seeding poll.
4. Call `RemoteDownloadTask.CancelDownload(ctx)` to delete the qB torrent and qB-side files.
5. Treat qB `ErrTaskNotFount` during post-transfer seeding as already clean, not as user cancellation.
6. Only update the Cloudreve task to terminal status after the qB cleanup call succeeds or the qB torrent is already missing.
7. If qB is unreachable or returns an unexpected error, leave the task in `suspending` with the throttled seeding poll. This keeps a visible owner and allows retry instead of leaving qB unmanaged.

`Cleanup` should either reuse `CancelDownload` for restored tasks or a narrower internal helper such as `cancelRemoteDownload(ctx, allowMissing bool)`. The important boundary is that restored tasks must not rely on `m.d != nil` for qB cleanup.

### Minimal design: task final status

Use `completed` for post-transfer cleanup caused by Cloudreve output deletion or qB already-missing state during `RemoteDownloadTaskPhaseAwaitSeeding`.

Rationale:

1. Cloudreve transfer already completed before the task entered seeding phase.
2. The user-visible file deletion is a later Cloudreve file operation, not a failed download.
3. `canceled` should remain reserved for explicit user cancellation before or during the download lifecycle.
4. `error` should represent failed download, failed transfer, invalid state, or repeated cleanup failure after retry policy is exhausted.

For current `monitor` behavior, refine `ErrTaskNotFount` handling:

1. If phase is `monitor` before transfer and a previous qB status existed, return `canceled` as today.
2. If phase is `seeding` after transfer, return `completed` because there is no active qB torrent left to manage.

For delete-triggered cleanup, update the task row to `completed`, remove it from the registry, and avoid invoking generic queue internals from `service/explorer/file.go`.

### Minimal design: avoiding deletion flow coupling to queue internals

Add a small service helper in `service/explorer/workflows.go` or a sibling file such as `service/explorer/remote_download_lifecycle.go`:

```go
func CleanupRemoteDownloadOutputsAfterDelete(ctx *gin.Context, deleted []*fs.URI) error
```

This helper owns all remote-download-specific behavior:

1. Query active `remote_download` tasks for the current user with statuses `queued`, `processing`, and `suspending` through `TaskClient.List` or a small inventory helper if pagination limits become awkward.
2. Prefer the live task from `TaskRegistry` when present; otherwise restore with `queue.NewTaskFromModel`.
3. Filter to `*workflows.RemoteDownloadTask` and phase `seeding` through `Summarize` or a typed helper.
4. Match deleted URIs through `AffectedByDeletedURIs`.
5. Call task-level cleanup through `CancelDownload` or a purpose-named cleanup method.
6. Persist `completed` through `TaskClient.Update` and remove the registry entry.
7. Log cleanup failures without printing private source URLs, qB server URLs, credentials, or full local storage paths.

Then change `DeleteFileService.Delete` only at the service boundary:

```go
if err = m.Delete(...); err != nil { return ... }
_ = CleanupRemoteDownloadOutputsAfterDelete(c, uris)
```

The file deletion code stays simple: after a successful delete, it notifies the remote-download lifecycle service. It does not inspect queue state, mutate registry directly, parse `PrivateState`, or call qB APIs.

A stricter alternative is making cleanup failure block deletion. That would be less friendly operationally because qB network issues could prevent normal file deletion. The minimal single-maintainer design should let deletion succeed, keep the task active on cleanup failure, and rely on throttled seeding polls to retry.

### Test strategy

Add focused unit tests in `pkg/filemanager/workflows/remote_download_test.go`:

1. Await-seeding poll uses the floor interval when node interval is shorter.
2. Monitor phase still uses the node interval for active downloading.
3. `AffectedByDeletedURIs` returns true for an exact transferred output URI.
4. `AffectedByDeletedURIs` returns true when deleting an ancestor directory of a transferred output.
5. `AffectedByDeletedURIs` returns false for unrelated files under the same destination.
6. Slave-upload transferred outputs are matched from `SlaveUploadState.Files`.
7. Post-transfer qB missing maps to `completed`, while pre-transfer qB missing keeps the existing `canceled` behavior.

Add service tests in `service/explorer/workflows_test.go` or a new `remote_download_lifecycle_test.go`:

1. A restored seeding task with a matching deleted output calls the fake downloader cancel path, persists `completed`, and is removed from `TaskRegistry`.
2. A restored seeding task with an unrelated deleted URI remains `suspending` and does not call cancel.
3. A qB cleanup failure leaves the task active and does not mark it completed.
4. User ownership is enforced: deleting another user's path cannot clean another user's remote-download task.

Add file service integration coverage only if existing test helpers make it cheap:

1. `DeleteFileService.Delete` calls the lifecycle helper after successful `m.Delete`.
2. Deletion failure skips lifecycle cleanup.

Keep the test set small and focused. The queue already has scheduler tests for due-time ordering in `pkg/queue/scheduler_test.go`; new tests should avoid broad queue integration unless a change touches queue behavior.

### Related specs

`.trellis/spec/backend/queue-task-lifecycle.md` applies because the design changes restored remote-download behavior, downloader cleanup, terminal status persistence, and registry removal.

`.trellis/spec/guides/code-reuse-thinking-guide.md` is relevant because the qB cleanup path should reuse `CancelDownload` and `ensureDownloaderForState` rather than adding a second qB client path.

`.trellis/spec/guides/cross-layer-thinking-guide.md` is relevant because the feature crosses file deletion service, workflow service, queue task state, inventory task rows, and qB downloader backend.

### External references

No external references were used. The request limited the research to local code, and the current design relies only on repository code and Trellis artifacts.

## Caveats / Not Found

The production snapshot in existing research says all 23 qB handles still exist and are seeding. This research did not re-check production and did not access private URLs or credentials.

There is no existing server-side file deletion subscriber for backend lifecycle cleanup. The current event hub is oriented toward browser file events, so using it for qB cleanup would require additional infrastructure.

Current `TaskClient.List` is paginated and user-filtered. If many active remote-download tasks exist, a service helper may need a tiny inventory query helper for active remote-download tasks by user and status. For the observed scale, `page_size=100` style pagination is enough.

The transferred-output match is conservative by design. It should skip cleanup when task state lacks `Status.Files`, `Dst`, or slave upload output URIs, because an unsafe match could delete an unrelated qB torrent.

Soft delete moves files out of normal view while retaining trash metadata. This design treats soft-deleted remote-download outputs as no longer owning the qB seeding task. Restoring from trash later restores the Cloudreve file only; it does not recreate qB seeding.
