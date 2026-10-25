#!/usr/bin/env python3
"""The egress event archive wiring in the ECS templates, checked offline (#2732).

    python3 deploy/local/cfn-egress-archive-test.py

## Why this exists

The CP archives raw egress events to S3 before it purges them (AF_EGRESS_ARCHIVE_URL, #2699).
On AWS three stacks have to agree, and every disagreement shows only on a real deployment: a
bucket the S3 endpoint does not admit, a CP role without the grant, or a URL with a prefix the
grant does not cover is AccessDenied on the first archive write, and the purger then keeps the
events. This fails when

  - 10-data's bucket is not optional (EgressArchive, empty by default), not retained, versioned,
    unencrypted, public, or has no expiry on egress/; or its output is exported or shares a
    name with a parameter (capture-env.sh copies an output over a parameter captured empty);
  - 20-platform's CP grant is not a managed policy behind the bucket parameter, or is more than
    PutObject / GetObject on <bucket>/egress/* and ListBucket on the bucket;
  - 30-ingress sets AF_EGRESS_ARCHIVE_URL other than s3://<bucket> behind the same switch;
  - the Go sink calls an S3 operation these grants do not cover, or the CP's keys stop starting
    with egress/ (the grant, the endpoint statement and the lifecycle rule are all on that prefix).

The endpoint statements are pinned by cfn-s3-endpoint-test.py. The positive controls at the end
run the same checks against broken copies.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import glob
import os
import re
import sys

try:
    import yaml
except ImportError:
    sys.exit("cfn-egress-archive-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CFN = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn")
CP = os.path.join(ROOT, "control-plane")
DATA, PLATFORM, INGRESS = "10-data.yaml", "20-platform.yaml", "30-ingress.yaml"
BUCKET_SUB = {"!Sub": "arn:${AWS::Partition}:s3:::${EgressArchiveBucket}"}


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


def check_data(t):
    fails = []
    params, res = t.get("Parameters", {}), t["Resources"]
    p = params.get("EgressArchive", {})
    if p.get("Default") != "" or p.get("AllowedValues") != ["", "create"]:
        fails.append("10-data: EgressArchive must default to empty with AllowedValues ['', create]")
    if t.get("Conditions", {}).get("CreateEgressArchive") != {"!Equals": [{"!Ref": "EgressArchive"}, "create"]}:
        fails.append("10-data: CreateEgressArchive is not EgressArchive == create")
    if params.get("EgressArchiveRetentionDays", {}).get("Type") != "Number":
        fails.append("10-data: no numeric EgressArchiveRetentionDays")
    b = res.get("EgressArchiveBucket")
    if not b or b.get("Type") != "AWS::S3::Bucket":
        return fails + ["10-data: no EgressArchiveBucket"]
    if b.get("Condition") != "CreateEgressArchive":
        fails.append("10-data: the bucket is not behind CreateEgressArchive (the archive must be opt-in)")
    if b.get("DeletionPolicy") != "Retain" or b.get("UpdateReplacePolicy") != "Retain":
        fails.append("10-data: the archive bucket is not Retain / Retain")
    bp = b.get("Properties", {})
    if bp.get("BucketName") != {"!Sub": "af-${AWS::StackName}-egress-archive-${AWS::AccountId}"}:
        fails.append("10-data: bucket name is not af-<stack>-egress-archive-<account> (00-network's endpoint pattern)")
    if "VersioningConfiguration" in bp:
        fails.append("10-data: the archive bucket is versioned (the CP replaces a day's object whole)")
    pab = bp.get("PublicAccessBlockConfiguration", {})
    if not all(pab.get(k) is True for k in ("BlockPublicAcls", "BlockPublicPolicy", "IgnorePublicAcls",
                                            "RestrictPublicBuckets")):
        fails.append("10-data: the archive bucket does not block all public access")
    sse = bp.get("BucketEncryption", {}).get("ServerSideEncryptionConfiguration", [])
    if not sse or "SSEAlgorithm" not in sse[0].get("ServerSideEncryptionByDefault", {}):
        fails.append("10-data: the archive bucket has no default encryption")
    rules = bp.get("LifecycleConfiguration", {}).get("Rules", [])
    exp = [r for r in rules if "ExpirationInDays" in r]
    if len(exp) != 1 or exp[0].get("Prefix") != "egress/" or exp[0].get("Status") != "Enabled" \
            or exp[0]["ExpirationInDays"] != {"!Ref": "EgressArchiveRetentionDays"}:
        fails.append("10-data: no enabled expiry rule on egress/ driven by EgressArchiveRetentionDays")
    if not any("AbortIncompleteMultipartUpload" in r and r.get("Status") == "Enabled" for r in rules):
        fails.append("10-data: no rule aborting incomplete multipart uploads")
    out = t.get("Outputs", {}).get("EgressArchiveBucket", {})
    if out.get("Condition") != "CreateEgressArchive" or out.get("Value") != {"!Ref": "EgressArchiveBucket"}:
        fails.append("10-data: output EgressArchiveBucket is missing or not the bucket behind the same condition")
    if "Export" in out:
        fails.append("10-data: EgressArchiveBucket is exported (it would pin the data stack; scripts pass it)")
    clash = set(t.get("Outputs", {})) & set(params)
    if clash:
        fails.append("10-data: output(s) %s share a parameter's name; capture-env.sh would copy the output "
                     "over a parameter captured empty" % sorted(clash))
    return fails


def check_platform(t):
    fails = []
    p = t.get("Parameters", {}).get("EgressArchiveBucket", {})
    if p.get("Default") != "" or not p.get("AllowedPattern"):
        fails.append("20-platform: EgressArchiveBucket must default to empty and carry an AllowedPattern")
    if t.get("Conditions", {}).get("HasEgressArchive") != {"!Not": [{"!Equals": [{"!Ref": "EgressArchiveBucket"}, ""]}]}:
        fails.append("20-platform: HasEgressArchive is not EgressArchiveBucket != empty")
    pol = t["Resources"].get("CpEgressArchivePolicy")
    if not pol or pol.get("Type") != "AWS::IAM::ManagedPolicy":
        return fails + ["20-platform: no managed policy CpEgressArchivePolicy"]
    if pol.get("Condition") != "HasEgressArchive":
        fails.append("20-platform: the CP archive grant is not behind HasEgressArchive")
    if pol["Properties"].get("Roles") != [{"!Ref": "CpTaskRole"}]:
        fails.append("20-platform: the archive grant is not attached to exactly the CP task role")
    stmts = pol["Properties"]["PolicyDocument"]["Statement"]
    objs = {"s3:PutObject", "s3:GetObject"}
    want_obj = {"!Sub": BUCKET_SUB["!Sub"] + "/egress/*"}
    seen = []
    for s in stmts:
        acts = set(s["Action"] if isinstance(s["Action"], list) else [s["Action"]])
        seen.append((s.get("Effect"), frozenset(acts), str(s.get("Resource")), "Condition" in s))
    want = [("Allow", frozenset(objs), str(want_obj), False),
            ("Allow", frozenset({"s3:ListBucket"}), str(BUCKET_SUB), False)]
    if sorted(seen) != sorted(want):
        fails.append("20-platform: archive grant is %s, want exactly PutObject+GetObject on <bucket>/egress/* "
                     "and ListBucket on <bucket>" % seen)
    # The grant stays out of the inline policy: its 10,240-character total is shared (#1576).
    inline = yaml.dump(t["Resources"]["CpTaskRole"]["Properties"].get("Policies", []))
    if "EgressArchiveBucket" in inline:
        fails.append("20-platform: the archive grant is in the CP role's inline policy")
    return fails


def check_ingress(t):
    fails = []
    p = t.get("Parameters", {}).get("EgressArchiveBucket", {})
    if p.get("Default") != "":
        fails.append("30-ingress: EgressArchiveBucket must default to empty (the archive stays off)")
    if t.get("Conditions", {}).get("HasEgressArchive") != {"!Not": [{"!Equals": [{"!Ref": "EgressArchiveBucket"}, ""]}]}:
        fails.append("30-ingress: HasEgressArchive is not EgressArchiveBucket != empty")
    found = []

    def walk(v):
        if isinstance(v, dict):
            if "!If" in v and isinstance(v["!If"], list) and len(v["!If"]) == 3 \
                    and isinstance(v["!If"][1], dict) and v["!If"][1].get("Name") == "AF_EGRESS_ARCHIVE_URL":
                found.append(v["!If"])
                return
            if v.get("Name") == "AF_EGRESS_ARCHIVE_URL":
                found.append(None)
            for x in v.values():
                walk(x)
        elif isinstance(v, list):
            for x in v:
                walk(x)
    walk(t.get("Resources", {}))
    if len(found) != 1 or found[0] is None:
        return fails + ["30-ingress: AF_EGRESS_ARCHIVE_URL is not set exactly once, behind an !If"]
    cond, val, none = found[0]
    if cond != "HasEgressArchive" or none != {"!Ref": "AWS::NoValue"}:
        fails.append("30-ingress: AF_EGRESS_ARCHIVE_URL is not behind HasEgressArchive / NoValue")
    if val.get("Value") != {"!Sub": "s3://${EgressArchiveBucket}"}:
        fails.append("30-ingress: AF_EGRESS_ARCHIVE_URL is %r, want s3://${EgressArchiveBucket} with no prefix "
                     "(the CP's keys start with egress/ already)" % (val.get("Value"),))
    return fails


def check_go():
    fails = []
    sink = open(os.path.join(CP, "egress_archive_sink.go")).read()
    ops = set(re.findall(r"api\.(\w+)\(", sink))
    if ops != {"PutObject", "HeadObject", "GetObject"}:
        fails.append("egress_archive_sink.go calls S3 operations %s; the grants cover PutObject, HeadObject "
                     "(GetObject) and GetObject only" % sorted(ops))
    arch = open(os.path.join(CP, "egress_archive.go")).read()
    keys = re.findall(r'Sprintf\("(egress/[^"]*)"', arch)
    if len(keys) < 2:
        fails.append("egress_archive.go: fewer than two archive keys starting with egress/")
    return fails


def check(templates):
    return (check_data(templates[DATA]) + check_platform(templates[PLATFORM])
            + check_ingress(templates[INGRESS]) + check_go())


def main():
    try:
        templates = load_all()
        for n in (DATA, PLATFORM, INGRESS):
            if n not in templates:
                raise OSError("%s not found" % n)
        check_go()
    except (OSError, yaml.YAMLError) as e:
        print("cfn-egress-archive-test: cannot run: %s" % e, file=sys.stderr)
        return 2
    fails = check(templates)
    for f in fails:
        print("FAIL  " + f)
    failed = bool(fails)
    if not fails:
        print("ok    optional archive bucket, CP grant and AF_EGRESS_ARCHIVE_URL agree in 3 templates")

    def mutated(fn, template):
        t = copy.deepcopy(templates)
        fn(t[template])
        return t

    def bucket(t):
        return t["Resources"]["EgressArchiveBucket"]

    def stmts(t):
        return t["Resources"]["CpEgressArchivePolicy"]["Properties"]["PolicyDocument"]["Statement"]

    def env_if(t):
        out = []

        def walk(v):
            if isinstance(v, dict):
                if "!If" in v and isinstance(v["!If"], list) and isinstance(v["!If"][1], dict) \
                        and v["!If"][1].get("Name") == "AF_EGRESS_ARCHIVE_URL":
                    out.append(v["!If"])
                for x in v.values():
                    walk(x)
            elif isinstance(v, list):
                for x in v:
                    walk(x)
        walk(t["Resources"])
        return out[0]

    def drop_env(t):
        def walk(v):
            if isinstance(v, list):
                v[:] = [x for x in v if not (isinstance(x, dict) and "!If" in x and isinstance(x["!If"], list)
                                              and isinstance(x["!If"][1], dict)
                                              and x["!If"][1].get("Name") == "AF_EGRESS_ARCHIVE_URL")]
                for x in v:
                    walk(x)
            elif isinstance(v, dict):
                for x in v.values():
                    walk(x)
        walk(t["Resources"])

    controls = [
        ("bucket unconditional", mutated(lambda t: bucket(t).pop("Condition"), DATA)),
        ("bucket not retained", mutated(lambda t: bucket(t).update(DeletionPolicy="Delete"), DATA)),
        ("bucket replacement not retained", mutated(lambda t: bucket(t).pop("UpdateReplacePolicy"), DATA)),
        ("bucket renamed", mutated(lambda t: bucket(t)["Properties"].update(BucketName="archive"), DATA)),
        ("bucket versioned", mutated(lambda t: bucket(t)["Properties"].update(
            VersioningConfiguration={"Status": "Enabled"}), DATA)),
        ("bucket public", mutated(lambda t: bucket(t)["Properties"]["PublicAccessBlockConfiguration"].update(
            BlockPublicPolicy=False), DATA)),
        ("bucket unencrypted", mutated(lambda t: bucket(t)["Properties"].pop("BucketEncryption"), DATA)),
        ("no expiry", mutated(lambda t: bucket(t)["Properties"]["LifecycleConfiguration"].update(
            Rules=[bucket(t)["Properties"]["LifecycleConfiguration"]["Rules"][1]]), DATA)),
        ("expiry off the egress/ prefix", mutated(lambda t: bucket(t)["Properties"]["LifecycleConfiguration"][
            "Rules"][0].update(Prefix=""), DATA)),
        ("archive on by default", mutated(lambda t: t["Parameters"]["EgressArchive"].update(Default="create"), DATA)),
        ("output exported", mutated(lambda t: t["Outputs"]["EgressArchiveBucket"].update(
            Export={"Name": "x"}), DATA)),
        ("output shares a parameter's name", mutated(lambda t: t["Parameters"].update(
            EgressArchiveBucket={"Type": "String", "Default": ""}), DATA)),
        ("grant adds DeleteObject", mutated(lambda t: stmts(t)[0]["Action"].append("s3:DeleteObject"), PLATFORM)),
        ("grant drops GetObject", mutated(lambda t: stmts(t)[0].update(Action=["s3:PutObject"]), PLATFORM)),
        ("grant widened to the whole bucket", mutated(lambda t: stmts(t)[0].update(
            Resource={"!Sub": "arn:${AWS::Partition}:s3:::${EgressArchiveBucket}/*"}), PLATFORM)),
        ("grant on any bucket", mutated(lambda t: stmts(t)[0].update(Resource="arn:aws:s3:::*/egress/*"), PLATFORM)),
        ("list dropped", mutated(lambda t: stmts(t).pop(1), PLATFORM)),
        ("list gains a prefix condition", mutated(lambda t: stmts(t)[1].update(
            Condition={"StringLike": {"s3:prefix": ["egress/*"]}}), PLATFORM)),
        ("grant unconditional", mutated(lambda t: t["Resources"]["CpEgressArchivePolicy"].pop("Condition"), PLATFORM)),
        ("grant on another role", mutated(lambda t: t["Resources"]["CpEgressArchivePolicy"]["Properties"].update(
            Roles=[{"!Ref": "WsTaskRole"}]), PLATFORM)),
        ("grant removed", mutated(lambda t: t["Resources"].pop("CpEgressArchivePolicy"), PLATFORM)),
        ("platform parameter defaults on", mutated(lambda t: t["Parameters"]["EgressArchiveBucket"].update(
            Default="af-x-egress-archive-123456789012"), PLATFORM)),
        ("URL carries a prefix", mutated(lambda t: env_if(t)[1].update(
            Value={"!Sub": "s3://${EgressArchiveBucket}/egress"}), INGRESS)),
        ("URL unconditional", mutated(lambda t: env_if(t).__setitem__(0, "HasEgressGw"), INGRESS)),
        ("URL not wired", mutated(drop_env, INGRESS)),
        ("ingress parameter defaults on", mutated(lambda t: t["Parameters"]["EgressArchiveBucket"].update(
            Default="b"), INGRESS)),
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
