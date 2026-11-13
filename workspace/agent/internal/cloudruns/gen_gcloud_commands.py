#!/usr/bin/env python3
"""Regenerate gcloud_commands.txt.gz from the pinned Google Cloud SDK's completion tree.

The run rows and the login card record `gcloud <command path>` only when every word is a
command the CLI itself knows (ADR 0126 decision 1): a pattern cannot tell a command from an
option value or a mistyped secret. The static completion tree
(`<sdk>/data/cli/gcloud_completions.py`) is the CLI's own command table, every command and
command group with its flags.

    python3 -I gen_gcloud_commands.py <sdk root, the directory holding bin/gcloud> <version>

The file holds one line per leaf command path (words separated by spaces); the loader adds every
prefix, so a command group is known too. Run it after bumping GCLOUD_VERSION in workspace/Dockerfile;
TestGCloudCommandsMatchPin fails until the header names the pinned version.
cli-pin-bump.sh only bumps the agent CLIs, so this regeneration is by hand, like aws's.
"""
import gzip
import os
import re
import runpy
import sys

sdk, version = sys.argv[1], sys.argv[2]
tree = runpy.run_path(os.path.join(sdk, "data", "cli", "gcloud_completions.py"))["STATIC_COMPLETION_CLI_TREE"]
name = re.compile(r"^[a-z0-9][a-z0-9-]{0,79}$")
paths = []


def walk(node, prefix):
    for cmd in sorted(node.get("commands", {})):
        if not name.match(cmd):
            continue
        p = prefix + [cmd]
        if node["commands"][cmd].get("commands"):
            walk(node["commands"][cmd], p)
        else:
            paths.append(" ".join(p))


walk(tree, [])
out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "gcloud_commands.txt.gz")
with open(out, "wb") as f:
    # mtime=0 keeps the file byte-identical across runs.
    f.write(gzip.compress(("# gcloud " + version + "\n" + "\n".join(paths) + "\n").encode(), 9, mtime=0))
print(f"{len(paths)} commands -> {out}")
