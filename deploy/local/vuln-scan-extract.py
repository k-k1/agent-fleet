#!/usr/bin/env python3
"""Extract an untrusted rootfs tar stream (stdin) into a directory as DATA.

    zstd -dc rootfs.tar.zst | vuln-scan-extract.py <dest>

The scan runs with a token that can write issues, on a package it did not build, so the
extraction must not be able to write outside <dest> or leave anything executable-by-
accident behind: absolute paths and `..` are refused, a member whose path goes through a
symlink that leaves <dest> is refused (tarfile's tar filter resolves real paths), hard
links must stay inside, device nodes / fifos are skipped, and owner and mode bits
(setuid) are dropped. Absolute symlinks are kept as symlinks: a rootfs is full of them and
nothing here follows them. Any refusal is exit 1, i.e. a red run.
"""
import os
import sys
import tarfile


def data_filter(member, dest):
    if member.isdev() or member.isfifo():
        return None
    m = tarfile.tar_filter(member, dest)  # raises FilterError on escapes
    # Readable by us whatever the archive says; no setuid, no foreign owner.
    return m.replace(mode=(0o755 if m.isdir() or (m.mode & 0o100) else 0o644),
                     uid=None, gid=None, uname=None, gname=None, deep=False)


def main(argv):
    if len(argv) != 2:
        print("usage: vuln-scan-extract.py <dest>", file=sys.stderr)
        return 2
    dest = argv[1]
    os.makedirs(dest, exist_ok=True)
    try:
        with tarfile.open(fileobj=sys.stdin.buffer, mode="r|") as tar:
            tar.extractall(dest, filter=data_filter)
    except (tarfile.TarError, OSError) as e:
        print(f"ERROR: refusing the rootfs: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
