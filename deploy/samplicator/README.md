# Shared Buddy metrics

[Samplicator](https://github.com/sleinen/samplicator) copies each UDP datagram
to Snapshot Buddy and Filament Buddy. This deployment targets a Linux Docker
host, including Raspberry Pi ARM64. It uses the published
[heywoodlh/samplicator image](https://hub.docker.com/r/heywoodlh/samplicator),
pinned by multi-platform digest, and runs without root or Linux capabilities.
The deployment requires no local image build. Its
[Dockerfile](https://github.com/heywoodlh/dockerfiles/blob/master/samplicator/Dockerfile)
builds upstream samplicator on Ubuntu and copies the installed binary into a
separate runtime image.

```text
CORE One -> Docker host LAN IP:8514 -> samplicator
                                      |-> 127.0.0.1:18514 -> snapshot-buddy:8514
                                      `-> 127.0.0.1:18515 -> filament-buddy:8514
```

Keeping the public port at 8514 preserves existing sliced files. Each app's
generated `M334` must advertise the Docker host LAN IP and port 8514, never
the localhost forwarding port. The apps can restart independently: fixed host
ports avoid caching container IP addresses in samplicator. The second app does
not need to exist when the relay starts; UDP packets sent while it is absent
are lost, with no replay.

## Install

Copy this directory to `/srv/docker/samplicator`. Copy `.env.example` to `.env`
and `samplicator.conf.example` to `samplicator.conf`. Set the host LAN address
in `.env` and the printer's reserved address in `samplicator.conf`.

Pull before interrupting the existing receiver:

```sh
docker compose config --quiet
docker compose pull
```

Run `python3 verify.py` on that Linux host to check exact datagram duplication,
large packets, source filtering, and receiver/relay restarts using isolated
ports and temporary receivers. It does not contact the printer or either app.
For an image update, pull the candidate digest and run
`python3 verify.py --image <repository>@sha256:<digest>` before changing the
Compose image. Keep the previous digest available for rollback.

Back up Snapshot Buddy's `compose.yaml` and `config.yaml`. When the printer
is idle, change **only its UDP port mapping**, retaining its HTTP mapping and
other settings:

```yaml
services:
  snapshot-buddy:
    ports:
      - "8080:8080/tcp"
      - "127.0.0.1:18514:8514/udp"
```

Use this Snapshot Buddy application configuration:

```yaml
metrics:
  address: ":8514"
  advertised_host: "192.168.1.50" # Docker host LAN IP; relay listens here
  advertised_port: 8514 # optional; defaults to the application's listening port
```

`metrics.advertised_port` controls the printer destination in generated G-code;
`metrics.address` controls the application's listener. Both use 8514 in this
deployment, so the override can be omitted. Docker's host forwarding port
18514 is not the application's listening port. Set an explicit advertised
port when a relay uses a different port from the application, then regenerate
the Start G-code snippet.

Samplicator's normal mode changes the UDP sender. If `printers[].source_ip`
is configured, use the source address observed at the consumer (typically
its Docker network gateway), **not the printer's LAN IP**. Obtain the network
gateway with `docker network inspect <network>` and verify it with packet
capture. The relay's configuration performs the original printer-IP check.
Do not use source spoofing (`-S`); it requires raw sockets and additional
routing configuration. Marker IDs still route events to the correct printer.

Recreate Snapshot Buddy with `docker compose up -d` in its directory, then
start the relay with `docker compose up -d` in this directory. Only the relay
should own the host LAN UDP port 8514. No changes to `M331`/`M332` wrappers or
application markers are needed.

Configure Filament Buddy's Compose service:

```yaml
services:
  filament-buddy:
    ports:
      - "127.0.0.1:18515:8514/udp"
```

Use the same metrics configuration for Filament Buddy. Both applications
support `metrics.advertised_port` with the same default. Apply the same
sender-address guidance. Keep its HTTP configuration and other settings as
defined by Filament Buddy.

## Verification and rollback

Check `docker compose ps` and `docker compose logs` in both directories, then
check Snapshot Buddy's `/healthz` and `/setup`. The generated Start G-code
must still contain `M334 <Docker-host-LAN-IP> 8514 13514`.

Verify packet forwarding using an isolated relay and UDP receivers before
cutover; do not inject synthetic START/LAYER/STOP markers into production.
During the next print, confirm real markers reach both apps. UDP delivery
remains best effort and the relay stores no data.

To roll back, stop samplicator, restore Snapshot Buddy's saved Compose and
configuration files, and run `docker compose up -d` in its directory. The
printer endpoint is unchanged throughout, and application data is untouched.
