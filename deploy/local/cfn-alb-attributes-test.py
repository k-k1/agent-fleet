#!/usr/bin/env python3
"""ALB attributes of 30-ingress.yaml, evaluated for AlbAccessLogBucket empty and set.

    python3 deploy/local/cfn-alb-attributes-test.py

An attribute left out of LoadBalancerAttributes keeps its CURRENT value on the ALB, so
clearing AlbAccessLogBucket on a stack that had logs on would leave them on (and billed) if the
template just dropped the attribute. This evaluates the list the way CloudFormation does for
each parameter value and fails unless

  - access_logs.s3.enabled is always present: "false" when the bucket is empty, "true" with the
    bucket named by the parameter when it is set;
  - routing.http.drop_invalid_header_fields.enabled is "true" in both cases.

A positive control runs the same check on a copy with the old shape (attribute dropped when
off) to show the check can fail. exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import os
import sys

try:
    import yaml
except ImportError:
    sys.exit("cfn-alb-attributes-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
TEMPLATE = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn", "30-ingress.yaml")


class CfnLoader(yaml.SafeLoader):
    pass


def _intrinsic(name):
    def construct(loader, node):
        if isinstance(node, yaml.ScalarNode):
            val = loader.construct_scalar(node)
        elif isinstance(node, yaml.SequenceNode):
            val = loader.construct_sequence(node, deep=True)
        else:
            val = loader.construct_mapping(node, deep=True)
        return {name: val}
    return construct


for _tag, _name in (("!If", "Fn::If"), ("!Ref", "Ref"), ("!Equals", "Fn::Equals"), ("!Not", "Fn::Not"),
                    ("!Sub", "Fn::Sub"), ("!Split", "Fn::Split"), ("!GetAtt", "Fn::GetAtt"),
                    ("!Join", "Fn::Join"), ("!And", "Fn::And"), ("!Or", "Fn::Or"),
                    ("!Select", "Fn::Select"), ("!FindInMap", "Fn::FindInMap"), ("!Condition", "Condition"),
                    ("!ImportValue", "Fn::ImportValue"), ("!Base64", "Fn::Base64")):
    CfnLoader.add_constructor(_tag, _intrinsic(_name))


def evaluate(doc, bucket):
    """The LoadBalancerAttributes as {key: value} for one AlbAccessLogBucket value."""
    conds = doc["Conditions"]

    def cond(name):
        c = conds[name]
        assert "Fn::Not" in c and "Fn::Equals" in c["Fn::Not"][0], "HasAlbLogs changed shape: update this test"
        a, b = c["Fn::Not"][0]["Fn::Equals"]
        assert a == {"Ref": "AlbAccessLogBucket"}, "HasAlbLogs no longer tests AlbAccessLogBucket"
        return not (bucket == b)

    def ev(v):
        if isinstance(v, dict) and "Fn::If" in v:
            name, yes, no = v["Fn::If"]
            return ev(yes if cond(name) else no)
        if isinstance(v, dict) and v == {"Ref": "AWS::NoValue"}:
            return None
        if isinstance(v, dict) and v == {"Ref": "AlbAccessLogBucket"}:
            return bucket
        return v

    out = {}
    for item in doc["Resources"]["Alb"]["Properties"]["LoadBalancerAttributes"]:
        item = ev(item)
        if item is not None:
            out[item["Key"]] = ev(item["Value"])
    return out


def check(doc):
    errs = []
    off, on = evaluate(doc, ""), evaluate(doc, "my-logs")
    if off.get("access_logs.s3.enabled") != "false":
        errs.append(f"bucket empty: access_logs.s3.enabled = {off.get('access_logs.s3.enabled')!r}, want \"false\" "
                    "(an omitted attribute keeps the current value)")
    if on.get("access_logs.s3.enabled") != "true" or on.get("access_logs.s3.bucket") != "my-logs":
        errs.append(f"bucket set: enabled/bucket = {on.get('access_logs.s3.enabled')!r}/"
                    f"{on.get('access_logs.s3.bucket')!r}, want \"true\"/\"my-logs\"")
    for label, got in (("empty", off), ("set", on)):
        if got.get("routing.http.drop_invalid_header_fields.enabled") != "true":
            errs.append(f"bucket {label}: drop_invalid_header_fields.enabled is not \"true\"")
    return errs


def main():
    with open(TEMPLATE) as f:
        doc = yaml.load(f, Loader=CfnLoader)
    errs = check(doc)
    for e in errs:
        print("FAIL:", e)
    if errs:
        return 1
    broken = copy.deepcopy(doc)
    attrs = broken["Resources"]["Alb"]["Properties"]["LoadBalancerAttributes"]
    for i, item in enumerate(attrs):
        if isinstance(item, dict) and item.get("Key") == "access_logs.s3.enabled":
            attrs[i] = {"Fn::If": ["HasAlbLogs", item, {"Ref": "AWS::NoValue"}]}
    if not check(broken):
        print("FAIL: positive control: the old shape (attribute dropped when off) passed the check")
        return 2
    print("cfn-alb-attributes: OK (off states false, on states true + bucket, invalid headers dropped; "
          "positive control caught the old shape)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
