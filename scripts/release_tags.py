#!/usr/bin/env python3
"""Choose container tags without moving a minor alias backwards."""
import argparse
import json
import re


def version(tag):
    match = re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?", tag)
    if not match:
        raise ValueError("Release tags must be vMAJOR.MINOR.PATCH, optionally with a prerelease suffix")
    return tuple(map(int, match.group(1, 2, 3))), bool(match.group(4))


def tags(tag, prerelease, latest, releases):
    current, suffix = version(tag)
    result = [tag[1:]]
    if prerelease or suffix:
        return result
    higher_minor = False
    higher_stable = False
    for release in releases:
        if release.get("prerelease") or release.get("draft"):
            continue
        try:
            other, pre = version(release["tag_name"])
        except (ValueError, KeyError):
            continue
        if not pre and other > current:
            higher_stable = True
            if other[:2] == current[:2]:
                higher_minor = True
    if not higher_minor:
        result.append(f"{current[0]}.{current[1]}")
    if latest == tag and not higher_stable:
        result.append("latest")
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("--validate", action="store_true")
    parser.add_argument("--latest", default="")
    parser.add_argument("--prerelease", choices=["true", "false"], default="false")
    parser.add_argument("--releases")
    args = parser.parse_args()
    version(args.tag)
    if not args.validate:
        with open(args.releases) as source:
            releases = json.load(source)
        print("\n".join(tags(args.tag, args.prerelease == "true", args.latest, releases)))
