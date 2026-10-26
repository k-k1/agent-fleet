#!/usr/bin/env python3
"""00-network from v0.34.x to this tree: the change sets the README promises, checked offline.

    python3 deploy/local/cfn-network-upgrade-test.py

## Why this exists

v0.34.x's 00-network has an S3 gateway endpoint with no policy and no workspace subnets. This
tree's endpoint has a policy that refuses (AccessDenied, no NAT fallback) the S3 of anything it
does not list, so it must stay off until every workspace has been restarted into a workspace
subnet. A rehearsal on a real stack (#2763) found the README describing a change set nobody
gets ("Add only, six resources") and nothing warning that a deploy with the defaults switches
the restricted endpoint on at once. The README's "Upgrading from v0.34.x" block now names the
shapes; this pins them to the two templates, so changing either one makes the text wrong here
first. It fails when

  - the step-1 deploy (S3EndpointEnabled=false) is not exactly Add of the six workspace
    resources plus Remove S3Endpoint, with nothing else modified;
  - a deploy with no overrides stops being "Add the six, Modify S3Endpoint" (the trap the README
    warns about), or S3EndpointEnabled stops defaulting to "true";
  - switching the endpoint on afterwards is anything but Add S3Endpoint, or off again anything
    but Remove S3Endpoint;
  - the template stops needing CAPABILITY_IAM, or the downgrade stops losing GatewayEip*;
  - the README block stops naming any of the above.

The v0.34.1 template is a frozen copy (testdata/00-network.v0.34.1.yaml): CI checks out one
commit, without tags. When the tag is present the copy is compared with it byte for byte.

The "change set" here is a static diff: a resource's presence is its Condition evaluated with
the given parameters, and Modify means its Properties differ once !If is resolved. That is what
CloudFormation compares for these resources (no transforms, no dynamic references).

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import os
import subprocess
import sys

try:
    import yaml
except ImportError:
    sys.exit("cfn-network-upgrade-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
NEW = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn", "00-network.yaml")
OLD = os.path.join(ROOT, "deploy", "local", "testdata", "00-network.v0.34.1.yaml")
README = os.path.join(ROOT, "deploy", "aws", "ecs", "README.md")
TAG, TAG_PATH = "v0.34.1", "deploy/aws/ecs/cfn/00-network.yaml"

SIX = {"WorkspaceSubnet0", "WorkspaceSubnet1", "WorkspaceRt", "WorkspaceDefaultRoute",
       "WorkspaceAssoc0", "WorkspaceAssoc1"}
BLOCK_HEADING = "Upgrading from v0.34.x"


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


def load(path):
    with open(path) as f:
        return yaml.load(f, Loader=CfnLoader)


def params(doc, overrides):
    out = {k: str(v.get("Default", "")) for k, v in doc.get("Parameters", {}).items()}
    for k, v in overrides.items():
        if k in out:
            out[k] = v
    return out


def evaluate(node, p, conds, doc):
    if isinstance(node, dict) and len(node) == 1:
        (k, v), = node.items()
        if k in ("!Equals", "Fn::Equals"):
            a, b = (evaluate(x, p, conds, doc) for x in v)
            return a == b
        if k in ("!Not", "Fn::Not"):
            return not evaluate(v[0], p, conds, doc)
        if k in ("!And", "Fn::And"):
            return all(evaluate(x, p, conds, doc) for x in v)
        if k in ("!Or", "Fn::Or"):
            return any(evaluate(x, p, conds, doc) for x in v)
        if k in ("!Condition", "Condition"):
            return condition(v, p, conds, doc)
        if k in ("!Ref", "Ref") and v in p:
            return p[v]
    return node


def condition(name, p, conds, doc):
    if name not in conds:
        conds[name] = evaluate(doc["Conditions"][name], p, conds, doc)
    return conds[name]


def resolve_ifs(node, p, conds, doc):
    """Properties with every !If decided; an AWS::NoValue branch removes the key or item."""
    nov = {"!Ref": "AWS::NoValue"}
    if isinstance(node, dict):
        if len(node) == 1 and next(iter(node)) in ("!If", "Fn::If"):
            c, a, b = next(iter(node.values()))
            return resolve_ifs(a if condition(c, p, conds, doc) else b, p, conds, doc)
        out = {}
        for k, v in node.items():
            r = resolve_ifs(v, p, conds, doc)
            if r != nov:
                out[k] = r
        return out
    if isinstance(node, list):
        return [r for r in (resolve_ifs(x, p, conds, doc) for x in node) if r != nov]
    return node


def stack(doc, overrides):
    """{logical id: (type, resolved properties)} of what a stack with these parameters holds."""
    p, conds, out = params(doc, overrides), {}, {}
    for lid, res in doc["Resources"].items():
        if "Condition" in res and not condition(res["Condition"], p, conds, doc):
            continue
        out[lid] = (res["Type"], resolve_ifs(res.get("Properties", {}), p, conds, doc))
    return out


def change_set(before, after):
    common = set(before) & set(after)
    return {
        "Add": set(after) - set(before),
        "Remove": set(before) - set(after),
        "Modify": {lid for lid in common if before[lid] != after[lid]},
    }


def shape(cs):
    return " ".join(f"{k} {sorted(v)}" for k, v in cs.items() if v) or "no changes"


def expect(errors, what, cs, add=(), remove=(), modify=()):
    want = {"Add": set(add), "Remove": set(remove), "Modify": set(modify)}
    if cs != want:
        errors.append(f"{what}: expected {shape(want)}, the templates give {shape(cs)}")


def readme_block(text):
    start = text.find(BLOCK_HEADING)
    if start < 0:
        return ""
    end = text.find("\n## ", start)
    nxt = text.find("\n### ", start + 1)
    if 0 <= nxt and (end < 0 or nxt < end):
        end = nxt
    return text[start:end if end >= 0 else len(text)]


def check(old, new, readme):
    errors = []
    v034 = stack(old, {})
    default = new["Parameters"].get("S3EndpointEnabled", {}).get("Default")
    if str(default) != "true":
        errors.append(f"S3EndpointEnabled defaults to {default!r}; the README says a deploy with "
                      "the defaults switches the restricted endpoint on (\"true\")")
    off, on = stack(new, {"S3EndpointEnabled": "false"}), stack(new, {})
    expect(errors, "step 1 (v0.34.x -> S3EndpointEnabled=false)", change_set(v034, off),
           add=SIX, remove={"S3Endpoint"})
    expect(errors, "a deploy with the defaults (v0.34.x -> S3EndpointEnabled=true)",
           change_set(v034, on), add=SIX, modify={"S3Endpoint"})
    expect(errors, "step 5 (S3EndpointEnabled=false -> true)", change_set(off, on), add={"S3Endpoint"})
    expect(errors, "rollback (S3EndpointEnabled=true -> false)", change_set(on, off), remove={"S3Endpoint"})
    expect(errors, "downgrade to v0.34.x (from S3EndpointEnabled=true)", change_set(on, v034),
           remove=SIX, modify={"S3Endpoint"})
    if not any(r["Type"].startswith("AWS::IAM::") for r in new["Resources"].values()):
        errors.append("00-network declares no IAM resource any more: the README's CAPABILITY_IAM "
                      "(and the release note saying it is now required) is wrong")
    if any(r["Type"].startswith("AWS::IAM::") for r in old["Resources"].values()):
        errors.append("the v0.34.1 copy declares IAM resources; it is not the v0.34.1 template")
    lost = {"GatewayEipAllocationId", "GatewayEipAddress"}
    if not lost <= set(new["Parameters"]) or lost & set(old["Parameters"]):
        errors.append("GatewayEip* is no longer a parameter only this tree has; the README's "
                      "downgrade note (they are lost) is wrong")

    block = readme_block(readme)
    if not block:
        errors.append(f'README: no "{BLOCK_HEADING}" block')
    else:
        for needle in sorted(SIX) + ["*Remove* `S3Endpoint`", "*Modify* `S3Endpoint`",
                                     "S3EndpointEnabled=false", "S3EndpointEnabled=true",
                                     "CAPABILITY_IAM", "--no-execute-changeset", "update.sh",
                                     "GatewayEipAllocationId"]:
            if needle not in block:
                errors.append(f'README "{BLOCK_HEADING}": does not mention {needle}')
    if "must be *Add* only" in readme:
        errors.append('README still says the step-1 change set "must be *Add* only"')
    return errors


def tag_copy_matches():
    """None when the tag cannot be read (shallow CI checkout), else whether the copy is exact."""
    try:
        blob = subprocess.run(["git", "-C", ROOT, "show", f"{TAG}:{TAG_PATH}"],
                              capture_output=True, check=True).stdout
    except (OSError, subprocess.CalledProcessError):
        return None
    with open(OLD, "rb") as f:
        return f.read() == blob


def main():
    try:
        old, new = load(OLD), load(NEW)
        with open(README) as f:
            readme = f.read()
    except (OSError, yaml.YAMLError) as e:
        print(f"cfn-network-upgrade-test: cannot read the inputs: {e}", file=sys.stderr)
        return 2

    errors = check(old, new, readme)
    same = tag_copy_matches()
    if same is False:
        errors.append(f"testdata/00-network.v0.34.1.yaml differs from {TAG}:{TAG_PATH}")
    if errors:
        for e in errors:
            print(f"FAIL: {e}")
        return 1

    # Positive controls: each broken copy must fail, or the check above is inert.
    controls = []
    n = copy.deepcopy(new)
    del n["Resources"]["S3Endpoint"]["Condition"]
    controls.append(("S3Endpoint without its condition", n, readme))
    n = copy.deepcopy(new)
    n["Parameters"]["S3EndpointEnabled"]["Default"] = "false"
    controls.append(("S3EndpointEnabled defaulting to false", n, readme))
    n = copy.deepcopy(new)
    n["Resources"]["WsSg"]["Properties"]["GroupDescription"] = "changed"
    controls.append(("an existing resource modified", n, readme))
    n = copy.deepcopy(new)
    n["Resources"]["WorkspaceSubnet2"] = copy.deepcopy(n["Resources"]["WorkspaceSubnet1"])
    controls.append(("a seventh added resource", n, readme))
    controls.append(("the README block without the Remove line", new,
                     readme.replace("*Remove* `S3Endpoint`", "Remove S3Endpoint")))
    controls.append(("the old README wording", new, readme + "\nIt must be *Add* only\n"))
    for what, tpl, text in controls:
        if not check(old, tpl, text):
            print(f"FAIL: positive control passed: {what}")
            return 1

    print("cfn-network-upgrade-test: OK (v0.34.x -> step 1: Add 6 + Remove S3Endpoint; defaults: "
          f"Add 6 + Modify S3Endpoint; tag copy {'verified' if same else 'not checked: no tag'}; "
          f"{len(controls)} positive controls fail)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
