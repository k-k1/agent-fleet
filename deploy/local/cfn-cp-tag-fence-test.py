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
  - a create call or launch template writes a tag key that Ec2TagOnCreate's exact key
    list does not name (read from the Go source, not from INVENTORY);
  - one of the ATTACKS would be allowed.

It is not the IAM policy simulator. It models only the operators the CP role uses, and
fails on any other operator rather than guessing. Tag-key condition names are matched
case-insensitively, as IAM does; when a request holds case variants of one key (EC2 keeps
both), the CP's own calls must pass under every reading and an attack fails if any
reading allows it. The positive control at the end runs
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
# One stack name for every template: the families it builds (af-<stack>-ingest,
# af-<stack>-home-ops) differ by their suffix, which is what the RunTask checks test.
STACK = "test-stack"
PARAMS = {"Cluster": POOL, "AWS::Region": REGION, "AWS::AccountId": ACCOUNT, "AWS::StackName": STACK}
# The 40-ec2-pool stack's own slot role, as !GetAtt resolves it there.
SLOT_ROLE_ARN = "arn:aws:iam::%s:role/af-test-pool-slot" % ACCOUNT
# 20-platform's execution role, which every task the CP starts names.
EXEC_ROLE_ARN = "arn:aws:iam::%s:role/af-test-exec" % ACCOUNT
GETATT = {"SlotRole.Arn": SLOT_ROLE_ARN, "ExecRole.Arn": EXEC_ROLE_ARN}
# The platform stack's cluster, as the other stacks import it (…-ClusterArn, …-ClusterName).
CLUSTER_ARN = "arn:aws:ecs:%s:%s:cluster/%s" % (REGION, ACCOUNT, POOL)
IMPORTS = {"ClusterArn": CLUSTER_ARN, "ClusterName": POOL}


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
        if k == "!GetAtt":
            name = ".".join(x) if isinstance(x, list) else x
            return GETATT.get(name, "<opaque:!GetAtt %s>" % name)
        if k == "!ImportValue" and isinstance(x, dict) and isinstance(x.get("Fn::Sub"), str):
            export = x["Fn::Sub"].rsplit("-", 1)[-1]
            return IMPORTS.get(export, "<opaque:!ImportValue %s>" % export)
        if k == "!Sub" and (isinstance(x, str) or (isinstance(x, list) and len(x) == 2)):
            # The list form names its own variables, each resolved like any other value.
            template, local = (x, {}) if isinstance(x, str) else (x[0], resolve(x[1]))
            def sub(m):
                return str(local.get(m.group(1), PARAMS.get(m.group(1), "<unresolved:%s>" % m.group(1))))
            return re.sub(r"\$\{([^}]+)\}", sub, template)
        return "<opaque:%s>" % k
    if isinstance(v, list):
        return [resolve(i) for i in v]
    if isinstance(v, dict):
        return {k: resolve(i) for k, i in v.items()}
    return v


# The resource types that put a policy document on a role. A grant moved between them (inline
# to managed, #1576) must stay in view here, or every check below runs against fewer
# statements than the role really has and passes for the wrong reason.
POLICY_TYPES = ("AWS::IAM::Policy", "AWS::IAM::ManagedPolicy", "AWS::IAM::RolePolicy")


def _attached_to_cp_role(props):
    """Whether a policy resource names CpTaskRole: by !Ref inside 20-platform, or through the
    imported CpTaskRoleArn everywhere else."""
    roles = props.get("Roles", []) + ([props["RoleName"]] if "RoleName" in props else [])
    return "CpTaskRoleArn" in repr(roles) or {"!Ref": "CpTaskRole"} in roles


def _statements(doc):
    """A policy document's statements, with every !If branch that is a statement in it: the
    reading in which every optional grant is present (SlotAmiIdArm64 set, and so on)."""
    out = []
    for st in doc["Statement"]:
        if isinstance(st, dict) and "!If" in st:
            out += [b for b in st["!If"][1:] if b != {"!Ref": "AWS::NoValue"}]
        else:
            out.append(st)
    return out


