#!/usr/bin/env python3
"""Resolve the rootfs a native package points at (rootfs.json on stdin).

    tar xzf native.tar.gz -O <pkg>/rootfs.json | vuln-scan-rootfs-ref.py <dist-repo> <arch>

Prints `<release-tag> <asset-name> <sha256>`. publish-dist.sh lets a package reference a
rootfs published by an EARLIER release (a --rootfs-json reuse build), so the rootfs is not
necessarily an asset of the release being scanned: the package's own reference decides.
The file is untrusted data, so the URL must be exactly this dist repo's release download of
a vX.Y.Z tag for the expected architecture, and the sha256 must be 64 hex digits.
Anything else is exit 1 (a red run), never a guess.
"""
import json
import re
import sys


def main(argv):
    if len(argv) != 2:
        print("usage: vuln-scan-rootfs-ref.py <dist-repo> <arch>", file=sys.stderr)
        return 2
    repo, arch = argv
    try:
        doc = json.load(sys.stdin)
        url, sha = doc["url"], doc["sha256"]
    except (ValueError, KeyError, TypeError) as e:
        print(f"ERROR: rootfs.json unreadable: {e}", file=sys.stderr)
        return 1
    m = re.fullmatch(
        re.escape(f"https://github.com/{repo}/releases/download/") +
        r"(v[0-9]+\.[0-9]+\.[0-9]+)/(agent-fleet-rootfs-[0-9A-Za-z.]+-linux-" + re.escape(arch) + r"\.tar\.zst)",
        url if isinstance(url, str) else "")
    if not m or not isinstance(sha, str) or not re.fullmatch(r"[0-9a-f]{64}", sha):
        print("ERROR: rootfs.json does not point at this dist repo's release asset for " + arch, file=sys.stderr)
        return 1
    print(m.group(1), m.group(2), sha)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
