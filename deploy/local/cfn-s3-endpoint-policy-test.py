#!/usr/bin/env python3
"""The S3 gateway endpoint's policy, checked against the templates, offline.

    python3 deploy/local/cfn-s3-endpoint-policy-test.py

## Why this exists

00-network's S3Endpoint sits on the private route table, so whatever its policy admits
reaches S3 without crossing the NAT gateway (and later the egress gateway of ADR 0122).
With no PolicyDocument that is every bucket in the region (#2657). So this check fails when

  - the endpoint has no policy, or a statement is not an Allow with a concrete action list;
  - a Resource is "*", or names no concrete bucket (`s3:::*`), or the set of buckets differs
    from the pinned list below - a new bucket is a decision, made here and in the template;
  - the models bucket name pattern in the endpoint stops matching the bucket 60-engines
    creates (the CP and the ingest tasks would lose it to AccessDenied).

The positive controls at the end run the same checks against broken copies, to show each
can fail at all.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import fnmatch
import os
import re
import sys

try:
    import yaml
except ImportError:
    sys.exit("cfn-s3-endpoint-policy-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CFN = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn")

# The buckets the endpoint may admit, as the Resource strings with the CloudFormation
# pseudo parameters spelled out.
EXPECTED = {
    "arn:${AWS::Partition}:s3:::prod-${AWS::Region}-starport-layer-bucket/*",
    "arn:${AWS::Partition}:s3:::al2023-repos-${AWS::Region}-de612dc2/*",
    "arn:${AWS::Partition}:s3:::af-*-models-${AWS::AccountId}/*",
    "arn:${AWS::Partition}:s3:::af-*-models-${AWS::AccountId}",
}


class CfnLoader(yaml.SafeLoader):
    pass


def _intrinsic(tag):
    def construct(loader, node):
        if isinstance(node, yaml.ScalarNode):
            return {"!" + tag: loader.construct_scalar(node)}
        if isinstance(node, yaml.SequenceNode):
            return {"!" + tag: loader.construct_sequence(node, deep=True)}
        return {"!" + tag: loader.construct_mapping(node, deep=True)}
    return construct


for _t in ("Ref", "Sub", "GetAtt", "ImportValue", "Select", "Split", "If", "Equals", "Not",
           "Join", "FindInMap", "Base64", "Condition", "And", "Or", "GetAZs", "Cidr"):
    CfnLoader.add_constructor("!" + _t, _intrinsic(_t))


def load(name):
    with open(os.path.join(CFN, name)) as f:
        return yaml.load(f, Loader=CfnLoader)


def as_list(v):
    return v if isinstance(v, list) else [v]


def check(net, engines):
    fails = []
    props = net["Resources"]["S3Endpoint"]["Properties"]
    doc = props.get("PolicyDocument")
    if not doc:
        return ["S3Endpoint has no PolicyDocument (full access)"]
    resources = set()
    for st in doc.get("Statement", []):
        sid = st.get("Sid", "?")
        if st.get("Effect") != "Allow":
            fails.append("%s: not an Allow" % sid)
        for a in as_list(st.get("Action", [])):
            if a in ("*", "s3:*") or a.endswith("*"):
                fails.append("%s: wildcard action %s" % (sid, a))
        if not st.get("Action"):
            fails.append("%s: no Action" % sid)
        for r in as_list(st.get("Resource", [])):
            r = r.get("!Sub", r) if isinstance(r, dict) else r
            resources.add(r)
            if r == "*" or re.search(r":s3:::\*", r):
                fails.append("%s: Resource %s names no concrete bucket" % (sid, r))
    if resources != EXPECTED:
        fails.append("bucket set differs: extra %s, missing %s"
                     % (sorted(resources - EXPECTED), sorted(EXPECTED - resources)))

    # The models bucket 60-engines creates must match the endpoint's wildcard.
    name = engines["Resources"]["ModelsBucket"]["Properties"]["BucketName"]["!Sub"]
    sample = name.replace("${AWS::StackName}", "af-ecs-engines").replace("${AWS::AccountId}", "123456789012")
    pat = [r for r in resources if "-models-" in r and r.endswith("/*")]
    pat = pat[0].split(":s3:::")[1][:-2].replace("${AWS::AccountId}", "123456789012") if pat else ""
    if not fnmatch.fnmatchcase(sample, pat):
        fails.append("models bucket %r does not match the endpoint pattern %r" % (sample, pat))
    return fails


def main():
    try:
        net, engines = load("00-network.yaml"), load("60-engines.yaml")
    except (OSError, KeyError, yaml.YAMLError) as e:
        print("cfn-s3-endpoint-policy-test: cannot run: %s" % e, file=sys.stderr)
        return 2
    fails = check(net, engines)
    for f in fails:
        print("FAIL  " + f)
    failed = bool(fails)
    if not fails:
        print("ok    the S3 endpoint policy admits exactly the pinned buckets")

    def mutated(fn):
        t = copy.deepcopy(net)
        fn(t["Resources"]["S3Endpoint"]["Properties"])
        return t

    def stmts(p):
        return p["PolicyDocument"]["Statement"]

    def renamed_bucket(e):
        e = copy.deepcopy(e)
        e["Resources"]["ModelsBucket"]["Properties"]["BucketName"]["!Sub"] = "models-${AWS::StackName}"
        return e

    controls = [
        ("policy removed", mutated(lambda p: p.pop("PolicyDocument")), engines),
        ("resource widened to *", mutated(lambda p: stmts(p)[0].update(Resource="*")), engines),
        ("any bucket", mutated(lambda p: stmts(p)[0].update(Resource="arn:aws:s3:::*/*")), engines),
        ("s3:* granted", mutated(lambda p: stmts(p)[0].update(Action="s3:*")), engines),
        ("deny statement", mutated(lambda p: stmts(p)[0].update(Effect="Deny")), engines),
        ("extra bucket", mutated(lambda p: stmts(p).append(
            {"Effect": "Allow", "Action": ["s3:GetObject"], "Resource": "arn:aws:s3:::other/*"})), engines),
        ("ECR bucket dropped", mutated(lambda p: stmts(p).pop(0)), engines),
        ("models bucket renamed", net, renamed_bucket(engines)),
    ]
    for name, n, e in controls:
        if check(n, e):
            print("ok    control: %s fails" % name)
        else:
            print("FAIL  control: %s passed - the check cannot see it" % name)
            failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