def cp_role_statements():
    """Every statement attached to CpTaskRole: its inline policies plus the policies other
    stacks attach to it (30-ingress, 40-ec2-pool, 60-engines), inline or managed. Only some of
    them matter to any one check, but all are returned so an Allow added elsewhere is not
    missed."""
    cfn = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn")
    out = []
    for tpl in ("20-platform.yaml", "30-ingress.yaml", "40-ec2-pool.yaml", "60-engines.yaml"):
        with open(os.path.join(cfn, tpl), encoding="utf-8") as fh:
            doc = yaml.load(fh, Loader=CfnLoader)
        for name, res in doc["Resources"].items():
            props = res.get("Properties", {})
            if name == "CpTaskRole":
                for pol in props.get("Policies", []):
                    out += _statements(pol["PolicyDocument"])
                for arn in props.get("ManagedPolicyArns", []):
                    if isinstance(arn, dict) and "!Ref" in arn:
                        out += _statements(doc["Resources"][arn["!Ref"]]["Properties"]["PolicyDocument"])
            elif res.get("Type") in POLICY_TYPES and _attached_to_cp_role(props):
                out += _statements(props["PolicyDocument"])
    return [resolve(s) for s in out]


# --- the evaluator ----------------------------------------------------------------------

def _list(v):
    return v if isinstance(v, list) else [v]


def _glob(pattern, value):
    # Action names are case-insensitive in IAM.
    return fnmatch.fnmatchcase(value.lower(), pattern.lower())


def _candidates(ctx, key):
    """The values IAM might bind `key` to. The tag-key part of a ResourceTag / RequestTag
    name is matched case-insensitively by IAM, while EC2 keeps `AF-ROLE` beside `af-role`,
    and which one IAM then reads is not specified - so every case variant is a candidate.
    Returns None for an absent key, a list of candidates otherwise."""
    if "Tag/" not in key:
        return [ctx[key]] if key in ctx else None
    hits = [v for k, v in ctx.items() if k.lower() == key.lower()]
    return hits or None


def _string(base, have, want):
    if base == "StringEquals":
        return have in want
    if base == "StringNotEquals":
        return have not in want
    if base == "StringEqualsIgnoreCase":
        return have.lower() in [w.lower() for w in want]
    if base == "StringNotEqualsIgnoreCase":
        return have.lower() not in [w.lower() for w in want]
    if base == "StringLike":
        return any(fnmatch.fnmatchcase(have, w) for w in want)
    if base == "ArnEquals":
        return have in want
    raise ValueError("operator %s is not modelled; extend the evaluator" % base)


def _cond(op, key, want, ctx, pick):
    """One condition key. ctx values are str (single-valued) or list (multi-valued).
    `pick` is any or all: how to combine the case-variant candidates of one tag key. any
    answers "could IAM allow this" (used for attacks), all "does IAM surely allow it"
    (used for the CP's own calls)."""
    want = [str(w) for w in _list(want)]
    cands = _candidates(ctx, key)
    if op == "Null":
        # An empty multi-valued key counts as absent, as it does in IAM.
        null = cands is None or all(c == [] for c in cands)
        return null == (want[0] == "true")
    if op.startswith("ForAllValues:") or op.startswith("ForAnyValue:"):
        quant, base = op.split(":", 1)
        _string(base, "", want)  # refuse an unmodelled operator even with no values
        values = [] if cands is None else [v for c in cands for v in c]
        test = all if quant == "ForAllValues" else any
        return test(_string(base, v, want) for v in values)
    if_exists = op.endswith("IfExists")
    base = op[:-len("IfExists")] if if_exists else op
    if cands is None:
        # Absent key: positive operators fail, negated ones (and ...IfExists) succeed.
        _string(base, "", want)  # still refuse an operator that is not modelled
        return if_exists or "Not" in base
    if any(isinstance(c, list) for c in cands):
        raise ValueError("multi-valued key %s under single-valued %s" % (key, op))
    return pick(_string(base, c, want) for c in cands)


