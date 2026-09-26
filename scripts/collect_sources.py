#!/usr/bin/env python3
"""Collect exact Alpine sources from a built image's APK inventory.

No APKBUILD is executed. Source archives and local patches must match the
SHA-512 hashes in the recipe recorded in the installed package database.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import tarfile
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


def fetch(url):
    headers = {"User-Agent": "Snapshot-Buddy-source-bundle"}
    if url.startswith("https://api.github.com/") and os.getenv("GH_TOKEN"):
        headers["Authorization"] = "Bearer " + os.environ["GH_TOKEN"]
    for attempt in range(4):
        try:
            with urlopen(Request(url, headers=headers), timeout=90) as response:
                return response.read()
        except HTTPError as error:
            if error.code == 404:
                return None
            if attempt == 3:
                raise
        except (URLError, TimeoutError):
            if attempt == 3:
                raise
        time.sleep(2 ** attempt)


def packages(text):
    result = []
    for record in text.strip().split("\n\n"):
        fields = {}
        for line in record.splitlines():
            if len(line) > 2 and line[1] == ":" and line[0] in "PVAocL":
                fields[line[0]] = line[2:]
        if "P" in fields:
            result.append(fields)
    return result


def archive_path(output):
    return Path(str(output) + ".tar.gz")


def alpine_branch(records):
    for package in records:
        if package.get("P") != "alpine-release":
            continue
        match = re.match(r"^(\d+)\.(\d+)(?:\.\d+)?-r\d+$", package.get("V", ""))
        if match:
            return f"v{match.group(1)}.{match.group(2)}"
        break
    raise ValueError("Cannot determine Alpine release branch from inventory")


def source_checksums(recipe):
    # APKBUILD permits both one-line quoted and multiline checksum lists.
    return re.findall(rb'''(?<![a-f0-9])([a-f0-9]{128})[ \t]+([^\s"']+)''', recipe)


def collect(inventory, output, branch):
    records = packages(inventory.read_text())
    branch = branch or alpine_branch(records)
    output.mkdir(parents=True, exist_ok=True)
    (output / "packages.json").write_text(json.dumps(records, indent=2) + "\n")
    origins = {}
    for p in records:
        origins[(p["o"], p["c"])] = p
    if not origins or not any(origin == "ffmpeg" for origin, _ in origins):
        raise ValueError("Inventory must include the bundled FFmpeg")
    for (origin, commit), package in sorted(origins.items()):
        if not re.fullmatch(r"[a-zA-Z0-9_.+-]+", origin) or not re.fullmatch(r"[a-f0-9]{40}", commit):
            raise ValueError("Unsafe package origin or commit")
        dest = output / (origin + "-" + commit)
        dest.mkdir(exist_ok=True)
        recipe = None
        for repository in ("main", "community", "testing"):
            base = f"https://raw.githubusercontent.com/alpinelinux/aports/{commit}/{repository}/{origin}"
            recipe = fetch(base + "/APKBUILD")
            if recipe is not None:
                break
        if recipe is None:
            raise ValueError(f"No matching recipe for {origin} at {commit}")
        print(f"Collecting {origin} ({package['V']})", flush=True)
        (dest / "APKBUILD").write_bytes(recipe)
        entries = json.loads(fetch(f"https://api.github.com/repos/alpinelinux/aports/contents/{repository}/{origin}?ref={commit}"))
        # Recipes normally keep all patches/configuration alongside APKBUILD.
        # Refuse nested layouts rather than silently producing incomplete source.
        for entry in entries:
            if entry["type"] != "file":
                raise ValueError(f"Review nested recipe content: {origin}/{entry['name']}")
            name = entry["name"]
            if Path(name).name != name:
                raise ValueError("Unsafe recipe filename")
            data = fetch(base + "/" + name)
            if data is None:
                raise ValueError("Missing recipe file")
            (dest / name).write_bytes(data)
        checksums = source_checksums(recipe)
        if not checksums and re.search(rb"(?m)^source=", recipe):
            raise ValueError(f"No source checksums for {origin}")
        for checksum, filename in checksums:
            name = filename.decode()
            if Path(name).name != name or name in (".", ".."):
                raise ValueError("Unsafe source filename")
            path = dest / name
            if path.exists() and hashlib.sha512(path.read_bytes()).hexdigest() == checksum.decode():
                continue
            data = fetch(f"https://distfiles.alpinelinux.org/distfiles/{branch}/{name}")
            if data is None or hashlib.sha512(data).hexdigest() != checksum.decode():
                raise ValueError(f"Missing or mismatched source: {origin}/{name}")
            path.write_bytes(data)
        (dest / "ORIGIN.txt").write_text(base + "\n" + package["L"] + "\n")
    (output / "README.txt").write_text(
        "Corresponding source and notices for the image's Alpine packages.\n"
        "Each directory contains its exact aports APKBUILD, local patches and\n"
        "configuration, plus checksum-verified source archives. Extract those\n"
        "archives for original copyright and license notices. Build recipes\n"
        "record configuration, dependencies and packaging instructions; use\n"
        "Alpine abuild with the matching release and architecture to rebuild.\n"
        "Package versions, licenses and recipe commits are in packages.json.\n"
        "Snapshot Buddy does not modify these upstream components.\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("inventory", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--branch")
    args = parser.parse_args()
    collect(args.inventory, args.output, args.branch)
    archive = archive_path(args.output)
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(args.output, arcname=args.output.name)
    archive.with_suffix(archive.suffix + ".sha256").write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n")
