#!/usr/bin/env python3
"""Regenerate aws_commands.txt.gz from the pinned aws-cli's completion index.

The run rows record `aws <service> <operation>` only when both are names the CLI itself
knows (ADR 0126 decision 1): a pattern cannot tell a service from an option value or a
mistyped secret. The completion index (`awscli/data/ac.index`) is the CLI's own command
table, customizations such as `s3 ls` included.

    python3 -I gen_aws_commands.py /usr/local/aws-cli/v2/<version>/dist/awscli/data/ac.index <version>

Run it after bumping AWSCLI_VERSION in workspace/Dockerfile; TestAWSCommandsMatchPin fails
until the header names the pinned version.
"""
import gzip
import os
import re
import sqlite3
import sys

index, version = sys.argv[1], sys.argv[2]
db = sqlite3.connect("file:" + index + "?mode=ro", uri=True)
name = re.compile(r"^[a-z0-9-]{1,80}$")
services = sorted(r[0] for r in db.execute("SELECT command FROM command_table WHERE parent = 'aws'") if name.match(r[0]))
lines = ["# aws-cli " + version]
for s in services:
    ops = sorted(r[0] for r in db.execute("SELECT command FROM command_table WHERE parent = ?", ("aws." + s,)) if name.match(r[0]))
    lines.append(" ".join([s] + ops))
out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "aws_commands.txt.gz")
with open(out, "wb") as f:
    # mtime=0 keeps the file byte-identical across runs.
    f.write(gzip.compress(("\n".join(lines) + "\n").encode(), mtime=0))
print(f"{len(services)} services -> {out}")