def _matches(stmt, action, resource, ctx, pick):
    if not any(_glob(a, action) for a in _list(stmt.get("Action", []))):
        return False
    if "NotResource" in stmt:
        if any(fnmatch.fnmatchcase(resource, r) for r in _list(stmt["NotResource"])):
            return False
    elif not any(fnmatch.fnmatchcase(resource, r) for r in _list(stmt.get("Resource", []))):
        return False
    for op, keys in (stmt.get("Condition") or {}).items():
        for key, want in keys.items():
            if not _cond(op, key, want, ctx, pick):
                return False
    return True


def allowed(statements, action, resource, ctx, could=False):
    """could=False: allowed under every reading of case-variant tag keys (the CP's own
    calls must pass this). could=True: allowed under at least one (an attack fails if so)."""
    pick, other = (any, all) if could else (all, any)
    allows = [s.get("Sid", "?") for s in statements
              if s["Effect"] == "Allow" and _matches(s, action, resource, ctx, pick)]
    if any(s["Effect"] == "Deny" and _matches(s, action, resource, ctx, other) for s in statements):
        return False, []
    return bool(allows), allows


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
    a key list (the CP never names a value when deleting); tags=None is a DeleteTags with
    no Tag.N at all, which deletes every tag on the resource."""
    ctx = {"ec2:ResourceTag/" + k: v for k, v in has.items()}
    if tags is None:
        return action, arn(kind, "existing"), ctx
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
        on_create("RunInstances", "instance", SLOT),
        on_create("RunInstances", "instance", dict(SLOT, **{"af-replaces-home": "vol-home"}))]),
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
    # The sweeper's fence on a free slot below $Latest, or on an orphaned replacement
    # (af-replaces-home): Ec2TagPoolResources already allows any key but af-pool / af-role on a
    # resource of this pool, so no template change is needed - this holds that it stays so.
    ("internal/runtime/runtime_ecs_ec2.go", "sweepFreeSlots", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "instance", SLOT, {"af-slot-retire": T}),
        on_existing("ec2:CreateTags", "instance", dict(SLOT, **{"af-replaces-home": "vol-home"}),
                    {"af-slot-retire": T})]),
    ("internal/runtime/runtime_ecs_ec2.go", "clearSlotFree", "DeleteTags", 1, [
        on_existing("ec2:DeleteTags", "instance", SLOT, {"af-slot-idle-since": None})]),
    ("internal/runtime/runtime_ecs_ec2_slot_replace.go", "ReserveSlotReplacement", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "instance", SLOT, {"af-slot-replace": T})]),
    ("internal/runtime/runtime_ecs_ec2_slot_replace.go", "ReserveSlotReplacement", "DeleteTags", 1, [
        on_existing("ec2:DeleteTags", "instance", SLOT, {"af-slot-replace": None})]),
    ("internal/runtime/runtime_ecs_ec2_slot_replace.go", "clearReplacesHome", "DeleteTags", 1, [
        on_existing("ec2:DeleteTags", "instance", dict(SLOT, **{"af-replaces-home": "vol-home"}),
                    {"af-replaces-home": None})]),
    ("internal/runtime/runtime_ecs_ec2_slot_replace.go", "retireUnusedReplacement", "CreateTags", 1, [
        on_existing("ec2:CreateTags", "instance", SLOT, {"af-slot-replace": T})]),
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
    ("DeleteTags naming no key (deletes every tag, af-pool included)",
     on_existing("ec2:DeleteTags", "instance", SLOT, None)),
    ("DeleteTags with an empty key list",
     on_existing("ec2:DeleteTags", "volume", HOME, {})),
    ("bookkeeping write carrying AF-ROLE=slot onto this pool's engine box",
     on_existing("ec2:CreateTags", "instance", ENGINE, {"AF-ROLE": "slot", "af-claim": "i-1"})),
    ("bookkeeping write carrying Af-Pool onto a pool volume",
     on_existing("ec2:CreateTags", "volume", HOME, {"Af-Pool": "another-deployment"})),
    ("delete a case variant of af-role",
     on_existing("ec2:DeleteTags", "instance", SLOT, {"AF-ROLE": None})),
    ("quarantine that also adds AF-POOL",
     on_existing("ec2:CreateTags", "instance", SLOT, {"af-role": "quarantined", "AF-POOL": "another-deployment"})),
    ("quarantine that also adds AF-ROLE=slot",
     on_existing("ec2:CreateTags", "instance", SLOT, {"af-role": "quarantined", "AF-ROLE": "slot"})),
    ("quarantine spelt AF-ROLE alongside a bookkeeping key",
     on_existing("ec2:CreateTags", "instance", ENGINE, {"AF-ROLE": "quarantined", "af-claim": "i-1"})),
    ("golden publish that also adds AF-POOL",
     on_existing("ec2:CreateTags", "snapshot", CANDIDATE, {"af-role": "golden", "AF-POOL": "another-deployment"})),
    ("launch a slot tagged af-pool=<other> with AF-POOL=<this pool> beside it",
     on_create("RunInstances", "instance", dict(SLOT, **{"af-pool": "another-deployment", "AF-POOL": POOL}))),
    ("buy an engine box tagged af-pool=<other> with AF-POOL=<this pool> beside it",
     on_create("CreateFleet", "instance", dict(ENGINE, **{"af-pool": "another-deployment", "AF-POOL": POOL}))),
    ("create a home tagged af-pool=<other> with AF-POOL=<this pool> beside it",
     on_create("CreateVolume", "volume", dict(HOME, **{"af-pool": "another-deployment", "AF-POOL": POOL}))),
    ("snapshot tagged af-pool=<other> with Af-Pool=<this pool> beside it",
     on_create("CreateSnapshot", "snapshot", dict(BACKUP, **{"af-pool": "another-deployment", "Af-Pool": POOL}))),
    ("tag-on-create with a key no create call writes",
     on_create("RunInstances", "instance", dict(SLOT, **{"af-claim": "i-1"}))),
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


# Where the keys of a create call come from: the call itself, the helpers it appends,
# and the launch templates EC2 merges in.
CREATE_KEY_FUNCS = {"runSlot", "tags", "createHomeVolume", "hibernate", "BackupHome",
                    "SnapshotHome", "ownedTags", "stampTags"}
KEY_RE = re.compile(r'Key:\s*aws\.String\(\s*([A-Za-z0-9_.]+|"[^"]*")\s*\)')
CONST_RE = re.compile(r'^\s*([A-Za-z0-9_]+)\s+(?:[A-Za-z]+\s+)?=\s*"([^"]*)"', re.M)


def create_keys_in_code():
    """Every tag key the create calls can write, read from the Go source and the launch
    templates, so a key added there fails here instead of at RunInstances."""
    consts, keys = {}, set()
    bodies = []
    for dirpath, _, files in os.walk(CP):
        for name in files:
            if not name.endswith(".go") or name.endswith("_test.go"):
                continue
            with open(os.path.join(dirpath, name), encoding="utf-8") as fh:
                src = fh.read()
            consts.update(CONST_RE.findall(src))
            func, body = None, []
            for line in src.splitlines():
                m = FUNC_RE.match(line)
                if m:
                    if func in CREATE_KEY_FUNCS:
                        bodies.append((func, "\n".join(body)))
                    func, body = m.group(1), []
                body.append(line)
            if func in CREATE_KEY_FUNCS:
                bodies.append((func, "\n".join(body)))
    if {f for f, _ in bodies} != CREATE_KEY_FUNCS:
        raise ValueError("create-key functions not found: %s" % sorted(CREATE_KEY_FUNCS - {f for f, _ in bodies}))
    for func, body in bodies:
        for expr in KEY_RE.findall(body):
            name = expr.split(".")[-1]
            if expr.startswith('"'):
                keys.add(expr.strip('"'))
            elif name in consts:
                keys.add(consts[name])
            else:
                raise ValueError("%s: cannot resolve tag key %s" % (func, expr))
    # homeWipeTags copies af-home-wipe-<kind> for every HomeWipe kind.
    prefix = consts["ec2TagHomeWipePrefix"]
    keys |= {prefix + consts[c] for c in ("HomeWipeRepos", "HomeWipeClean")}
    cfn = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn")
    for tpl in ("40-ec2-pool.yaml", "60-engines.yaml"):
        with open(os.path.join(cfn, tpl), encoding="utf-8") as fh:
            doc = yaml.load(fh, Loader=CfnLoader)
        for res in doc["Resources"].values():
            if res.get("Type") == "AWS::EC2::LaunchTemplate":
                for spec in res["Properties"]["LaunchTemplateData"].get("TagSpecifications", []):
                    keys |= {t["Key"] for t in spec["Tags"]}
    return keys


def main():
    try:
        stmts = cp_role_statements()
    except (OSError, KeyError, ValueError, yaml.YAMLError) as e:
        print("cfn-cp-tag-fence-test: cannot read the templates: %s" % e)
        return 2
    failed = 0

    try:
        code_keys = create_keys_in_code()
    except (OSError, KeyError, ValueError) as e:
        print("cfn-cp-tag-fence-test: cannot read the create calls' keys: %s" % e)
        return 2
    for k in sorted(code_keys):
        action, res, ctx = on_create("RunInstances", "instance", dict({k: "v"}, **{"af-pool": POOL}))
        if not allowed(stmts, action, res, ctx)[0]:
            failed += 1
            print("FAIL  create calls write the tag key %r, which Ec2TagOnCreate does not list" % k)
    print("ok    %d create-time keys found in code and launch templates" % len(code_keys))

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
            ok, sids = allowed(stmts, action, res, ctx, could=True)
            if ok:
                failed += 1
                print("FAIL  attack ALLOWED via %s: %s" % (",".join(sids), what))
            else:
                print("ok    denied  %s" % what)

        # The evaluator's own semantics the statements rely on: Null treats an absent and an
        # empty key list alike, ForAllValues is vacuously true for both, and a tag-key
        # condition name binds case variants.
        null_false = [{"Effect": "Allow", "Action": "ec2:DeleteTags", "Resource": "*",
                       "Condition": {"Null": {"aws:TagKeys": "false"}}}]
        vacuous = [{"Effect": "Allow", "Action": "ec2:DeleteTags", "Resource": "*",
                    "Condition": {"ForAllValues:StringNotEquals": {"aws:TagKeys": ["af-pool"]}}}]
        role_is = [{"Effect": "Allow", "Action": "ec2:CreateTags", "Resource": "*",
                    "Condition": {"StringEquals": {"aws:RequestTag/af-role": "slot"}}}]
        checks = [
            ("Null=false denies a missing key list", not allowed(null_false, *on_existing("ec2:DeleteTags", "instance", SLOT, None))[0]),
            ("Null=false denies an empty key list", not allowed(null_false, *on_existing("ec2:DeleteTags", "instance", SLOT, {}))[0]),
            ("Null=false allows a named key", allowed(null_false, *on_existing("ec2:DeleteTags", "instance", SLOT, {"af-claim": None}))[0]),
            ("ForAllValues is vacuously true without keys", allowed(vacuous, *on_existing("ec2:DeleteTags", "instance", SLOT, None))[0]),
            ("RequestTag/af-role binds AF-ROLE", allowed(role_is, *on_existing("ec2:CreateTags", "instance", FOREIGN, {"AF-ROLE": "slot"}))[0]),
        ]
        for what, ok in checks:
            if not ok:
                failed += 1
                print("FAIL  evaluator control: %s" % what)
            else:
                print("ok    evaluator control: %s" % what)

        # Positive control: the evaluator must be able to say "allowed" to the attack when
        # the statement allows it, or every "denied" above proves nothing.
        old = [{"Effect": "Allow", "Action": ["ec2:CreateTags", "ec2:DeleteTags"], "Resource": "*"}]
        action, res, ctx = ATTACKS[0][1]
        if not allowed(old, action, res, ctx, could=True)[0]:
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
