#!/usr/bin/env python3
"""The S3 gateway endpoint and the workspace route table, checked offline.

    python3 deploy/local/cfn-s3-endpoint-test.py

## Why this exists

An S3 gateway endpoint lets whatever sits on its route table reach S3 without crossing the
NAT gateway (and later the egress gateway of ADR 0122), unrecorded (#2657). A policy narrows
it but does not reroute what it refuses: the prefix-list route beats 0.0.0.0/0, so the refusal
is AccessDenied. So the endpoint may sit only where nothing a user drives runs: the private
route table (CP, ecs-ec2 slot hosts pulling images, engines, the home task). Workspace task
ENIs have their own subnets and route table with no endpoint route (#2716). This fails when

  - the templates declare an S3 VPC endpoint other than 00-network's S3Endpoint (or one whose
    ServiceName is built from parts, which cannot be told from S3), or any prefix-list route;
  - S3Endpoint is not a gateway endpoint on exactly the private route table, or is not behind
    S3EndpointEnabled (default "true"; "false" is the migration step and the rollback);
  - its policy is missing, or differs from the pinned statements below (a new bucket is a
    decision, made here and in the template), or its models-bucket pattern stops matching the
    bucket 60-engines creates;
  - the workspace route table routes anything but 0.0.0.0/0 to the NAT, or its subnets are
    not exactly the workspace subnets, or a workspace subnet is on another route table;
  - a workspace subnet's default CIDR overlaps another subnet or leaves VpcCidr;
  - 00-network stops exporting WorkspaceSubnets, or 30-ingress stops handing the
    WorkspaceSubnets parameter to the CP as AF_ECS_WORKSPACE_SUBNETS.

The positive controls at the end run the same checks against broken copies, to show each can
fail at all.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import fnmatch
import glob
import ipaddress
import os
import re
import sys

try:
    import yaml
except ImportError:
    sys.exit("cfn-s3-endpoint-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CFN = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn")
NET, INGRESS, ENGINES = "00-network.yaml", "30-ingress.yaml", "60-engines.yaml"

# What each statement may admit: Sid -> (actions, resources, owner condition required).
# Resources are spelled with the CloudFormation pseudo parameters left in. The AWS-owned
# buckets take no owner condition; the models bucket must, because its name is not proof of
# ownership (anyone can create af-x-models-<this account id> elsewhere).
OWNER = {"StringEquals": {"s3:ResourceAccount": {"!Ref": "AWS::AccountId"}}}
MODELS = "arn:${AWS::Partition}:s3:::af-*-models-${AWS::AccountId}"
EXPECTED = {
    "EcrLayerBucket": ({"s3:GetObject"},
                       {"arn:${AWS::Partition}:s3:::prod-${AWS::Region}-starport-layer-bucket/*"}, None),
    "Al2023Repos": ({"s3:GetObject"},
                    {"arn:${AWS::Partition}:s3:::al2023-repos-${AWS::Region}-de612dc2/*"}, None),
    "SsmAgentUpdate": ({"s3:GetObject"},
                       {"arn:${AWS::Partition}:s3:::amazon-ssm-${AWS::Region}/*",
                        "arn:${AWS::Partition}:s3:::aws-ssm-${AWS::Region}/*"}, None),
    "EcsAgentDownload": ({"s3:GetObject"},
                         {"arn:${AWS::Partition}:s3:::amazon-ecs-agent/*",
                          "arn:${AWS::Partition}:s3:::amazon-ecs-agent-${AWS::Region}/*"}, None),
    "ModelsObjects": ({"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload",
                       "s3:ListMultipartUploadParts", "s3:GetObjectTagging", "s3:PutObjectTagging"},
                      {MODELS + "/*"}, OWNER),
    "ModelsList": ({"s3:ListBucket", "s3:ListBucketMultipartUploads"}, {MODELS}, OWNER),
}
WS_SUBNETS = {"WorkspaceSubnet0", "WorkspaceSubnet1"}
PRIVATE_SUBNETS = {"PrivateSubnet0", "PrivateSubnet1"}


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


def as_list(v):
    return v if isinstance(v, list) else [v]


def ref(v):
    return v.get("!Ref") if isinstance(v, dict) else None


def is_s3_or_unknown(service):
    """True for an S3 service name, or one this check cannot resolve (fail safe).

    A literal or a !Sub string is read as text once ${AWS::Region} and ${AWS::URLSuffix} are
    set aside; a remaining substitution makes it unknown. Any service name starting with "s3"
    counts (s3, s3express, s3-outposts). Anything else (!Join, !FindInMap, ...) builds the name
    from parts, so it is refused rather than guessed.
    """
    if isinstance(service, str):
        text = service
    elif isinstance(service, dict) and list(service) == ["!Sub"] and isinstance(service["!Sub"], str):
        text = service["!Sub"]
        for known in ("${AWS::Region}", "${AWS::URLSuffix}"):
            text = text.replace(known, "")
        if "${" in text:
            return True
    else:
        return True
    return any(tok.startswith("s3") for tok in text.lower().replace("-", ".").split("."))


def check_policy(props, engines):
    fails = []
    doc = props.get("PolicyDocument")
    if not doc:
        return ["S3Endpoint has no PolicyDocument (full access)"]
    resources = set()
    seen = set()
    for st in doc.get("Statement", []):
        sid = st.get("Sid", "?")
        seen.add(sid)
        if st.get("Effect") != "Allow":
            fails.append("%s: not an Allow" % sid)
        if st.get("Principal") != "*":
            fails.append("%s: Principal is %r, not \"*\" (gateway endpoints carry no principal)"
                         % (sid, st.get("Principal")))
        actions = set(as_list(st.get("Action", [])))
        for a in actions:
            if a.endswith("*"):
                fails.append("%s: wildcard action %s" % (sid, a))
        rs = set()
        for r in as_list(st.get("Resource", [])):
            r = r.get("!Sub", r) if isinstance(r, dict) else r
            rs.add(r)
            if r == "*" or re.search(r":s3:::\*", r):
                fails.append("%s: Resource %s names no concrete bucket" % (sid, r))
        resources |= rs
        if sid not in EXPECTED:
            fails.append("%s: statement is not in the pinned list" % sid)
            continue
        want_actions, want_res, want_cond = EXPECTED[sid]
        if actions != want_actions:
            fails.append("%s: actions %s, want %s" % (sid, sorted(actions), sorted(want_actions)))
        if rs != want_res:
            fails.append("%s: resources %s, want %s" % (sid, sorted(rs), sorted(want_res)))
        if st.get("Condition") != want_cond:
            fails.append("%s: condition %r, want %r" % (sid, st.get("Condition"), want_cond))
    for sid in sorted(set(EXPECTED) - seen):
        fails.append("%s: pinned statement is missing" % sid)

    # The models bucket 60-engines creates must match the endpoint's wildcard.
    name = engines["Resources"]["ModelsBucket"]["Properties"]["BucketName"]["!Sub"]
    sample = name.replace("${AWS::StackName}", "af-ecs-engines").replace("${AWS::AccountId}", "123456789012")
    pat = [r for r in resources if "-models-" in r and r.endswith("/*")]
    pat = pat[0].split(":s3:::")[1][:-2].replace("${AWS::AccountId}", "123456789012") if pat else ""
    if not fnmatch.fnmatchcase(sample, pat):
        fails.append("models bucket %r does not match the endpoint pattern %r" % (sample, pat))
    return fails


def check_routes(net):
    fails = []
    res = net["Resources"]
    if res.get("WorkspaceRt", {}).get("Type") != "AWS::EC2::RouteTable":
        return ["00-network: no WorkspaceRt route table"]
    # Routes on the workspace route table: the default route to the NAT, nothing else.
    for rid, r in res.items():
        if r.get("Type") != "AWS::EC2::Route":
            continue
        p = r.get("Properties") or {}
        if ref(p.get("RouteTableId")) != "WorkspaceRt":
            continue
        if p.get("DestinationCidrBlock") != "0.0.0.0/0" or ref(p.get("NatGatewayId")) != "Nat" \
                or set(p) - {"RouteTableId", "DestinationCidrBlock", "NatGatewayId"}:
            fails.append("00-network: %s routes something other than 0.0.0.0/0 to the NAT on WorkspaceRt" % rid)
    defaults = [rid for rid, r in res.items() if r.get("Type") == "AWS::EC2::Route"
                and ref((r.get("Properties") or {}).get("RouteTableId")) == "WorkspaceRt"]
    if not defaults:
        fails.append("00-network: WorkspaceRt has no default route to the NAT")
    # Subnet associations: each workspace subnet on WorkspaceRt only, nothing else on it.
    on = {}
    for rid, r in res.items():
        if r.get("Type") == "AWS::EC2::SubnetRouteTableAssociation":
            p = r.get("Properties") or {}
            on.setdefault(ref(p.get("SubnetId")), set()).add(ref(p.get("RouteTableId")))
    ws_on_rt = {s for s, rts in on.items() if "WorkspaceRt" in rts}
    if ws_on_rt != WS_SUBNETS:
        fails.append("00-network: WorkspaceRt carries %s, want exactly %s" % (sorted(ws_on_rt), sorted(WS_SUBNETS)))
    for s in WS_SUBNETS:
        if on.get(s, set()) - {"WorkspaceRt"}:
            fails.append("00-network: %s is also on %s" % (s, sorted(on[s] - {"WorkspaceRt"})))
    for s in PRIVATE_SUBNETS:
        if on.get(s) != {"PrivateRt"}:
            fails.append("00-network: %s is on %s, want PrivateRt" % (s, sorted(on.get(s, set()))))
    return fails


def check_cidrs(net):
    fails = []
    params, res = net.get("Parameters", {}), net["Resources"]
    vpc = ipaddress.ip_network(params["VpcCidr"]["Default"])
    nets = {}
    for rid, r in res.items():
        if r.get("Type") != "AWS::EC2::Subnet":
            continue
        c = r["Properties"]["CidrBlock"]
        if ref(c):
            c = params[ref(c)]["Default"]
        elif isinstance(c, dict) and "!FindInMap" in c:
            m, k, a = c["!FindInMap"]
            c = net["Mappings"][m][k][a]
        nets[rid] = ipaddress.ip_network(c)
    for s in WS_SUBNETS:
        if s not in nets:
            fails.append("00-network: no subnet %s" % s)
            continue
        if not nets[s].subnet_of(vpc):
            fails.append("00-network: %s default %s is outside VpcCidr %s" % (s, nets[s], vpc))
        for o, n in nets.items():
            if o != s and nets[s].overlaps(n):
                fails.append("00-network: %s default %s overlaps %s (%s)" % (s, nets[s], o, n))
    return fails


def check_wiring(net, ingress):
    fails = []
    out = net.get("Outputs", {}).get("WorkspaceSubnets", {})
    want = {"!Join": [",", [{"!Ref": "WorkspaceSubnet0"}, {"!Ref": "WorkspaceSubnet1"}]]}
    if out.get("Value") != want or not out.get("Export"):
        fails.append("00-network: WorkspaceSubnets is not exported as both workspace subnets")
    if "WorkspaceSubnets" not in ingress.get("Parameters", {}):
        fails.append("30-ingress: no WorkspaceSubnets parameter")
    found = False

    def walk(v):
        nonlocal found
        if isinstance(v, dict):
            if v.get("Name") == "AF_ECS_WORKSPACE_SUBNETS" and ref(v.get("Value")) == "WorkspaceSubnets":
                found = True
            for x in v.values():
                walk(x)
        elif isinstance(v, list):
            for x in v:
                walk(x)
    walk(ingress.get("Resources", {}))
    if not found:
        fails.append("30-ingress: AF_ECS_WORKSPACE_SUBNETS is not the WorkspaceSubnets parameter")
    return fails


def check(templates):
    fails = []
    net = templates[NET]
    for tname, t in templates.items():
        for rname, r in (t.get("Resources") or {}).items():
            props = r.get("Properties") or {}
            if r.get("Type") == "AWS::EC2::VPCEndpoint" and is_s3_or_unknown(props.get("ServiceName")) \
                    and (tname, rname) != (NET, "S3Endpoint"):
                fails.append("%s: %s is another S3 VPC endpoint (or its service name is not a plain string)"
                             % (tname, rname))
            if r.get("Type") == "AWS::EC2::Route" and "DestinationPrefixListId" in props:
                fails.append("%s: %s routes a prefix list" % (tname, rname))
    ep = net["Resources"].get("S3Endpoint")
    if not ep or ep.get("Type") != "AWS::EC2::VPCEndpoint":
        fails.append("00-network: no S3Endpoint")
    else:
        p = ep.get("Properties") or {}
        if p.get("ServiceName") != {"!Sub": "com.amazonaws.${AWS::Region}.s3"}:
            fails.append("00-network: S3Endpoint service is %r" % (p.get("ServiceName"),))
        if p.get("VpcEndpointType") != "Gateway":
            fails.append("00-network: S3Endpoint is not a gateway endpoint")
        if p.get("RouteTableIds") != [{"!Ref": "PrivateRt"}]:
            fails.append("00-network: S3Endpoint is on %r, want exactly [PrivateRt]" % (p.get("RouteTableIds"),))
        # The one switch is S3EndpointEnabled, on by default: off is the migration step for a
        # running stack and the rollback, never a silent state.
        if ep.get("Condition") != "S3EndpointOn":
            fails.append("00-network: S3Endpoint is not behind S3EndpointOn (%r)" % (ep.get("Condition"),))
        if net.get("Conditions", {}).get("S3EndpointOn") != {"!Equals": [{"!Ref": "S3EndpointEnabled"}, "true"]}:
            fails.append("00-network: S3EndpointOn is not exactly S3EndpointEnabled == \"true\"")
        if net.get("Parameters", {}).get("S3EndpointEnabled", {}).get("Default") != "true":
            fails.append("00-network: S3EndpointEnabled does not default to \"true\"")
        fails += check_policy(p, templates[ENGINES])
    fails += check_routes(net)
    fails += check_cidrs(net)
    fails += check_wiring(net, templates[INGRESS])
    return fails


def main():
    try:
        templates = load_all()
        for n in (NET, INGRESS, ENGINES):
            if n not in templates:
                raise OSError("%s not found" % n)
    except (OSError, yaml.YAMLError) as e:
        print("cfn-s3-endpoint-test: cannot run: %s" % e, file=sys.stderr)
        return 2
    fails = check(templates)
    for f in fails:
        print("FAIL  " + f)
    failed = bool(fails)
    if not fails:
        print("ok    S3 endpoint on PrivateRt only, pinned policy, WorkspaceRt without it, in %d templates"
              % len(templates))

    def mutated(fn, template=NET):
        t = copy.deepcopy(templates)
        fn(t[template])
        return t

    def res(t):
        return t["Resources"]

    def ep(t):
        return res(t)["S3Endpoint"]["Properties"]

    def stmts(t):
        return ep(t)["PolicyDocument"]["Statement"]

    def swap_resources(t):
        s = stmts(t)
        s[4]["Resource"], s[5]["Resource"] = s[5]["Resource"], s[4]["Resource"]

    gateway = {"Type": "AWS::EC2::VPCEndpoint", "Properties": {
        "VpcId": {"!Ref": "Vpc"}, "ServiceName": {"!Sub": "com.amazonaws.${AWS::Region}.s3"},
        "VpcEndpointType": "Gateway", "RouteTableIds": [{"!Ref": "WorkspaceRt"}]}}
    joined = copy.deepcopy(gateway)
    joined["Properties"]["ServiceName"] = {"!Join": [".", ["com", "amazonaws", {"!Ref": "AWS::Region"}, "s3"]]}
    express = copy.deepcopy(gateway)
    express["Properties"]["ServiceName"] = {"!Sub": "com.amazonaws.${AWS::Region}.s3express"}
    ecr = copy.deepcopy(gateway)
    ecr["Properties"].update(VpcEndpointType="Interface", ServiceName={"!Sub": "com.amazonaws.${AWS::Region}.ecr.api"})
    ecr["Properties"].pop("RouteTableIds")
    prefix_route = {"Type": "AWS::EC2::Route", "Properties": {
        "RouteTableId": {"!Ref": "WorkspaceRt"}, "DestinationPrefixListId": "pl-x", "GatewayId": "vpce-x"}}
    extra_ws_route = {"Type": "AWS::EC2::Route", "Properties": {
        "RouteTableId": {"!Ref": "WorkspaceRt"}, "DestinationCidrBlock": "10.99.0.0/16", "NatGatewayId": {"!Ref": "Nat"}}}
    priv_on_ws = {"Type": "AWS::EC2::SubnetRouteTableAssociation",
                  "Properties": {"SubnetId": {"!Ref": "PrivateSubnet0"}, "RouteTableId": {"!Ref": "WorkspaceRt"}}}

    def ws_assoc_to_private(t):
        res(t)["WorkspaceAssoc0"]["Properties"]["RouteTableId"] = {"!Ref": "PrivateRt"}

    def drop_ws_default(t):
        del res(t)["WorkspaceDefaultRoute"]

    def overlap(t):
        t["Parameters"]["WorkspaceSubnet1Cidr"]["Default"] = "10.20.11.0/24"

    def outside(t):
        t["Parameters"]["WorkspaceSubnet0Cidr"]["Default"] = "10.21.0.0/20"

    def ingress_unwired(t):
        env = res(t)["TaskDef"]["Properties"]["ContainerDefinitions"][0]["Environment"]
        for i, e in enumerate(env):
            if isinstance(e, dict) and "!If" in e and e["!If"][1].get("Name") == "AF_ECS_WORKSPACE_SUBNETS":
                env.pop(i)
                return
        raise AssertionError("control could not find AF_ECS_WORKSPACE_SUBNETS")

    controls = [
        ("endpoint on WorkspaceRt too", mutated(lambda t: ep(t)["RouteTableIds"].append({"!Ref": "WorkspaceRt"}))),
        ("endpoint moved to WorkspaceRt", mutated(lambda t: ep(t).update(RouteTableIds=[{"!Ref": "WorkspaceRt"}]))),
        ("endpoint removed", mutated(lambda t: res(t).pop("S3Endpoint"))),
        ("endpoint becomes Interface", mutated(lambda t: ep(t).update(VpcEndpointType="Interface"))),
        ("second S3 endpoint", mutated(lambda t: res(t).update(Hidden=gateway))),
        ("S3 Express endpoint", mutated(lambda t: res(t).update(Hidden=express))),
        ("S3 endpoint built with !Join in 60-engines", mutated(lambda t: res(t).update(Hidden=joined), ENGINES)),
        ("prefix-list route on WorkspaceRt", mutated(lambda t: res(t).update(S3Route=prefix_route))),
        ("extra route on WorkspaceRt", mutated(lambda t: res(t).update(Extra=extra_ws_route))),
        ("no default route on WorkspaceRt", mutated(drop_ws_default)),
        ("private subnet on WorkspaceRt", mutated(lambda t: res(t).update(Extra=priv_on_ws))),
        ("workspace subnet on PrivateRt", mutated(ws_assoc_to_private)),
        ("workspace CIDR overlaps a private subnet", mutated(overlap)),
        ("workspace CIDR outside the VPC", mutated(outside)),
        ("WorkspaceSubnets not exported", mutated(lambda t: t["Outputs"].pop("WorkspaceSubnets"))),
        ("30-ingress does not pass AF_ECS_WORKSPACE_SUBNETS", mutated(ingress_unwired, INGRESS)),
        ("policy removed", mutated(lambda t: ep(t).pop("PolicyDocument"))),
        ("resource widened to *", mutated(lambda t: stmts(t)[0].update(Resource="*"))),
        ("any bucket", mutated(lambda t: stmts(t)[0].update(Resource="arn:aws:s3:::*/*"))),
        ("s3:* granted", mutated(lambda t: stmts(t)[0].update(Action="s3:*"))),
        ("deny statement", mutated(lambda t: stmts(t)[0].update(Effect="Deny"))),
        ("extra bucket", mutated(lambda t: stmts(t).append(
            {"Effect": "Allow", "Action": ["s3:GetObject"], "Resource": "arn:aws:s3:::other/*"}))),
        ("ECR bucket dropped", mutated(lambda t: stmts(t).pop(0))),
        ("models resources swapped", mutated(swap_resources)),
        ("principal dropped", mutated(lambda t: stmts(t)[0].pop("Principal"))),
        ("owner condition dropped (objects)", mutated(lambda t: stmts(t)[4].pop("Condition"))),
        ("owner condition dropped (list)", mutated(lambda t: stmts(t)[5].pop("Condition"))),
        ("SSM agent buckets dropped", mutated(lambda t: stmts(t).pop(2))),
        ("ECS agent buckets dropped", mutated(lambda t: stmts(t).pop(3))),
        ("ECS agent statement loses the partition bucket", mutated(lambda t: stmts(t)[3].update(Resource=stmts(t)[3]["Resource"][1:]))),
        ("ECS agent statement loses the regional bucket", mutated(lambda t: stmts(t)[3].update(Resource=stmts(t)[3]["Resource"][:1]))),
        ("SSM agent statement narrowed to one bucket", mutated(lambda t: stmts(t)[2].update(Resource=stmts(t)[2]["Resource"][:1]))),
        ("endpoint unconditional", mutated(lambda t: res(t)["S3Endpoint"].pop("Condition"))),
        ("endpoint off by default", mutated(lambda t: t["Parameters"]["S3EndpointEnabled"].update(Default="false"))),
        ("S3EndpointOn inverted", mutated(lambda t: t["Conditions"].update(
            S3EndpointOn={"!Equals": [{"!Ref": "S3EndpointEnabled"}, "false"]}))),
        ("owner condition on an AWS-owned bucket", mutated(lambda t: stmts(t)[0].update(Condition=OWNER))),
        ("models bucket renamed", mutated(lambda t: res(t)["ModelsBucket"]["Properties"]["BucketName"].update(
            {"!Sub": "models-${AWS::StackName}"}), ENGINES)),
    ]
    if check(mutated(lambda t: res(t).update(Ecr=ecr))):
        print("FAIL  an ECR interface endpoint is refused - the check is too broad")
        failed = True
    else:
        print("ok    control: an ECR interface endpoint is not refused")
    for name, t in controls:
        if check(t):
            print("ok    control: %s fails" % name)
        else:
            print("FAIL  control: %s passed - the check cannot see it" % name)
            failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
