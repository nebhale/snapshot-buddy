"""Exercise the configured relay image on isolated ports; no printer traffic."""

import argparse
import pathlib
import socket
import subprocess
import tempfile
import time
import uuid


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def receiver():
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("127.0.0.1", 0))
    sock.settimeout(2)
    return sock


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--image", help="Test a candidate image instead of the Compose image")
args = parser.parse_args()
image = args.image or docker(
    "compose", "-f", str(pathlib.Path(__file__).with_name("compose.yaml")),
    "config", "--images",
)

with tempfile.TemporaryDirectory(prefix="samplicator-test-") as directory:
    first, second, reserved = receiver(), receiver(), receiver()
    ingress = reserved.getsockname()[1]
    reserved.close()
    config = pathlib.Path(directory) / "samplicator.conf"
    config.write_text(
        f"127.0.0.1: 127.0.0.1/{first.getsockname()[1]} "
        f"127.0.0.1/{second.getsockname()[1]}\n"
    )
    name = "samplicator-test-" + uuid.uuid4().hex[:8]
    try:
        docker(
            "run", "-d", "--name", name, "--network", "host",
            "--user", "10001:10001",
            "--read-only", "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges:true",
            "--mount", f"type=bind,source={config},target=/config,readonly",
            image, "-4", "-s", "127.0.0.1",
            "-p", str(ingress), "-c", "/config",
        )
        time.sleep(0.3)
        assert docker("inspect", "-f", "{{.State.Running}}", name) == "true"
        assert docker("exec", name, "id", "-u") == "10001"
        sender = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        sender.bind(("127.0.0.1", 0))
        destination = ("127.0.0.1", ingress)
        for packet in [b'probe v="first"\nprobe v="second"\n', bytes(range(256)) * 200]:
            sender.sendto(packet, destination)
            for sink in (first, second):
                actual, _ = sink.recvfrom(65535)
                assert actual == packet, (len(actual), len(packet))
        rejected = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        rejected.bind(("127.0.0.2", 0))
        rejected.sendto(b"must be filtered", destination)
        for sink in (first, second):
            try:
                sink.recvfrom(65535)
            except TimeoutError:
                pass
            else:
                raise AssertionError("unapproved source passed filter")
        second_port = second.getsockname()[1]
        second.close()
        for i in range(3):
            packet = f"missing consumer {i}".encode()
            sender.sendto(packet, destination)
            assert first.recvfrom(65535)[0] == packet
            time.sleep(0.1)
        second = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        second.bind(("127.0.0.1", second_port))
        second.settimeout(2)
        sender.sendto(b"consumer back", destination)
        assert first.recvfrom(65535)[0] == b"consumer back"
        assert second.recvfrom(65535)[0] == b"consumer back"
        docker("restart", name)
        time.sleep(0.3)
        sender.sendto(b"relay back", destination)
        assert first.recvfrom(65535)[0] == b"relay back"
        assert second.recvfrom(65535)[0] == b"relay back"
        print("PASS: exact fan-out, 51KB datagram, source filter, absent/restarted consumer, relay restart, non-root")
    finally:
        first.close()
        second.close()
        docker("rm", "-f", name)
