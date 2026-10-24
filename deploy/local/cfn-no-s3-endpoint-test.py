#!/usr/bin/env python3
"""No S3 gateway endpoint route on the private route table, checked offline.

    python3 deploy/local/cfn-no-s3-endpoint-test.py

## Why this exists

An S3 gateway endpoint on the private route table lets a workspace reach S3 without
crossing the NAT gateway (and later the egress gateway of ADR 0122), unrecorded (#2657).
A policy narrows it but does not reroute what it refuses: the prefix-list route beats
0.0.0.0/0, so the refusal is AccessDenied. Until #2700 lands the end state, all S3 leaves
through the NAT. So this check fails when any template under deploy/aws/ecs/cfn

  - declares an AWS::EC2::VPCEndpoint for an S3 service (gateway or interface), or one whose
    ServiceName is built from parts (!Join and the like), which cannot be told from S3, or
  - routes a prefix list (an AWS::EC2::Route with DestinationPrefixListId) at all.

The positive controls at the end run the same checks against broken copies, to show each
can fail at all. When #2700 puts an endpoint back, this test is replaced by that decision's
own, not relaxed.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import glob
import os
import sys

try:
    import yaml
except ImportError:
    sys.exit("cfn-no-s3-endpoint-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CFN = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn")


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


def load_all():
    out = {}
    for path in sorted(glob.glob(os.path.join(CFN, "*.yaml"))):
        with open(path) as f:
            out[os.path.basename(path)] = yaml.load(f, Loader=CfnLoader)
    return out


def leaves(v):
    """Every string inside a (possibly intrinsic-wrapped) value."""
    if isinstance(v, str):
        yield v
    elif isinstance(v, dict):
        for x in v.values():
            yield from leaves(x)
    elif isinstance(v, list):
        for x in v:
            yield from leaves(x)


def is_s3_or_unknown(service):
    """True for an S3 service name, or one this check cannot resolve (fail safe).

    A literal or a !Sub string is read as text. Anything else (!Join, !FindInMap, !If,
    !ImportValue, ...) builds the name from parts, so it is refused rather than guessed:
    `!Join ['.', [com, amazonaws, !Ref AWS::Region, s3]]` is the same service.
    """
    if isinstance(service, str):
        text = service
    elif isinstance(service, dict) and list(service) == ["!Sub"] and isinstance(service["!Sub"], str):
        text = service["!Sub"]
    else:
        return True
    return "s3" in text.lower().replace("-", ".").split(".")


def check(templates):
    fails = []
    for tname, t in templates.items():
        for rname, r in (t.get("Resources") or {}).items():
            props = r.get("Properties") or {}
            if r.get("Type") == "AWS::EC2::VPCEndpoint":
                if is_s3_or_unknown(props.get("ServiceName")):
                    fails.append("%s: %s is an S3 VPC endpoint (or its service name is not a plain string)"
                                 % (tname, rname))
            if r.get("Type") == "AWS::EC2::Route" and "DestinationPrefixListId" in props:
                fails.append("%s: %s routes a prefix list" % (tname, rname))
    return fails


def main():
    try:
        templates = load_all()
        if "00-network.yaml" not in templates:
            raise OSError("00-network.yaml not found")
    except (OSError, yaml.YAMLError) as e:
        print("cfn-no-s3-endpoint-test: cannot run: %s" % e, file=sys.stderr)
        return 2
    fails = check(templates)
    for f in fails:
        print("FAIL  " + f)
    failed = bool(fails)
    if not fails:
        print("ok    no S3 endpoint and no prefix-list route in %d templates" % len(templates))

    def with_resources(extra, template="00-network.yaml"):
        t = copy.deepcopy(templates)
        t[template]["Resources"].update(extra)
        return t

    gateway = {"Type": "AWS::EC2::VPCEndpoint", "Properties": {
        "VpcId": {"!Ref": "Vpc"}, "ServiceName": {"!Sub": "com.amazonaws.${AWS::Region}.s3"},
        "VpcEndpointType": "Gateway", "RouteTableIds": [{"!Ref": "PrivateRt"}]}}
    interface = copy.deepcopy(gateway)
    interface["Properties"].update(VpcEndpointType="Interface")
    interface["Properties"].pop("RouteTableIds")
    prefix_route = {"Type": "AWS::EC2::Route", "Properties": {
        "RouteTableId": {"!Ref": "PrivateRt"}, "DestinationPrefixListId": "pl-x",
        "GatewayId": "vpce-x"}}
    joined = copy.deepcopy(gateway)
    joined["Properties"]["ServiceName"] = {"!Join": [".", ["com", "amazonaws", {"!Ref": "AWS::Region"}, "s3"]]}
    controls = [
        ("S3 endpoint built with !Join in 60-engines", with_resources({"Hidden": joined}, "60-engines.yaml")),
        ("S3 endpoint built with !Join in 00-network", with_resources({"Hidden": joined})),
        ("S3 gateway endpoint comes back", with_resources({"S3Endpoint": gateway})),
        ("S3 interface endpoint", with_resources({"S3If": interface})),
        ("prefix-list route", with_resources({"S3Route": prefix_route})),
    ]
    for name, t in controls:
        if check(t):
            print("ok    control: %s fails" % name)
        else:
            print("FAIL  control: %s passed - the check cannot see it" % name)
            failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
