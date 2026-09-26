# Changelog

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
