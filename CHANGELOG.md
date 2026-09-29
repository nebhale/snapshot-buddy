# Changelog

## Unreleased

- Select multiple sessions on the current library page to
  permanently delete closed sessions after confirmation. Report partial results,
  preserve selection through live updates, and reconcile lost responses without
  automatically replaying actions. Native forms remain available without JavaScript.

## 1.3.0

- Add editable session display names with original-name fallback, live updates,
  and protection against conflicting edits in another tab. Printer markers and
  session matching remain unchanged.
- Update print libraries and session pages automatically over authenticated SSE,
  with polling fallback, reconnect recovery, and a compact connection indicator.
- Preserve drafts, focus, scroll position, filters, and expanded panels while
  updating rendered regions; save forms inline and refresh credentials after
  restarts without replaying writes.
- Append capture updates without replacing saved images or live cameras; update
  video progress and frame-rate hints while retaining duration drafts and
  automatic downloads only for requests made in the current tab.
- Use configured printer names and readable marker descriptions throughout
  session pages and the library while retaining original protocol records.
- Add browser regression checks and SSE streaming/deadline coverage, with
  documented public-host verification for the next Cloudflare Tunnel deployment.

MP4 downloads use the current display name without rebuilding existing videos.
The database upgrades automatically to schema 3. Earlier versions cannot open
the upgraded database; retain a pre-upgrade data backup if rollback is needed.
No slicer snippet changes are required when upgrading from 1.2.0.

## 1.2.0

- Allow generated G-code to target a separate `metrics.advertised_port`,
  defaulting to the application's UDP listening port.
- Match Filament Buddy’s slicer filename check, using `Print` for names that
  cannot fit safely in the firmware’s 47-byte metric value.
- Strengthen START and STOP delivery with three markers spaced 1.1 seconds
  apart and a restart-persistent five-second deduplication window. Layer
  markers retain their two copies and 100 ms spacing. Update the Start and
  End slicer blocks after upgrading; existing sliced files are unchanged.
- Align interface terminology and setup guidance with Filament Buddy, keep
  printer IDs visible during active sessions, and show snapshot progress on
  its own line.

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
