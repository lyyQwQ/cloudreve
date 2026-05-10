# Production Remote Download Churn Investigation 2026-04-29

## Scope

Read-only investigation for production background task page timeouts and old remote-download tasks repeatedly switching between `suspending` and `processing`.

No production task rows, qBittorrent torrents, Cloudreve settings, or files were modified during this investigation.

## Production Snapshot

Active task counts from production DB:

```text
remote_download canceled=2 completed=24 error=10 suspending=23
video_subtitle_burn processing=1 queued=16 completed=32 error=5 canceled=2
```

The 23 active `remote_download` tasks all have:

```text
Cloudreve task status: suspending
remote-download phase: seeding
downloader state: seeding
handle present: yes
```

qBittorrent check for the 23 persisted handles:

```text
matched handles: 23
missing handles: 0
qB states: queuedUP=21, stalledUP=2
progress: 100% for sampled tasks
```

This means the old Cloudreve tasks still correspond to real qBittorrent torrents. qBittorrent considers them complete and in upload/seeding states, not failed or missing.

## Churn Evidence

Cloudreve logs over a recent 5-minute window contained:

```text
RemoteDownloadQueue / seeding / status-change related lines: 4949
processing -> suspending transitions: 684
suspending -> processing transitions: 684
Download task seeding lines: 707
```

Workflow endpoint logs in the same production session were currently fast:

```text
GET /api/v4/workflow ... 200, mostly 5ms-25ms
GET /api/v4/workflow/progress/... 200, mostly 1ms-7ms
```

So the previously observed browser 502/timeout is not reproduced at the exact time of this snapshot. The remote-download churn is still real and continuously writes logs and task rows.

## Code Path

`RemoteDownloadTask.Do` routes both `monitor` and `seeding` phases into `monitor`:

```text
RemoteDownloadTaskPhaseMonitor, RemoteDownloadTaskPhaseAwaitSeeding -> monitor(ctx, dep)
```

`monitor` uses node interval as the next poll delay:

```text
resumeAfter := time.Duration(m.node.Settings(ctx).Interval) * time.Second
```

For qB seeding state:

```text
if phase == monitor:
    phase = transfer
    return suspending
else if !WaitForSeeding:
    return completed
else:
    ResumeAfter(resumeAfter)
    return suspending
```

The production node has a very short polling interval and `wait_for_seeding` enabled. Sensitive node credentials were not recorded in this research note.

Queue behavior persists every iteration transition:

```text
suspending -> processing -> suspending
```

Each cycle updates task row state and emits log lines, even when the task is merely waiting for qB seeding and no user-visible state changed.

## Interpretation

Root cause for churn: 23 old remote-download tasks are already transferred and in Cloudreve `seeding` phase. qBittorrent still keeps the torrents in upload states (`queuedUP` / `stalledUP`). Because production has `wait_for_seeding=true`, Cloudreve keeps these tasks alive and polls them repeatedly. The short node interval makes this produce hundreds of DB writes and log lines every few minutes.

The old tasks should not be treated as qB missing or failed. qB confirms all handles exist and are complete.

The workflow endpoint timeout is likely pressure-related or transient at the application/proxy/browser boundary. Current logs show workflow API itself is fast while churn continues, so the fix should target churn first and then re-check whether workflow timeouts disappear.

## Candidate Fix

Minimal backend change:

1. Add a dedicated seeding wait poll floor for `RemoteDownloadTaskPhaseAwaitSeeding`, instead of using the short active-download node interval.
2. Keep normal monitor/download polling unchanged, so active downloads still update promptly.
3. Lower repeated `Download task seeding` info logs after transfer, or log only on phase transition. Continuous seeding wait should not emit an info line every poll.
4. Preserve cancellation: seeding tasks remain in `suspending`, keep their persisted handle, and `CancelDownload` can still recreate downloader runtime from `PrivateState`.
5. Treat qBittorrent as a Cloudreve-managed execution backend, not a standalone seeding manager. Do not complete a Cloudreve task while leaving the qB torrent unmanaged.
6. When a Cloudreve file or directory produced by a remote-download task is deleted, clean the related active seeding task and qB torrent if the task can be matched safely from `Dst` and transferred file names.
7. Add tests covering await-seeding poll delay, status behavior, and delete-triggered cleanup for transferred remote-download outputs.

This keeps the existing qB seeding semantics while reducing task-row updates and log volume by roughly two orders of magnitude for old seeding tasks.

## Open Question

The product decision is now clarified: Cloudreve owns the remote-download lifecycle. qBittorrent is an execution backend attached to Cloudreve, so fixes must avoid leaving qB torrents running without a Cloudreve task owner.

Remaining design detail: matching a deleted Cloudreve path to an active seeding task should stay conservative. It should only clean qB when the deleted target matches the task destination and the transferred output names, so unrelated qB torrents are never affected.
