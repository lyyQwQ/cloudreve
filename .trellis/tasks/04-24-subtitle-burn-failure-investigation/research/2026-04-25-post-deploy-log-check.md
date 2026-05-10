# 2026-04-25 Post-deploy log check

## Scope

Observed production Cloudreve container after deploying `ghcr.io/lyyqwq/cloudreve:sha-9f5296a`.

## Remote download cancel observation

- At `2026-04-25 03:00:21 UTC`, `DELETE /api/v4/workflow/download/V6Jpfx` returned HTTP 200.
- No Cloudreve error/panic appeared around the request.
- No qB `torrents/delete` outgoing request was observed around the cancel request.
- The remote-download tasks kept cycling between `processing` and `suspending` every polling interval, with qB `torrents/info`, `torrents/files`, and `torrents/pieceStates` requests returning 200.
- Database rows for recent remote-download tasks remained `suspending`; no recently deleted task rows were found.

Likely code-level cause to verify: `CancelDownloadTask` only calls `RemoteDownloadTask.CancelDownload`. For restored/suspended tasks, `RemoteDownloadTask.CancelDownload` returns nil when the runtime downloader field is nil, causing the API to return success without mutating task status or sending qB delete.

## Subtitle burn observation

- Four `video_subtitle_burn` tasks were submitted after deployment: `12714`, `12715`, `12716`, `12717`.
- `12714` moved to `processing`; the others stayed `queued` because `VideoProcessQueue` has one worker.
- A live `ffmpeg` process is running for `12714`.
- The live ffmpeg command uses the deployed safer syntax: `subtitles=filename='<path>':si=0`.
- No `Trailing garbage after a filter`, `exit status 234`, or subtitle filter parse failure appeared in recent logs.

Current reading: the subtitle fix is taking effect; the first burn job is still running rather than failing at filter-parse startup.
