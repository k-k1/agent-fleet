#!/usr/bin/env python3
"""The Control Plane task role's EC2 tag writes, checked against the policy offline.

    python3 deploy/local/cfn-cp-tag-fence-test.py

## Why this exists

`SsmSlotCommandInstances` (deploy/aws/ecs/cfn/20-platform.yaml) lets the CP task role run
a root shell only on instances tagged af-pool=<pool> and af-role=slot. That fence is only
as strong as the role's grip on those two tags, so the role's own ec2:CreateTags /
ec2:DeleteTags are fenced as well (the Ec2Tag* statements).

A fenced tag write fails with AccessDenied, and nothing before a real deployment notices:
the live E2E runs as the deployer, not as this role, and most of these calls are
best-effort writes that only log. So the inventory lives here, next to an evaluator for
the subset of IAM the statements use, and this check fails when

  - a CP call site that writes EC2 tags is not in INVENTORY (a new one has to be added,
    with the tags the resource carries at that moment, before it ships);
  - an inventoried call would be denied by the template as it stands;
  - one of the ATTACKS would be allowed.

It is not the IAM policy simulator. It models only the operators the CP role uses, and
fails on any other operator rather than guessing. The positive control at the end runs
the attack against the unconditioned statement the fence replaced, to show the
evaluator can say "allowed" at all.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import fnmatch
import os
import re
import sys

try:
    import yaml
except ImportError:
    sys.exit("cfn-cp-tag-fence-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CP = os.path.join(ROOT, "control-plane")
REGION, ACCOUNT, POOL = "ap-northeast-1", "111122223333", "af-test-cluster"
PARAMS = {"Cluster": POOL, "AWS::Region": REGION, "AWS::AccountId": ACCOUNT}


# --- template loading -------------------------------------------------------------------

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


def resolve(v):
    """!Ref / !Sub over PARAMS. Anything else stays an opaque string that matches nothing,
    so an unresolved value can only make a statement narrower, never wider."""
    if isinstance(v, dict) and len(v) == 1 and next(iter(v)).startswith("!"):
        (k, x), = v.items()
        if k == "!Ref":
            return PARAMS.get(x, "<unresolved:%s>" % x)
        if k == "!Sub" and isinstance(x, str):
            def sub(m):
                return PARAMS.get(m.group(1), "<unresolved:%s>" % m.group(1))
            return re.sub(r"\$\{([^}]+)\}", sub, x)
        return "<opaque:%s>" % k
    if isinstance(v, list):
        return [resolve(i) for i in v]
    if isinstance(v, dict):
        return {k: resolve(i) for k, i in v.items()}
    return v


def cp_role_statements():
    """Every statement attached to CpTaskRole: its inline policies plus the 60-engines
    policy that names it. Only those that mention ec2 tag writes matter, but all are
    returned so an Allow added elsewhere is not missed."""
    cfn = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn")
    out = []
    with open(os.path.join(cfn, "20-platform.yaml"), encoding="utf-8") as fh:
        platform = yaml.load(fh, Loader=CfnLoader)
    for pol in platform["Resources"]["CpTaskRole"]["Properties"]["Policies"]:
        out += pol["PolicyDocument"]["Statement"]
    with open(os.path.join(cfn, "60-engines.yaml"), encoding="utf-8") as fh:
        engines = yaml.load(fh, Loader=CfnLoader)
    for res in engines["Resources"].values():
        if res.get("Type") == "AWS::IAM::Policy" and "CpTaskRoleArn" in repr(res["Properties"]["Roles"]):
            out += res["Properties"]["PolicyDocument"]["Statement"]
    return [resolve(s) for s in out]


# --- the evaluator ----------------------------------------------------------------------

def _list(v):
    return v if isinstance(v, list) else [v]


def _glob(pattern, value):
    # Action names are case-insensitive in IAM.
    return fnmatch.fnmatchcase(value.lower(), pattern.lower())


def _cond(op, key, want, ctx):
    """One condition key. ctx values are str (single-valued) or list (multi-valued)."""
    want = [str(w) for w in _list(want)]
    have = ctx.get(key)
    if op == "Null":
        return (have is None) == (want[0] == "true")
    if op.startswith("ForAllValues:"):
        base = op.split(":", 1)[1]
        return all(_cond(base, key, want, {key: h}) for h in (have or []))
    if op.startswith("ForAnyValue:"):
        base = op.split(":", 1)[1]
        return any(_cond(base, key, want, {key: h}) for h in (have or []))
    if_exists = op.endswith("IfExists")
    base = op[:-len("IfExists")] if if_exists else op
    if have is None:
        # Absent key: positive operators fail, negated ones (and ...IfExists) succeed.
        return if_exists or base.startswith("StringNot") or base.startswith("ArnNot")
    if isinstance(have, list):
        raise ValueError("multi-valued key %s under single-valued %s" % (key, op))
    if base == "StringEquals":
        return have in want
    if base == "StringNotEquals":
        return have not in want
    if base == "StringLike":
        return any(fnmatch.fnmatchcase(have, w) for w in want)
    if base == "ArnEquals":
        return have in want
    raise ValueError("operator %s is not modelled; extend the evaluator" % op)


def _matches(stmt, action, resource, ctx):
    if not any(_glob(a, action) for a in _list(stmt.get("Action", []))):
        return False
    if not any(fnmatch.fnmatchcase(resource, r) for r in _list(stmt.get("Resource", []))):
        return False
    for op, keys in (stmt.get("Condition") or {}).items():
        for key, want in keys.items():
            if not _cond(op, key, want, ctx):
                return False
    return True


def allowed(statements, action, resource, ctx):
    hits = [s for s in statements if _matches(s, action, resource, ctx)]
    if any(s["Effect"] == "Deny" for s in hits):
        return False, []
    sids = [s.get("Sid", "?") for s in hits if s["Effect"] == "Allow"]
    return bool(sids), sids


def arn(kind, rid):
    if kind == "snapshot":
        return "arn:aws:ec2:%s::snapshot/%s" % (REGION, rid)
    return "arn:aws:ec2:%s:%s:%s/%s" % (REGION, ACCOUNT, kind, rid)


def on_create(create_action, kind, tags):
    """CreateTags as authorized for TagSpecifications on a create call."""
    ctx = {"ec2:CreateAction": create_action, "aws:TagKeys": list(tags)}
    ctx.update({"aws:RequestTag/" + k: v for k, v in tags.items()})
    return "ec2:CreateTags", arn(kind, "new"), ctx


def on_existing(action, kind, has, tags):
    """CreateTags / DeleteTags on a resource that exists and carries `has`. DeleteTags gets
    a key list (the CP never names a value when deleting)."""
    ctx = {"ec2:ResourceTag/" + k: v for k, v in has.items()}
    ctx["aws:TagKeys"] = list(tags)
    if action == "ec2:CreateTags":
        ctx.update({"aws:RequestTag/" + k: v for k, v in tags.items()})
    return action, arn(kind, "existing"), ctx


# --- what the resources look like when the CP writes to them ----------------------------
# Taken from the create calls below: every resource the CP retags was created by the CP
# with af-pool=<pool> in the same call, and is found again through af-pool or through a
# membership tag that only ever lands together with it.

SLOT = {"af-pool": POOL, "af-role": "slot", "af-slot-size": "m7i.large",
        "Name": "af-slot-m7i.large", "af-managed-by": "agent-fleet"}
QUARANTINED = dict(SLOT, **{"af-role": "quarantined"})
HOME = {"af-pool": POOL, "af-role": "home", "af-membership": "m-1", "af-workspace": "ws",
        "Name": "ws-home", "af-tenant": "acme"}
HIBERNATED = dict(HOME)
BACKUP = dict(HOME, **{"af-role": "backup"})
CANDIDATE = {"af-pool": POOL, "af-role": "golden-candidate", "af-arch": "x86_64",
             "af-bake-started": "t", "Name": "af-golden-candidate-x86_64",
             "af-image": "img", "af-image-fp": "fp"}
ENGINE = {"af-pool": POOL, "af-role": "engine-llm", "af-engine-offer": "o", "af-engine-buy": "spot",
          "af-managed-by": "agent-fleet", "Name": "af-engine-llm"}
T = "2026-01-01T00:00:00Z"

# (file under control-plane/, function, operation, number of such calls in it) -> the
# requests those calls make. "TagSpecifications" is tag-on-create.
INVENTORY = [
    # --- tag-on-create ---
    ("internal/runtime/runtime_ecs_ec2.go", "runSlot", "TagSpecifications", 1, [
        on_create("RunInstances", "instance", SLOT)]),
    ("engine_fleet.go", "request", "TagSpecifications", 1, [
        on_create("CreateFleet", "instance", ENGINE)]),
    ("internal/runtime/runtime_ecs_ec2.go", "createHomeVolume", "TagSpecifications", 1, [
        on_create("CreateVolume", "volume", HOME),
        on_create("CreateVolume", "volume", dict(HOME, **{"af-home-wipe-clean": T}))]),
    ("internal/runtime/runtime_ecs_ec2.go", "hibernate", "TagSpecifications", 1, [
        on_create("CreateSnapshot", "snapshot", dict(HIBERNATED, **{"af-home-wipe-repos": T}))]),
    ("internal/runtime/runtime_ecs_ec2.go", "BackupHome", "TagSpecifications", 1, [
        on_create("CreateSnapshot", "snapshot", dict(BACKUP, **{"af-backup-at": T, "Name": "ws-backup"}))]),
    ("internal/runtime/runtime_ecs_ec2_golden.go", "SnapshotHome", "TagSpecifications", 1, [
        on_create("CreateSnapshot", "snapshot", CANDIDATE)]),
    # --- home volume bookkeeping ---
    ("internal/runtime/runtime_ecs_ec2.go", "markIdle", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "volume", HOME, {"af-idle-since": T})]),
    ("internal/runtime/runtime_ecs_ec2.go", "clearIdle", "DeleteTags", 1, [
        on_existing("ec2:DeleteTags", "volume", HOME, {"af-idle-since": None})]),
    ("internal/runtime/runtime_ecs_ec2.go", "clearDormancy", "DeleteTags", 1, [
        on_existing("ec2:DeleteTags", "volume", HOME, {"af-idle-since": None, "af-hibernating": None})]),
    ("internal/runtime/runtime_ecs_ec2.go", "claim", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "volume", HOME, {"af-claim": "i-1", "af-claim-at": T})]),
    ("internal/runtime/runtime_ecs_ec2.go", "unclaim", "DeleteTags", 1, [
        on_existing("ec2:DeleteTags", "volume", HOME, {"af-claim": None, "af-claim-at": None})]),
    ("internal/runtime/runtime_ecs_ec2.go", "hibernationMark", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "volume", HOME, {"af-hibernating": T})]),
    ("internal/runtime/runtime_ecs_ec2.go", "unmarkHibernating", "DeleteTags", 1, [
        on_existing("ec2:DeleteTags", "volume", HOME, {"af-hibernating": None})]),
    ("internal/runtime/runtime_ecs_ec2_home_wipe.go", "markHomeWipe", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "volume", HOME, {"af-home-wipe-repos": T}),
        on_existing("ec2:CreateTags", "snapshot", HIBERNATED, {"af-home-wipe-clean": T})]),
    ("internal/runtime/runtime_ecs_ec2_home_wipe.go", "wipeMountedHome", "DeleteTags", 1, [
        on_existing("ec2:DeleteTags", "volume", HOME, {"af-home-wipe-clean": None, "af-home-wipe-repos": None})]),
    ("internal/runtime/runtime_ecs_ec2_golden.go", "MarkHomeBaked", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "volume", HOME, {"af-bake-ready": T})]),
    # --- slot instance bookkeeping ---
    ("internal/runtime/runtime_ecs_ec2.go", "tagSlotOwner", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "instance", SLOT, {"af-membership": "m-1", "af-tenant": "acme"})]),
    ("internal/runtime/runtime_ecs_ec2.go", "untagSlotOwner", "DeleteTags", 1, [
        on_existing("ec2:DeleteTags", "instance", SLOT, {"af-membership": None, "af-tenant": None}),
        on_existing("ec2:DeleteTags", "instance", QUARANTINED, {"af-membership": None, "af-tenant": None})]),
    ("internal/runtime/runtime_ecs_ec2.go", "markSlotFree", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "instance", SLOT, {"af-slot-idle-since": T})]),
    ("internal/runtime/runtime_ecs_ec2.go", "clearSlotFree", "DeleteTags", 1, [
        on_existing("ec2:DeleteTags", "instance", SLOT, {"af-slot-idle-since": None})]),
    ("internal/runtime/runtime_ecs_ec2.go", "sweepSlotOwnerTags", "DeleteTags", 2, [
        on_existing("ec2:DeleteTags", "instance", SLOT, {"af-membership": None, "af-tenant": None}),
        on_existing("ec2:DeleteTags", "instance", QUARANTINED, {"af-tenant": None})]),
    ("internal/runtime/runtime_ecs_ec2.go", "sweepSlotOwnerTags", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "instance", SLOT, {"af-membership": "m-1", "af-tenant": "acme"}),
        on_existing("ec2:CreateTags", "instance", QUARANTINED, {"af-membership": "m-1"})]),
    # --- role changes ---
    ("internal/runtime/runtime_ecs_ec2.go", "markQuarantined", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "instance", SLOT, {"af-role": "quarantined",
                    "af-quarantine-reason": "mount failed", "af-quarantine-at": T})]),
    ("internal/runtime/runtime_ecs_ec2_golden.go", "SetGoldenRole", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "snapshot", CANDIDATE, {"af-role": "golden", "Name": "af-golden"}),
        on_existing("ec2:CreateTags", "snapshot", CANDIDATE, {"af-role": "golden-rejected",
                    "Name": "af-golden-rejected", "af-bake-reason": "probe timed out"})]),
]

FOREIGN = {"Name": "someone-elses-box"}
OTHER_POOL = dict(SLOT, **{"af-pool": "another-deployment"})

# Each of these must be DENIED.
ATTACKS = [
    ("stamp af-pool + af-role=slot onto an unrelated instance",
     on_existing("ec2:CreateTags", "instance", FOREIGN, {"af-pool": POOL, "af-role": "slot"})),
    ("stamp af-pool alone onto an unrelated instance",
     on_existing("ec2:CreateTags", "instance", FOREIGN, {"af-pool": POOL})),
    ("stamp af-role=slot onto an unrelated instance",
     on_existing("ec2:CreateTags", "instance", FOREIGN, {"af-role": "slot"})),
    ("move another deployment's slot into this pool",
     on_existing("ec2:CreateTags", "instance", OTHER_POOL, {"af-pool": POOL})),
    ("turn this pool's engine box into a slot",
     on_existing("ec2:CreateTags", "instance", ENGINE, {"af-role": "slot"})),
    ("un-quarantine a slot",
     on_existing("ec2:CreateTags", "instance", QUARANTINED, {"af-role": "slot"})),
    ("quarantine a snapshot (wrong resource type for that role)",
     on_existing("ec2:CreateTags", "snapshot", CANDIDATE, {"af-role": "quarantined"})),
    ("publish an instance as golden (wrong resource type for that role)",
     on_existing("ec2:CreateTags", "instance", ENGINE, {"af-role": "golden"})),
    ("re-point a pool resource at another pool",
     on_existing("ec2:CreateTags", "volume", HOME, {"af-pool": "another-deployment"})),
    ("strip af-pool from a pool resource",
     on_existing("ec2:DeleteTags", "instance", SLOT, {"af-pool": None})),
    ("strip af-role from a pool resource",
     on_existing("ec2:DeleteTags", "instance", QUARANTINED, {"af-role": None})),
    ("any bookkeeping tag on an unrelated volume",
     on_existing("ec2:CreateTags", "volume", FOREIGN, {"af-claim": "i-1"})),
    ("delete a tag from an unrelated instance",
     on_existing("ec2:DeleteTags", "instance", FOREIGN, {"Name": None})),
    ("plant a slot into another deployment's pool at launch",
     on_create("RunInstances", "instance", dict(SLOT, **{"af-pool": "another-deployment"}))),
    ("plant a home into another deployment's pool",
     on_create("CreateVolume", "volume", dict(HOME, **{"af-pool": "another-deployment"}))),
    ("tag-on-create with no af-pool at all",
     on_create("RunInstances", "instance", {"af-role": "slot"})),
    ("tag-on-create through a create action the CP never calls",
     on_create("CreateImage", "image", {"af-pool": POOL})),
]


# --- call-site discovery ----------------------------------------------------------------

SITE_RE = re.compile(r"\.(CreateTags|DeleteTags)\(ctx|TagSpecifications:\s*\[\]ec2types\.TagSpecification")
FUNC_RE = re.compile(r"^func (?:\([^)]*\) )?([A-Za-z0-9_]+)")


def discover():
    """(file, func, op) -> count, over every non-test Go file of the control plane."""
    found = {}
    for dirpath, _, files in os.walk(CP):
        for name in files:
            if not name.endswith(".go") or name.endswith("_test.go"):
                continue
            path = os.path.join(dirpath, name)
            func = None
            with open(path, encoding="utf-8") as fh:
                for line in fh:
                    m = FUNC_RE.match(line)
                    if m:
                        func = m.group(1)
                    s = SITE_RE.search(line)
                    if s:
                        op = s.group(1) or "TagSpecifications"
                        key = (os.path.relpath(path, CP), func, op)
                        found[key] = found.get(key, 0) + 1
    return found


def main():
    try:
        stmts = cp_role_statements()
    except (OSError, KeyError, ValueError, yaml.YAMLError) as e:
        print("cfn-cp-tag-fence-test: cannot read the templates: %s" % e)
        return 2
    failed = 0

    found = discover()
    if not found:
        print("FAIL  no call site found at all - the discovery regex is broken")
        return 1
    listed = {(f, fn, op): n for f, fn, op, n, _ in INVENTORY}
    for key in sorted(set(found) | set(listed)):
        if found.get(key) != listed.get(key):
            failed += 1
            print("FAIL  call sites %s:%s %s: code has %s, INVENTORY has %s - inventory it with "
                  "the tags the resource carries at that moment" % (key[0], key[1], key[2],
                                                                     found.get(key, 0), listed.get(key, 0)))

    try:
        n = 0
        for f, fn, op, _, cases in INVENTORY:
            for action, res, ctx in cases:
                n += 1
                ok, sids = allowed(stmts, action, res, ctx)
                if not ok:
                    failed += 1
                    print("FAIL  %s %s (%s) would be DENIED: %s keys=%s" % (f, fn, op, res, ctx["aws:TagKeys"]))
                else:
                    print("ok    allowed %-22s %-38s via %s" % (fn, action + " " + res.rsplit(":", 1)[1], ",".join(sids)))
        for what, (action, res, ctx) in ATTACKS:
            ok, sids = allowed(stmts, action, res, ctx)
            if ok:
                failed += 1
                print("FAIL  attack ALLOWED via %s: %s" % (",".join(sids), what))
            else:
                print("ok    denied  %s" % what)

        # Positive control: the evaluator must be able to say "allowed" to the attack when
        # the statement allows it, or every "denied" above proves nothing.
        old = [{"Effect": "Allow", "Action": ["ec2:CreateTags", "ec2:DeleteTags"], "Resource": "*"}]
        action, res, ctx = ATTACKS[0][1]
        if not allowed(old, action, res, ctx)[0]:
            failed += 1
            print("FAIL  positive control: the unconditioned statement did not allow the attack")
        else:
            print("ok    positive control: the unconditioned CreateTags allows the attack")
    except ValueError as e:
        print("cfn-cp-tag-fence-test: %s" % e)
        return 2

    print("%d inventoried requests, %d attacks, %d failure(s)" % (n, len(ATTACKS), failed))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
