# Changelog

## Unreleased

- Allow generated G-code to target a separate `metrics.advertised_port`,
  defaulting to the application's UDP listening port.

- Match Filament Buddy’s slicer filename check, using `Print` for names that
  cannot fit safely in the firmware’s 47-byte metric value.

- Strengthen START and STOP delivery with three markers spaced 1.1 seconds
  apart and a restart-persistent five-second deduplication window. Layer
  markers retain their two copies and 100 ms spacing. Update the Start and
  End slicer blocks after upgrading; existing sliced files are unchanged.

## 1.1.0

- Shorten generated markers to `SB1` and rename the layer notification from
  `FRAME` to `LAYER`, leaving more of the firmware's 47-byte metric value for
  printer IDs and print names.
- Continue accepting legacy `SNAPSHOT_BUDDY_V1` and `FRAME` markers from
  already-sliced files.
- Derive Alpine source archives from the built runtime version so release
  notices remain aligned when the base image changes.

## 1.0.0

The first stable release of Snapshot Buddy.

- Collect layer-triggered JPEG snapshots from named go2rtc streams.
- Support multiple printers, duplicate UDP marker handling, recovered sessions,
  manual closure, and restart-persistent session state.
- Browse sessions, capture failures, and browser-local timestamps.
- Watch live camera previews and expand them to a screen-fitting modal.
- Download ordered TAR archives with capture manifests.
- Create duration-controlled H.264 MP4 timelapses, defaulting to 10 seconds,
  with automatic downloads, session-based filenames, and reusable cached exports.
- Deploy configurable, non-root Linux ARM64 and AMD64 containers with SQLite
  metadata, persistent image storage, and optional Basic authentication.

Camera RTSP support and an existing go2rtc service are required. The slicer
integration requires a printer capable of sending Prusa Buddy G-code metrics.
No printer parking or additional motion is introduced.
