# Third-party components

Snapshot Buddy's own source is Apache-2.0. Dependency versions and checksums
are recorded in `go.mod` and `go.sum`; dependency licenses remain their own.

The Docker image includes Alpine Linux packages, including FFmpeg with libx264.
These components are not relicensed under Snapshot Buddy's license. FFmpeg's
GPL-enabled build and libx264 have their own distribution requirements.

The bundled FFmpeg executable reports GPL-3.0-or-later. Its package metadata
also lists GPL/LGPL component licenses. Snapshot Buddy invokes FFmpeg as a
separate executable; the Apache-2.0 label describes Snapshot Buddy's source,
not all software in the container.

Each [GitHub release](https://github.com/nebhale/snapshot-buddy/releases) provides
architecture-specific source archives and SHA-256 checksums for the image's
Alpine components, including permissive-license notices as well as copyleft
source. They include exact package inventories, aports
recipes at the commits recorded in the installed package database, local
patches/configuration, and upstream archives checked against recipe SHA-512
hashes. Original notices and license texts are in those source archives.
Missing or mismatched source prevents image publication. These assets remain
available alongside the release; do not replace them with sources from a newer
package version when redistributing an existing image.

License texts and Go dependency notices are also installed under
`/usr/share/licenses/snapshot-buddy` in the container.

- [FFmpeg legal information and source](https://ffmpeg.org/legal.html)
- [x264 source and licensing](https://code.videolan.org/videolan/x264)
- [Alpine source packages](https://gitlab.alpinelinux.org/alpine/aports)
- [modernc SQLite](https://pkg.go.dev/modernc.org/sqlite)
- [Go YAML](https://github.com/go-yaml/yaml)

To identify exact bundled package versions, run `apk info -vv` using the
image's shell entrypoint. Alpine's package indexes link the corresponding
source recipes and upstream archives. Preserve the applicable notices and
source availability when redistributing the image.
