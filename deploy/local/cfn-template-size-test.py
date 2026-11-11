#!/usr/bin/env python3
"""Byte budget of the CFN templates that cannot be handed over through S3 (#2756).

    python3 deploy/local/cfn-template-size-test.py

af_cfn_deploy (env.sh) switches to `--s3-bucket` past 51,200 bytes, but the bucket is an output
of 20-platform. So a template that is sent by direct upload while no bucket exists stops the
deploy with a CLI error when it crosses the limit:

  - 00-network, 10-data and 20-platform on a first stand-up (update.sh deploys neither 00 nor
    10; only pause.sh updates 00-network, through af_cfn_deploy, which can use an existing
    bucket but is not something to rely on);
  - 40-ec2-pool in deploy/aws/ecs/harness/setup.sh, which sends it with a plain
    `aws cloudformation deploy --template-file` against dummy network / platform exports.

30-ingress and the rest come after the bucket; 30-ingress keeps its own budget in
cfn-cp-settings-env-test.py (64 KiB, above the inline limit). Because it may therefore need S3,
every documented or printed `--template-file cfn/30-ingress.yaml` command must carry
`--s3-bucket` too: the AWS CLI refuses a direct upload over 51,200 bytes before calling the API.

Fails when one of them is within 4 KiB of 51,200 bytes (measured on the file, which is what
the AWS CLI measures). Long prose belongs in cfn/PARAMETERS.md: a YAML comment counts exactly
like a Description.

The positive control pads each template's size to one byte over the budget and checks it is caught.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
CFN = os.path.join(HERE, "..", "aws", "ecs", "cfn")
LIMIT = 51200
HEADROOM = 4096
BUDGET = LIMIT - HEADROOM
PRE_BUCKET = ["00-network.yaml", "10-data.yaml", "20-platform.yaml", "40-ec2-pool.yaml"]


def check(name, size):
    if size > BUDGET:
        return ["%s is %d bytes; the budget is %d (%d under %d) - move prose into cfn/PARAMETERS.md"
                % (name, size, BUDGET, HEADROOM, LIMIT)]
    return []


def ingress_commands():
    """(file, line, command text) for each `--template-file ... 30-ingress.yaml` command under deploy/aws/ecs.

    A command is a line joined with its backslash continuations, so the flag and the file name
    may sit on different lines. `echo "DRY: ..."` lines are skipped: they print a plan for a
    human, and the real call goes through af_cfn_deploy, which adds the bucket by itself.
    """
    root = os.path.join(HERE, "..", "aws", "ecs")
    out = []
    for dirpath, dirs, files in os.walk(root):
        dirs[:] = [d for d in dirs if d not in ("harness", "comfyui", "engine-tools")]
        for fn in files:
            if not fn.endswith((".md", ".sh")):
                continue
            path = os.path.join(dirpath, fn)
            with open(path, encoding="utf-8") as fh:
                lines = fh.read().split("\n")
            out += [(os.path.relpath(path, root), n, t) for n, t in ingress_blocks(lines)]
    return out


def ingress_blocks(lines):
    out, i = [], 0
    while i < len(lines):
        j = i
        while j + 1 < len(lines) and lines[j].rstrip().endswith("\\"):
            j += 1
        text = "\n".join(lines[i:j + 1])
        if "--template-file" in text and "30-ingress.yaml" in text and 'echo "DRY:' not in text:
            out.append((i + 1, text))
        i = j + 1
    return out


def check_ingress_commands(cmds):
    return ["%s:%d deploys 30-ingress with --template-file but no --s3-bucket" % (f, n)
            for f, n, text in cmds if "--s3-bucket" not in text]


def main():
    sizes = {}
    for name in PRE_BUCKET:
        path = os.path.join(CFN, name)
        if not os.path.isfile(path):
            print("cfn-template-size-test: %s not found" % path)
            return 2
        sizes[name] = os.path.getsize(path)
    fails = []
    for name, size in sizes.items():
        fails += check(name, size)
        # positive control: padding the file by whatever takes it one byte over the budget
        if not check(name, size + max(BUDGET + 1 - size, 0)):
            fails.append("positive control for %s was not caught" % name)
    cmds = ingress_commands()
    if not cmds:
        print("cfn-template-size-test: no 30-ingress deploy command found; the scan is broken")
        return 2
    fails += check_ingress_commands(cmds)
    # positive control: the same commands without the flag are caught
    if len(check_ingress_commands([(f, n, t.replace("--s3-bucket", "--nope")) for f, n, t in cmds])) != len(cmds):
        fails.append("positive control for the --s3-bucket scan did not catch every command")
    if len(ingress_blocks(["aws cloudformation deploy \\", "  --template-file \\", "  cfn/30-ingress.yaml"])) != 1:
        fails.append("positive control: a flag and file name on separate lines was not seen")
    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1
    print("cfn-template-size-test: ok (%s; budget %d; %d 30-ingress commands via S3)"
          % (", ".join("%s %d" % kv for kv in sizes.items()), BUDGET, len(cmds)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
