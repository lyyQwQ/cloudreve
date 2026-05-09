# Production Subtitle Shapes For Batch Processing

Read-only production inspection on 2026-05-09. No files, database rows, containers, task rows, or settings were modified.

## Scope

Checked representative files for two existing media groups requested by Estrella:

* 瑞克和莫蒂
* 庆余年

Inspection used directory listing for video and subtitle extensions, then `ffprobe` subtitle stream metadata for one sample per detected season/group.

## Findings

### 庆余年

* Season 1 directory has 46 video files and 0 external `.srt` / `.ass` / `.ssa` files next to the videos.
* Season 1 sample video has 3 embedded subtitle streams:
  * `subrip`, `chi`, `简体中文`
  * `subrip`, `chi`, `繁體中文`
  * `subrip`, `chi`, `Traditional`
* Season 2 directory has 36 video files and 0 external `.srt` / `.ass` / `.ssa` files next to the videos.
* Season 2 sample video has 2 embedded subtitle streams:
  * `mov_text`, `chi`
  * `mov_text`, `zht`

### 瑞克和莫蒂

* Checked seasons 1, 2, 3, 5, and 7 under the detected media tree.
* Each checked season directory has 0 external `.srt` / `.ass` / `.ssa` files next to the videos.
* Season 1 sample has 3 embedded subtitle streams:
  * `subrip`, `chi`, `简体`
  * `subrip`, `chi`, `繁體`
  * `subrip`, `eng`, `SDH`
* Seasons 2, 3, and 5 samples each have 8 embedded subtitle streams. The first subtitle stream is Simplified Chinese, followed by Traditional Chinese variants, English, and other languages.
* Season 7 sample has 27 embedded subtitle streams. The first subtitle stream is Simplified Chinese, followed by Traditional Chinese variants, English, and other languages.

## Implications

The current production examples do not validate a same-name external subtitle batch flow, because the inspected directories contain no external subtitle files. They validate an embedded-subtitle batch flow instead.

For the batch subtitle MVP, the simplest behavior that covers these examples is:

1. Prefer same-name external subtitle when a matching `.srt` / `.ass` / `.ssa` exists.
2. Otherwise, optionally support embedded subtitle auto-selection with a Simplified Chinese preference.

If MVP remains external-only, 瑞克和莫蒂 and 庆余年 would be skipped by design in batch subtitle processing.

## Candidate Embedded Selection Rule

A simple embedded-subtitle rule for the current samples:

1. Prefer stream metadata whose title contains `简体` or `Simplified Chinese`.
2. Otherwise prefer language `chi`.
3. Otherwise use the first embedded subtitle stream.

This rule should remain explicit and visible in UI copy because it is heuristic-based.

## Season-Level Embedded Subtitle Consistency

Additional read-only check on 2026-05-10 across whole seasons:

### 庆余年

* Season 1: 46/46 videos share the same embedded subtitle signature.
  * index 0: `subrip`, `chi`, `简体中文`
  * index 1: `subrip`, `chi`, `繁體中文`
  * index 2: `subrip`, `chi`, `Traditional`
* Season 2: 36/36 videos share the same embedded subtitle signature.
  * index 0: `mov_text`, `chi`
  * index 1: `mov_text`, `zht`

### 瑞克和莫蒂第三季

* Season 3: 10/10 videos have `sub_index=0` as `subrip`, `chi`, `Simplified Chinese`.
* The later subtitle ordering is not fully identical across the season:
  * 5 files use Taiwan before Hong Kong traditional Chinese.
  * 4 files use Hong Kong before Taiwan traditional Chinese.
  * 1 file lacks one traditional Chinese variant.
* The Simplified Chinese candidate is stable across the whole checked season.

## Updated Implication

For embedded subtitles, comparing only the full subtitle list is too strict because secondary tracks can vary. A better batch preflight rule is to group by the selected candidate track identity:

* candidate type: embedded
* candidate index: 0
* language: `chi`
* title: `简体中文` / `Simplified Chinese` or equivalent

The preflight dialog should show the detected candidate for each file and require confirmation before creating tasks.

## Rick And Morty All Detected Seasons

Additional read-only check on 2026-05-10 across all detected Rick and Morty season directories.

* Season 1: 11 videos, 0 external subtitle files, 1 unique full subtitle signature.
  * Common candidates: Simplified Chinese, Traditional Chinese, English SDH.
  * Simplified Chinese is stable at embedded index 0.
* Season 2: 10 videos, 0 external subtitle files, 3 unique full subtitle signatures.
  * Common candidates include Simplified Chinese, Traditional Chinese Hong Kong, Traditional Chinese Taiwan, English, Malay, Thai, Indonesian.
  * Simplified Chinese is stable at embedded index 0.
* Season 3: 10 videos, 0 external subtitle files, 3 unique full subtitle signatures.
  * Common candidates include Simplified Chinese, Traditional Chinese Taiwan, English, Malay, Thai, Indonesian, Spanish.
  * Simplified Chinese is stable at embedded index 0.
* Season 4: 10 videos, 0 external subtitle files, 2 unique full subtitle signatures.
  * Common candidates include Simplified Chinese, Traditional Chinese Hong Kong, Traditional Chinese Taiwan, English, Malay, Thai, Indonesian, Spanish.
  * Simplified Chinese is stable at embedded index 0.
* Season 5: 10 videos, 0 external subtitle files, 2 unique full subtitle signatures.
  * Common candidates include Simplified Chinese, Traditional Chinese Hong Kong, Traditional Chinese Taiwan, English, Malay, Thai, Indonesian, Spanish.
  * Simplified Chinese is stable at embedded index 0.
* Season 6: 10 videos, 0 external subtitle files, 10 unique full subtitle signatures.
  * Full subtitle lists vary for every episode, but common normalized candidates still include Simplified Chinese, Traditional Chinese Hong Kong, Traditional Chinese Taiwan, English, Malay, Thai, Indonesian, Portuguese, Spanish, and many European languages.
  * Simplified Chinese is stable at embedded index 0.
* Season 7: 10 videos, 0 external subtitle files, 10 unique full subtitle signatures.
  * Full subtitle lists vary for every episode, but common normalized candidates still include Simplified Chinese, Traditional Chinese Hong Kong, Traditional Chinese Taiwan, English, Malay, Thai, Indonesian, Portuguese, Spanish, and many European languages.
  * Simplified Chinese is stable at embedded index 0.

Implication: for this library, season-level batch preflight should not depend on full subtitle signature equality. Normalized candidate intersection works better. Simplified Chinese is a reliable common candidate across all detected Rick and Morty seasons and stays at embedded index 0 in this snapshot.
