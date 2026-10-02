#!/usr/bin/env python3
"""The Control Plane task role's EC2 writes, PassRole and ECS/EFS tag writes, checked
against the policy offline.

    python3 deploy/local/cfn-cp-ec2-scope-test.py

## Why this exists

The CP task role's EC2 instance, volume and snapshot writes are fenced to resources that
carry this deployment's af-pool tag (Ec2SlotPool, Ec2Create*, Ec2RunInPool in
deploy/aws/ecs/cfn/20-platform.yaml), its iam:PassRole names the one slot role its own
40-ec2-pool stack creates, and its ECS / EFS tag writes are bounded to the keys the CP
writes. Every one of those fences turns a call the CP makes into AccessDenied the moment
the call names a resource the fence does not expect - and nothing before a real
deployment notices, because the live E2E runs as the deployer, not as this role.

So the inventory lives here, with the resources each call names, and this check fails when

  - a CP call site that makes an EC2 write (any ec2.*Input other than Describe* and the
    tag calls cfn-cp-tag-fence-test.py owns), or an ECS / EFS call that writes tags, is not
    in INVENTORY;
  - an inventoried call would be denied by the templates as they stand;
  - an ECS / EFS tag key the code writes is not one the policy allows (read from the source);
  - one of the ATTACKS would be allowed.

A request is allowed only when EVERY resource it names is allowed by some statement, as
IAM authorizes EC2 actions. aws:RequestTag is a property of the request, so it is present
on each of those resources, the source volume or snapshot included - which is the reason
the create statements must name one resource type each.

It is not the IAM policy simulator, and it does not prove which resources AWS evaluates for
a given call: that list (per the Service Authorization Reference) is written down here,
per call, and is what the live run has to confirm. The evaluator is the one in
cfn-cp-tag-fence-test.py.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import importlib.util
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("tagfence", os.path.join(HERE, "cfn-cp-tag-fence-test.py"))
tf = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(tf)

ACCOUNT, REGION, POOL, CP = tf.ACCOUNT, tf.REGION, tf.POOL, tf.CP
SLOT, QUARANTINED, HOME, HIBERNATED, BACKUP, CANDIDATE, ENGINE = (
    tf.SLOT, tf.QUARANTINED, tf.HOME, tf.HIBERNATED, tf.BACKUP, tf.CANDIDATE, tf.ENGINE)
GOLDEN = dict(CANDIDATE, **{"af-role": "golden", "Name": "af-golden"})
FOREIGN = {"Name": "someone-elses-box"}
OTHER_POOL = {"af-pool": "another-deployment", "af-role": "home", "af-membership": "m-9"}
# The launch template's image is Amazon's: neither it nor its snapshot carries a tag.
AMAZON = {}


def ec2(kind, rid, tags):
    """One existing EC2 resource, with the tags it carries."""
    return (tf.arn(kind, rid), {"ec2:ResourceTag/" + k: v for k, v in tags.items()})


def new(kind):
    """The resource the call creates: no tags of its own yet."""
    return (tf.arn(kind, "new"), {})


def plain(arn):
    return (arn, {})


def request(action, resources, request_tags=None, extra=None):
    """An EC2 request: the action, every resource it names, and the tags it carries.
    RequestTag / TagKeys are request-wide, so they are added to every resource."""
    rt = {}
    if request_tags:
        rt["aws:TagKeys"] = list(request_tags)
        rt.update({"aws:RequestTag/" + k: v for k, v in request_tags.items()})
    rt.update(extra or {})
    return action, [(arn, dict(ctx, **rt)) for arn, ctx in resources]


def subnet():
    return plain("arn:aws:ec2:%s:%s:subnet/subnet-1" % (REGION, ACCOUNT))


LAUNCH_SUPPORT = [
    subnet(),
    plain("arn:aws:ec2:%s:%s:security-group/sg-1" % (REGION, ACCOUNT)),
    plain("arn:aws:ec2:%s:%s:network-interface/eni-new" % (REGION, ACCOUNT)),
    plain("arn:aws:ec2:%s:%s:launch-template/lt-1" % (REGION, ACCOUNT)),
    plain("arn:aws:ec2:%s::image/ami-1" % REGION),
    ec2("snapshot", "snap-ami", AMAZON),
    new("volume"),
]
SPOT = plain("arn:aws:ec2:%s:%s:spot-instances-request/sir-new" % (REGION, ACCOUNT))


def run(tags, extra_resources=()):
    return request("ec2:RunInstances", [new("instance")] + LAUNCH_SUPPORT + list(extra_resources), tags)


def pass_role(role_arn, service="ec2.amazonaws.com"):
    return "iam:PassRole", [(role_arn, {"iam:PassedToService": service})]


def ecs_tag(cluster, tags):
    return request("ecs:TagResource",
                   [plain("arn:aws:ecs:%s:%s:service/%s/ws-1" % (REGION, ACCOUNT, cluster))], tags)


def efs_tag(kind, tags):
    return request("elasticfilesystem:TagResource",
                   [plain("arn:aws:elasticfilesystem:%s:%s:%s/fsap-1" % (REGION, ACCOUNT, kind))], tags)


T = "2026-01-01T00:00:00Z"
HOME_AP_TAGS = {"af-membership": "m-1", "af-role": "home", "Name": "ws-home", "af-tenant": "acme"}

# (file under control-plane/, function, operation, number of such literals in it) -> the
# requests those calls make, each with every resource IAM authorizes it against.
INVENTORY = [
    # --- slots ---
    ("internal/runtime/runtime_ecs_ec2.go", "runSlot", "RunInstances", 1, [
        run(SLOT),
        pass_role(tf.SLOT_ROLE_ARN)]),
    ("internal/runtime/runtime_ecs_ec2.go", "wakeSlot", "StartInstances", 1, [
        request("ec2:StartInstances", [ec2("instance", "i-slot", SLOT)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "quarantineSlot", "StopInstances", 1, [
        request("ec2:StopInstances", [ec2("instance", "i-slot", SLOT)]),
        request("ec2:StopInstances", [ec2("instance", "i-slot", QUARANTINED)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "quarantineSlot", "DetachVolume", 1, [
        request("ec2:DetachVolume", [ec2("instance", "i-slot", SLOT), ec2("volume", "vol-home", HOME)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "abandonLostSlot", "StopInstances", 1, [
        request("ec2:StopInstances", [ec2("instance", "i-slot", SLOT)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "abandonLostSlot", "DetachVolume", 1, [
        request("ec2:DetachVolume", [ec2("instance", "i-slot", SLOT), ec2("volume", "vol-home", HOME)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "forceDetachHome", "DetachVolume", 1, [
        request("ec2:DetachVolume", [ec2("instance", "i-slot", SLOT), ec2("volume", "vol-home", HOME)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "releaseSlotSince", "DetachVolume", 1, [
        request("ec2:DetachVolume", [ec2("instance", "i-slot", SLOT), ec2("volume", "vol-home", HOME)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "attachHome", "AttachVolume", 1, [
        request("ec2:AttachVolume", [ec2("instance", "i-slot", SLOT), ec2("volume", "vol-home", HOME)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "sweepVolume", "StopInstances", 1, [
        request("ec2:StopInstances", [ec2("instance", "i-slot", SLOT)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "sweepFreeSlots", "StopInstances", 1, [
        request("ec2:StopInstances", [ec2("instance", "i-slot", SLOT)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "terminateSlot", "TerminateInstances", 1, [
        request("ec2:TerminateInstances", [ec2("instance", "i-slot", SLOT)]),
        request("ec2:TerminateInstances", [ec2("instance", "i-slot", QUARANTINED)])]),
    # --- homes ---
    ("internal/runtime/runtime_ecs_ec2.go", "createHomeVolume", "CreateVolume", 1, [
        request("ec2:CreateVolume", [new("volume")], HOME),
        request("ec2:CreateVolume", [new("volume"), ec2("snapshot", "snap-hib", HIBERNATED)], HOME),
        request("ec2:CreateVolume", [new("volume"), ec2("snapshot", "snap-golden", GOLDEN)], HOME)]),
    ("internal/runtime/runtime_ecs_ec2.go", "ResizeHome", "ModifyVolume", 1, [
        request("ec2:ModifyVolume", [ec2("volume", "vol-home", HOME)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "deleteHomeVolume", "DeleteVolume", 1, [
        request("ec2:DeleteVolume", [ec2("volume", "vol-home", HOME)])]),
    # --- hibernation and backups ---
    ("internal/runtime/runtime_ecs_ec2.go", "hibernate", "CreateSnapshot", 1, [
        request("ec2:CreateSnapshot", [ec2("volume", "vol-home", HOME), new("snapshot")],
                dict(HIBERNATED, **{"af-home-wipe-repos": T}))]),
    ("internal/runtime/runtime_ecs_ec2.go", "hibernate", "DeleteSnapshot", 2, [
        request("ec2:DeleteSnapshot", [ec2("snapshot", "snap-hib", HIBERNATED)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "restoreSource", "DeleteSnapshot", 1, [
        request("ec2:DeleteSnapshot", [ec2("snapshot", "snap-hib", HIBERNATED)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "deleteHomeSnapshots", "DeleteSnapshot", 1, [
        request("ec2:DeleteSnapshot", [ec2("snapshot", "snap-hib", HIBERNATED)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "BackupHome", "CreateSnapshot", 1, [
        request("ec2:CreateSnapshot", [ec2("volume", "vol-home", HOME), new("snapshot")],
                dict(BACKUP, **{"af-backup-at": T}))]),
    ("internal/runtime/runtime_ecs_ec2.go", "pruneBackups", "DeleteSnapshot", 1, [
        request("ec2:DeleteSnapshot", [ec2("snapshot", "snap-bak", BACKUP)])]),
    ("internal/runtime/runtime_ecs_ec2.go", "deleteBackups", "DeleteSnapshot", 1, [
        request("ec2:DeleteSnapshot", [ec2("snapshot", "snap-bak", BACKUP)])]),
    # --- golden bake ---
    ("internal/runtime/runtime_ecs_ec2_golden.go", "SnapshotHome", "CreateSnapshot", 1, [
        request("ec2:CreateSnapshot", [ec2("volume", "vol-bake", HOME), new("snapshot")], CANDIDATE)]),
    ("internal/runtime/runtime_ecs_ec2_golden.go", "DropSupersededGoldens", "DeleteSnapshot", 1, [
        request("ec2:DeleteSnapshot", [ec2("snapshot", "snap-golden", GOLDEN)]),
        request("ec2:DeleteSnapshot", [ec2("snapshot", "snap-cand", CANDIDATE)])]),
    ("internal/runtime/runtime_ecs_ec2_golden.go", "SweepOrphans", "DeleteVolume", 1, [
        request("ec2:DeleteVolume", [ec2("volume", "vol-bake", HOME)])]),
    # --- engine boxes (60-engines). An instant fleet launches with the caller's
    # RunInstances; on-demand and spot both shown. PassRole of the engine role is
    # 60-engines' own, unchanged, and not modelled here. ---
    ("engine_fleet.go", "request", "CreateFleet", 1, [
        request("ec2:CreateFleet", [plain("arn:aws:ec2:%s:%s:fleet/fleet-new" % (REGION, ACCOUNT))], ENGINE),
        run(ENGINE),
        run(ENGINE, [SPOT])]),
    ("engine_fleet.go", "terminate", "TerminateInstances", 1, [
        request("ec2:TerminateInstances", [ec2("instance", "i-engine", ENGINE)])]),
    # --- ECS / EFS tag writes ---
    ("internal/runtime/runtime_ecs.go", "upsertService", "ecs:CreateService+Tags", 1, [
        ecs_tag(POOL, {"af-membership": "m-1", "af-role": "workspace", "af-tenant": "acme"}),
        ecs_tag(POOL, {"af-membership": "m-1", "af-role": "workspace"})]),
    ("internal/runtime/runtime_ecs.go", "ensureAccessPoint", "efs:CreateAccessPoint+Tags", 1, [
        efs_tag("access-point", HOME_AP_TAGS),
        efs_tag("file-system", HOME_AP_TAGS)]),
    ("internal/runtime/runtime_ecs_ec2.go", "ensureAccessPoint", "efs:CreateAccessPoint+Tags", 1, [
        efs_tag("access-point", HOME_AP_TAGS),
        efs_tag("file-system", HOME_AP_TAGS)]),
    ("internal/runtime/runtime_ecs_ec2.go", "writeEraseRecord", "efs:TagResource", 1, [
        efs_tag("access-point", {"af-home-erased-volumes": "vol-1"})]),
]

# Each of these must be DENIED.
ATTACKS = [
    ("detach a volume from an instance outside the pool",
     request("ec2:DetachVolume", [ec2("instance", "i-x", FOREIGN), ec2("volume", "vol-x", FOREIGN)])),
    ("detach a foreign root volume from a pool slot",
     request("ec2:DetachVolume", [ec2("instance", "i-slot", SLOT), ec2("volume", "vol-x", FOREIGN)])),
    ("attach a foreign volume to a pool slot",
     request("ec2:AttachVolume", [ec2("instance", "i-slot", SLOT), ec2("volume", "vol-x", FOREIGN)])),
    ("attach another deployment's home to a pool slot",
     request("ec2:AttachVolume", [ec2("instance", "i-slot", SLOT), ec2("volume", "vol-o", OTHER_POOL)])),
    ("attach a pool home to an instance outside the pool",
     request("ec2:AttachVolume", [ec2("instance", "i-x", FOREIGN), ec2("volume", "vol-home", HOME)])),
    ("stop an instance outside the pool",
     request("ec2:StopInstances", [ec2("instance", "i-x", FOREIGN)])),
    ("start an instance outside the pool",
     request("ec2:StartInstances", [ec2("instance", "i-x", FOREIGN)])),
    ("terminate an instance outside the pool",
     request("ec2:TerminateInstances", [ec2("instance", "i-x", FOREIGN)])),
    ("terminate another deployment's slot",
     request("ec2:TerminateInstances", [ec2("instance", "i-o", dict(SLOT, **{"af-pool": "another-deployment"}))])),
    ("delete a volume outside the pool",
     request("ec2:DeleteVolume", [ec2("volume", "vol-x", FOREIGN)])),
    ("resize a volume outside the pool",
     request("ec2:ModifyVolume", [ec2("volume", "vol-x", FOREIGN)])),
    ("delete a snapshot outside the pool",
     request("ec2:DeleteSnapshot", [ec2("snapshot", "snap-x", FOREIGN)])),
    ("snapshot a foreign volume, tagging the copy into this pool",
     request("ec2:CreateSnapshot", [ec2("volume", "vol-x", FOREIGN), new("snapshot")], HIBERNATED)),
    ("snapshot another deployment's home into this pool",
     request("ec2:CreateSnapshot", [ec2("volume", "vol-o", OTHER_POOL), new("snapshot")], HIBERNATED)),
    ("snapshot a pool home without af-pool on the copy",
     request("ec2:CreateSnapshot", [ec2("volume", "vol-home", HOME), new("snapshot")], {"Name": "x"})),
    ("snapshot a pool home with no tags on the copy",
     request("ec2:CreateSnapshot", [ec2("volume", "vol-home", HOME), new("snapshot")])),
    ("restore a foreign snapshot into a volume tagged into this pool",
     request("ec2:CreateVolume", [new("volume"), ec2("snapshot", "snap-x", FOREIGN)], HOME)),
    ("restore another deployment's snapshot into this pool",
     request("ec2:CreateVolume", [new("volume"), ec2("snapshot", "snap-o", OTHER_POOL)], HOME)),
    ("create a volume tagged into another pool",
     request("ec2:CreateVolume", [new("volume")], dict(HOME, **{"af-pool": "another-deployment"}))),
    ("create an untagged volume",
     request("ec2:CreateVolume", [new("volume")])),
    ("launch an instance without af-pool",
     run({"Name": "x"})),
    ("launch an instance with no tags",
     run(None)),
    ("launch an instance into another pool",
     run(dict(SLOT, **{"af-pool": "another-deployment"}))),
    ("pass another deployment's slot role",
     pass_role("arn:aws:iam::%s:role/af-other-pool-slot" % ACCOUNT)),
    ("pass this stack's slot role to a service other than EC2",
     pass_role(tf.SLOT_ROLE_ARN, "lambda.amazonaws.com")),
    ("tag another cluster's ECS service",
     ecs_tag("af-other-cluster", {"af-membership": "m-1"})),
    ("write af-pool onto an ECS service",
     ecs_tag(POOL, {"af-pool": POOL})),
    ("ECS TagResource naming no key",
     ecs_tag(POOL, None)),
    ("write an arbitrary key onto an EFS file system",
     efs_tag("file-system", {"af-pool": POOL})),
    ("write an arbitrary key onto an EFS access point",
     efs_tag("access-point", {"owner": "x"})),
    ("EFS TagResource naming no key",
     efs_tag("access-point", None)),
]


# --- call-site discovery ----------------------------------------------------------------

EC2_RE = re.compile(r"&ec2\.([A-Za-z]+)Input\{")
EC2_OWNED_ELSEWHERE = re.compile(r"^(Describe|CreateTags$|DeleteTags$)")
ECS_EFS_RE = re.compile(r"&(ecs|efs)\.([A-Za-z]+)Input\{")
FUNC_RE = re.compile(r"^func (?:\([^)]*\) )?([A-Za-z0-9_]+)")


def _go_files():
    for dirpath, _, files in os.walk(CP):
        for name in files:
            if name.endswith(".go") and not name.endswith("_test.go"):
                yield os.path.join(dirpath, name)


def _funcs(path):
    """(func name, body) for each top-level func in a Go file."""
    with open(path, encoding="utf-8") as fh:
        src = fh.read()
    out, func, body = [], None, []
    for line in src.splitlines():
        m = FUNC_RE.match(line)
        if m:
            if func:
                out.append((func, "\n".join(body)))
            func, body = m.group(1), []
        body.append(line)
    if func:
        out.append((func, "\n".join(body)))
    return src, out


def discover():
    """(file, func, op) -> count. EC2: every write literal outside the tag calls. ECS / EFS:
    a TagResource literal, or a create literal in a function that sets Tags."""
    found = {}
    for path in _go_files():
        _, funcs = _funcs(path)
        rel = os.path.relpath(path, CP)
        for func, body in funcs:
            for name in EC2_RE.findall(body):
                if not EC2_OWNED_ELSEWHERE.match(name):
                    key = (rel, func, name)
                    found[key] = found.get(key, 0) + 1
            for svc, name in ECS_EFS_RE.findall(body):
                if name == "TagResource":
                    op = "%s:TagResource" % svc
                elif re.search(r"^\s*Tags:", body, re.M) and name.startswith(("Create", "Run", "Register")):
                    op = "%s:%s+Tags" % (svc, name)
                else:
                    continue
                key = (rel, func, op)
                found[key] = found.get(key, 0) + 1
    return found


# Where the ECS / EFS tag keys come from: the functions that write them and the helpers
# that append af-tenant.
TAG_KEY_FUNCS = {"ensureAccessPoint", "upsertService", "writeEraseRecord",
                 "appendEFSTenantTag", "appendECSTenantTag"}
KEY_RE = re.compile(r'Key:\s*aws\.String\(\s*([A-Za-z0-9_.]+|"[^"]*")\s*\)')
CONST_RE = re.compile(r'^\s*([A-Za-z0-9_]+)\s+(?:[A-Za-z]+\s+)?=\s*"([^"]*)"', re.M)


def ecs_efs_keys_in_code():
    consts, keys, seen = {}, {"ecs": set(), "efs": set()}, set()
    bodies = []
    for path in _go_files():
        src, funcs = _funcs(path)
        consts.update(CONST_RE.findall(src))
        bodies += [(f, b) for f, b in funcs if f in TAG_KEY_FUNCS]
    for func, body in bodies:
        seen.add(func)
        # The ecs-ec2 upsertService writes no tags: its body names neither tag type.
        if "efstypes.Tag" in body:
            svc = "efs"
        elif "ecstypes.Tag" in body:
            svc = "ecs"
        else:
            continue
        for expr in KEY_RE.findall(body):
            name = expr.split(".")[-1]
            if expr.startswith('"'):
                keys[svc].add(expr.strip('"'))
            elif name in consts:
                keys[svc].add(consts[name])
            else:
                raise ValueError("%s: cannot resolve tag key %s" % (func, expr))
    if seen != TAG_KEY_FUNCS:
        raise ValueError("tag-key functions not found: %s" % sorted(TAG_KEY_FUNCS - seen))
    return keys


def allowed_request(stmts, action, resources, could=False):
    """Allowed only when every resource the request names is allowed."""
    sids = []
    for arn, ctx in resources:
        ok, by = tf.allowed(stmts, action, arn, ctx, could=could)
        if not ok:
            return False, arn
        sids += by
    return True, sorted(set(sids))


def main():
    try:
        stmts = tf.cp_role_statements()
    except (OSError, KeyError, ValueError) as e:
        print("cfn-cp-ec2-scope-test: cannot read the templates: %s" % e)
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
                  "every resource it names and the tags each carries" % (
                      key[0], key[1], key[2], found.get(key, 0), listed.get(key, 0)))

    try:
        keys = ecs_efs_keys_in_code()
    except (OSError, ValueError) as e:
        print("cfn-cp-ec2-scope-test: cannot read the ECS / EFS tag keys: %s" % e)
        return 2
    for k in sorted(keys["ecs"]):
        if not allowed_request(stmts, *ecs_tag(POOL, {k: "v"}))[0]:
            failed += 1
            print("FAIL  the CP writes the ECS tag key %r, which EcsTagServiceOnCreate does not list" % k)
    for k in sorted(keys["efs"]):
        if not allowed_request(stmts, *efs_tag("access-point", {k: "v"}))[0]:
            failed += 1
            print("FAIL  the CP writes the EFS tag key %r, which EfsTagAccessPoints does not list" % k)
    print("ok    %d ECS and %d EFS tag keys found in code" % (len(keys["ecs"]), len(keys["efs"])))

    try:
        n = 0
        for f, fn, op, _, cases in INVENTORY:
            for action, resources in cases:
                n += 1
                ok, why = allowed_request(stmts, action, resources)
                if not ok:
                    failed += 1
                    print("FAIL  %s %s (%s) would be DENIED on %s" % (f, fn, action, why))
                else:
                    print("ok    allowed %-22s %-26s via %s" % (fn, action, ",".join(why)))
        for what, (action, resources) in ATTACKS:
            ok, why = allowed_request(stmts, action, resources, could=True)
            if ok:
                failed += 1
                print("FAIL  attack ALLOWED via %s: %s" % (",".join(why), what))
            else:
                print("ok    denied  %s" % what)

        # Positive controls: the statements these replaced must allow the attacks, or every
        # "denied" above proves nothing. The old Ec2SlotPool, PassSlotRole and TagResource
        # grants, verbatim in shape.
        old = [
            {"Effect": "Allow", "Resource": "*", "Action": [
                "ec2:CreateVolume", "ec2:DeleteVolume", "ec2:AttachVolume", "ec2:DetachVolume",
                "ec2:RunInstances", "ec2:StartInstances", "ec2:StopInstances", "ec2:TerminateInstances",
                "ec2:CreateSnapshot", "ec2:DeleteSnapshot", "ec2:ModifyVolume"]},
            {"Effect": "Allow", "Action": "iam:PassRole",
             "Resource": "arn:aws:iam::%s:role/af-*-slot" % ACCOUNT,
             "Condition": {"StringEquals": {"iam:PassedToService": "ec2.amazonaws.com"}}},
            {"Effect": "Allow", "Resource": "*",
             "Action": ["ecs:TagResource", "elasticfilesystem:TagResource"]},
        ]
        controls = [w for w, _ in ATTACKS if w not in (
            "pass this stack's slot role to a service other than EC2",
            "ECS TagResource naming no key", "EFS TagResource naming no key")]
        for what, (action, resources) in ATTACKS:
            if what in controls and not allowed_request(old, action, resources, could=True)[0]:
                failed += 1
                print("FAIL  positive control: the old grants did not allow: %s" % what)
        print("ok    positive control: the old grants allow %d of the attacks" % len(controls))

        # The reason the create statements name one resource type each: a CreateVolume
        # statement on volume/* AND snapshot/* under aws:RequestTag lets any snapshot in.
        wide = [{"Effect": "Allow", "Action": "ec2:CreateVolume",
                 "Resource": [tf.arn("volume", "*"), tf.arn("snapshot", "*")],
                 "Condition": {"StringEquals": {"aws:RequestTag/af-pool": POOL}}}]
        action, resources = request("ec2:CreateVolume", [new("volume"), ec2("snapshot", "snap-x", FOREIGN)], HOME)
        if not allowed_request(wide, action, resources, could=True)[0]:
            failed += 1
            print("FAIL  evaluator control: RequestTag is not modelled as request-wide")
        else:
            print("ok    evaluator control: a RequestTag statement over both types admits a foreign snapshot")
    except ValueError as e:
        print("cfn-cp-ec2-scope-test: %s" % e)
        return 2

    print("%d inventoried requests, %d attacks, %d failure(s)" % (n, len(ATTACKS), failed))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
