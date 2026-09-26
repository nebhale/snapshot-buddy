# Security

Security fixes target the latest stable release. Upgrade to the latest patch
release before reporting an issue that may already be fixed.

Please report suspected vulnerabilities privately through
[GitHub security advisories](https://github.com/nebhale/snapshot-buddy/security/advisories/new).
Do not include credentials, camera URLs, private images, or printer addresses
in public issues. Include the affected version, a minimal reproduction, and
the impact you observed. There is no guaranteed response time.

Snapshot Buddy is intended for a trusted LAN or an authenticated HTTPS reverse
proxy. Configure authentication before exposing it to untrusted clients.
UDP printer markers are not authenticated; sender-IP filtering is an additional
guard, not a replacement for network isolation. WebRTC media needs its own
reachable, appropriately restricted go2rtc network path.

Dependabot monitors Go modules, Docker base-image references, and GitHub Actions.
Updates are reviewed manually. It does not directly update individual Alpine
packages such as FFmpeg; those are reviewed when rebuilding a release.
