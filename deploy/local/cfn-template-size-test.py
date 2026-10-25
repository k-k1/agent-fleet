#!/usr/bin/env python3
"""Byte budget of the CFN templates that cannot be handed over through S3 (#2756).

    python3 deploy/local/cfn-template-size-test.py

af_cfn_deploy (env.sh) switches to `--s3-bucket` past 51,200 bytes, but the bucket is an output
of 20-platform. So the three stacks that exist before it - 00-network, 10-data and 20-platform
itself - are deployed by direct upload on a stand-up, pause.sh and update.sh, and a template
that crosses the limit there stops the whole deploy with a CLI error. 30-ingress and the rest
come after the bucket; 30-ingress keeps its own budget in cfn-cp-settings-env-test.py.

Fails when one of the three is within 4 KiB of 51,200 bytes (measured on the file, which is what
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
PRE_BUCKET = ["00-network.yaml", "10-data.yaml", "20-platform.yaml"]


def check(name, size):
    if size > BUDGET:
        return ["%s is %d bytes; the budget is %d (%d under %d) - move prose into cfn/PARAMETERS.md"
                % (name, size, BUDGET, HEADROOM, LIMIT)]
    return []


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
    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1
    print("cfn-template-size-test: ok (%s; budget %d)"
          % (", ".join("%s %d" % kv for kv in sizes.items()), BUDGET))
    return 0


if __name__ == "__main__":
    sys.exit(main())
