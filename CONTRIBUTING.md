# Contributing

Snapshot Buddy keeps capture and export independent of the camera firmware.
Configuration comes from YAML; durable state belongs in SQLite and the data
directory. The browser is a client of the Go service, not a required component
of capture or encoding.

## Layout

- `cmd/snapshot-buddy`: startup, socket binding, signals, health checks.
- `internal/buddy/config.go` and `protocol.go`: configuration and Prusa metrics.
- `store.go`: schema, transaction boundaries, restart recovery, and export queue.
- `service.go` and `capture.go`: per-printer execution and validated go2rtc JPEGs.
- `video.go`: immutable frame lists, FFmpeg workers, timing, normalization.
- `web.go` and `web/`: embedded server-rendered pages, local assets, and HTTP routes.

UDP reception never waits on a camera. Each printer has an ordered worker,
while the status mutex is held only briefly. The database reserves a capture
before contacting go2rtc, so duplicate triggers never photograph a later
layer on retry. File operations are confined to `os.Root`; a file lock keeps
downloads and encoders safe from concurrent deletion. SQLite uses one
connection to keep transaction ownership straightforward on Raspberry Pi.

Capture completion commits only after the JPEG is renamed into place. A crash
between these operations may leave an unreferenced image, but it cannot expose
a partial frame as saved. No crash-recovery path substitutes a later image.
Video jobs preserve their frame list as JSON and only expose a completed MP4
after FFmpeg succeeds and the temporary output is renamed.

## Verification

```sh
gofmt -w cmd internal
go vet ./...
REQUIRE_FFMPEG=1 go test -race -count=1 ./...
go test ./internal/buddy -run '^$' -fuzz FuzzParsePacket -fuzztime 10s
node --test internal/buddy/testdata/*.test.cjs
python3 -m unittest discover -s scripts -p '*_test.py'
```

Tests create temporary SQLite databases and local HTTP/UDP listeners. Video
tests use real FFmpeg and ffprobe, including fractional timing, sub-one-frame
rates, single-frame exports, mixed source dimensions, and decoded color order.
Without FFmpeg/ffprobe, these tests skip locally; `REQUIRE_FFMPEG=1` makes their
absence a failure, as in CI.

To test the bundled runtime and FFmpeg directly:

```sh
docker build --target test -t snapshot-buddy:integration .
docker run --rm snapshot-buddy:integration
```

Keep lifecycle tests at the behavior boundary, including missed START,
manual close suppression, restart recovery, and concurrent printers. Changes
to the manifest, G-code protocol, or persisted schema require explicit
compatibility considerations. Add schema migrations before incrementing the
SQLite `user_version`.

The UI uses no external fonts, scripts, CDN, or build tooling. Review both
desktop and mobile widths when changing its layout. `go run ./scripts/demo`
starts a loopback-only UI demo at port 8090 with synthetic images, sessions,
and simulated live media. It never contacts a real camera or printer; its
temporary data is separate from any deployment.

Submit changes through a pull request. Source and both container architecture
checks must pass; a second-person approval is not required. Squash merges should
use a concise, capitalized imperative subject without a trailing period and a
body explaining motivation or tradeoffs when needed. Dependabot updates are
reviewed manually; compatible updates are grouped weekly by ecosystem.

## Release

CI tests and starts native ARM64 and AMD64 containers, checks `/healthz` and
`/setup`, and verifies the runtime is non-root. Publish a GitHub release tagged
`vX.Y.Z` to trigger publication; a tag push alone does nothing. The workflow
verifies the tagged source, builds on native runners, collects exact Alpine
source material, and uploads source/checksum assets before creating the combined
GHCR image tags. Both architectures must succeed. Provenance and SBOM attestations
accompany the images. Existing exact version tags are not overwritten.

Stable releases publish `X.Y.Z` and the newest patch in a minor series updates
`X.Y`. `latest` follows GitHub's latest stable release without moving backwards.
Prereleases (for example `v1.1.0-rc.1`) publish only their exact version. Set the
prerelease checkbox when creating them. Verify anonymous pulls after first
publication and make the repository-linked GHCR package public if necessary.
Release publication never deploys dockerpi or any other installation.

Do not put live printer IPs, credentials, or personal camera images in tests
or tracked configuration. Example addresses and generated image fixtures are
sufficient.
