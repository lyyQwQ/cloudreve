# Research: ffmpeg-rest hk3 smoke test

Date: 2026-04-27

## Question

Evaluate whether `ffmpeg-rest` can be used as a standalone remote worker on hk3 for Cloudreve subtitle-burn tasks, using an existing Cloudreve video sample and the FFmpeg parameters currently defined in Cloudreve.

## Local/VPS Access Findings

- Local SSH config has an `hk3` host alias, and non-interactive SSH to hk3 succeeds.
- hk3 has Docker, FFmpeg, and FFprobe available on host.
- hk3 host profile observed during test: Ubuntu kernel `6.8.0-88-generic`, about 7.7 GiB memory, about 110 GiB free root disk.
- Cloudreve production host is reachable through the existing `cloudreve-vps` SSH alias.
- Cloudreve container has FFmpeg and FFprobe installed. The host itself did not have `ffprobe` in PATH.

## Sample Video

A Cloudreve-managed video was selected from `/data/cloudreve/data/uploads/...` and a 12-second sample clip was created inside the Cloudreve container, then copied out to host temp storage:

- Source duration: about 1430.08 seconds.
- Source size: about 257 MiB.
- Test clip path on Cloudreve host: `/tmp/cloudreve-ffmpeg-rest-test/input.mp4`.
- Test subtitle path on Cloudreve host: `/tmp/cloudreve-ffmpeg-rest-test/subtitle.srt`.
- Test clip duration: about 12.23 seconds.
- Test clip size: about 5.4 MiB.

The test clip and subtitle were transferred to hk3 at `/tmp/cloudreve-ffmpeg-rest-test/`.

## Raw FFmpeg Test on hk3

The exact Cloudreve-style subtitle burn command shape was tested through Python `subprocess` to avoid shell quoting problems:

- `-vf subtitles=filename='<subtitle.srt>':force_style='FontSize=22,MarginV=28,Outline=0.3,Shadow=1'`
- `-c:v libx264`
- `-crf 18`
- `-preset medium`
- `-maxrate <source bitrate>`
- `-bufsize <source bitrate * 2>`
- `-c:a copy`
- `-movflags +faststart`
- `-nostats`
- `-progress pipe:1`

Result:

- Exit code: 0.
- Elapsed time: about 7.21 seconds for the 12-second sample.
- Progress output included `out_time_us` and `progress=end`, compatible with Cloudreve's existing progress parser.
- Output file: `/tmp/cloudreve-ffmpeg-rest-test/burned-output.mp4`.
- Output size: about 5.2 MiB.
- Output duration: about 12.22 seconds.

Conclusion: hk3 itself can run Cloudreve's current subtitle-burn command shape successfully.

## ffmpeg-rest Deployment Smoke Test

Image tested:

- `ghcr.io/crisog/ffmpeg-rest-standalone:1.2.0`

Runtime notes:

- The image still needs Redis reachable by `REDIS_URL`; running it alone produced Redis connection errors.
- A temporary Docker network, Redis container, and ffmpeg-rest container were started on hk3.
- The API was bound only to `127.0.0.1:3300` during the test.
- Containers were stopped after testing and left in stopped state for inspection.

Observed OpenAPI paths:

- `/video/mp4`
- `/video/mp4/url`
- `/video/audio`
- `/video/audio/url`
- `/video/frames`
- `/video/frames/url`
- `/video/gif`
- `/video/gif/url`
- `/media/info`
- audio/image utility endpoints

Smoke test request:

- `POST /video/mp4` with the 5.4 MiB sample clip.
- Response: HTTP 200.
- Output: MP4 about 5.3 MiB, duration about 12.22 seconds.

## Code Inspection Findings

Repository inspected locally from `https://github.com/crisog/ffmpeg-rest`.

Relevant behavior in the current repository version:

- `POST /video/mp4` accepts only a multipart `file` field in its schema.
- The server controller hardcodes `crf: 23`, `preset: medium`, and `smartCopy: true` for `/video/mp4`.
- Query parameters such as `crf=18`, `preset=medium`, or `smartCopy=false` are not wired into `/video/mp4` in the inspected controller.
- The worker `processVideoToMp4` supports only a standard MP4 conversion/copy path. It does not accept arbitrary `-vf`, subtitle file, `force_style`, `-maxrate`, `-bufsize`, `-c:a copy`, or `-progress pipe:1` customization from API input.
- There is no observed subtitle-burn endpoint.

## Assessment

`ffmpeg-rest` can run as a standalone remote conversion API on hk3 and can process simple MP4 conversions. It cannot currently execute Cloudreve's subtitle-burn command through the public API without modifying or extending the project.

For the current Cloudreve requirement, `ffmpeg-rest` is useful as a reference implementation for:

- API + worker split.
- Redis/BullMQ job execution.
- Multipart upload and binary response behavior.
- Optional S3 output mode.

It is not a drop-in worker for Cloudreve subtitle burning because Cloudreve needs to send a selected subtitle file, exact filtergraph, bitrate constraints, audio copy policy, FFmpeg progress stream, and cancellation behavior.

## Recommended Next Direction

Two practical choices remain:

1. Extend/fork `ffmpeg-rest` with a new subtitle-burn endpoint that accepts video + subtitle + safe style options, emits job progress, and returns the output MP4.
2. Write a very small Cloudreve-specific worker API with only the endpoints needed by Cloudreve: submit subtitle burn, query status/progress, cancel, and download result.

Given single-person operations, a Cloudreve-specific minimal worker may be easier to reason about than adapting the broader `ffmpeg-rest` codebase, unless reusing its Redis/BullMQ architecture is considered valuable.
