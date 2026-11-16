#!/usr/bin/env python3
"""The EFS file-system policy and the home task's mount, checked offline against the templates.

    python3 deploy/local/cfn-efs-policy-test.py

## Why this exists

Access points are chosen by the client. Without a file-system policy any task in the workspace
security group can mount the root, or another member's access point, and read the CLI
credentials kept there (#3445). 10-data therefore declares a FileSystemPolicy; the one mount
that must stay possible without an access point is the home task's (Recreate, Clean home and
Destroy run `efs-home-op` against the root), which needs an IAM-authorized mount by a dedicated
role that the policy names.

This evaluates the policy statements (Allow / Deny, Bool, Null) for the mounts
that matter and fails when

  - an anonymous mount without an access point, one without TLS, or one that does not come
    through a mount target is admitted;
  - a workspace mount (access point, TLS, no IAM) is refused;
  - the home task's role cannot mount the root, or any other principal can;
  - a Deny names an action other than the three client actions (a Deny on everything would
    refuse CreateAccessPoint, which carries no access point);
  - the home task stops using the role, stops asking for IAM authorization, or the CP loses
    the right to pass that role;
  - the security groups stop being able to keep workspace tasks off NFS on ecs-ec2: the
    workspace rule is unconditional again, the home task or the slots lose their own rule,
    or the ecs-ec2 task definition goes back to EFS volumes (mounted through the task's ENI).

Positive controls at the end run the same checks against broken copies.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import os
import sys

try:
    import yaml
except ImportError:
    sys.exit("cfn-efs-policy-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CFN = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn")
ROLE = "arn:aws:iam::111122223333:role/home-ops"
OTHER = "arn:aws:iam::111122223333:role/ws-task"
EFS_KEYS = {"aws:SecureTransport", "aws:SourceIp", "elasticfilesystem:AccessPointArn",
            "elasticfilesystem:AccessedViaMountTarget"}
CLIENT = {"elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite",
          "elasticfilesystem:ClientRootAccess"}


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


class UnsupportedKey(Exception):
    pass


def as_list(x):
    return x if isinstance(x, list) else [x]


def resolve(x):
    """The policy with HomeOpsEfsRole's ARN substituted, as CloudFormation would."""
    if isinstance(x, dict):
        if x == {"!GetAtt": "HomeOpsEfsRole.Arn"}:
            return ROLE
        return {k: resolve(v) for k, v in x.items()}
    if isinstance(x, list):
        return [resolve(v) for v in x]
    return x


def cond_holds(cond, ctx):
    """AWS condition semantics for Bool and Null, over only the four condition keys EFS
    enforces for NFS clients; any other key is a failure, not a pass (#3474 review)."""
    for op, kv in cond.items():
        for key, want in kv.items():
            want = [str(w).lower() if isinstance(w, bool) else str(w) for w in as_list(want)]
            if key not in EFS_KEYS:
                raise UnsupportedKey(key)
            have = ctx.get(key)
            if op == "Bool":
                ok = have is not None and str(have).lower() in want
            elif op == "Null":
                ok = (have is None) == (want[0] == "true")
            else:
                raise SystemExit("cfn-efs-policy-test: unhandled condition operator %s" % op)
            if not ok:
                return False
    return True


def allowed(policy, action, ctx, identity_allow=False):
    """Explicit deny wins; otherwise one Allow, from the file-system policy or (when the
    caller's identity policy grants the action, as for IAM-authorized principals) from the
    identity side: EFS needs only one of the two (iam-access-control-nfs-efs)."""
    verdict = identity_allow
    for st in policy["Statement"]:
        princ = as_list(st["Principal"]["AWS"])
        who = ctx.get("aws:PrincipalArn")
        if "*" not in princ and who not in princ:
            continue
        if action not in as_list(st["Action"]) or not cond_holds(st.get("Condition", {}), ctx):
            continue
        if st["Effect"] == "Deny":
            return False
        verdict = True
    return verdict


def mount(*, ap=True, tls=True, mt=True, role=None):
    ctx = {"elasticfilesystem:AccessedViaMountTarget": mt, "aws:SecureTransport": tls}
    if ap:
        ctx["elasticfilesystem:AccessPointArn"] = "arn:aws:elasticfilesystem:r:a:access-point/fsap-1"
    if role:
        ctx["aws:PrincipalArn"] = role
    return ctx


def nfs_rule(rule, source):
    return (isinstance(rule, dict) and rule.get("IpProtocol") == "tcp" and rule.get("FromPort") == 2049
            and rule.get("ToPort") == 2049 and source in str(rule.get("SourceSecurityGroupId")))


def check_sg(data, ingress, pool):
    """Who can reach the mount targets. The policy cannot tell members apart, so on ecs-ec2
    the workspace group has to be closable without taking the home task or the slots with it."""
    fails = []
    rules = data["Resources"]["EfsSg"]["Properties"]["SecurityGroupIngress"]
    ws = [r for r in rules if "WsSgId" in str(r)]
    if len(ws) != 1 or "!If" not in ws[0] or ws[0]["!If"][0] != "EfsAdmitsWorkspaces" \
            or not nfs_rule(ws[0]["!If"][1], "WsSgId") or ws[0]["!If"][2] != {"!Ref": "AWS::NoValue"}:
        fails.append("EfsSg must admit the workspace group only under EfsAdmitsWorkspaces")
    if data.get("Conditions", {}).get("EfsAdmitsWorkspaces") != {"!Equals": [{"!Ref": "EfsWorkspaceIngress"}, "allow"]}:
        fails.append("EfsAdmitsWorkspaces must be EfsWorkspaceIngress == allow")
    param = data["Parameters"].get("EfsWorkspaceIngress", {})
    if param.get("Default") != "allow" or param.get("AllowedValues") != ["allow", "deny"]:
        fails.append("EfsWorkspaceIngress must offer allow|deny and default to allow (ecs needs it)")
    if not any(nfs_rule(r, "HomeOpsSg") for r in rules):
        fails.append("EfsSg must admit the home task's own group unconditionally")
    for out in ("HomeOpsSgId", "EfsSgId"):
        if data["Outputs"].get(out, {}).get("Export") is None:
            fails.append("10-data must export %s" % out)
    env = ingress["Resources"]["TaskDef"]["Properties"]["ContainerDefinitions"][0]["Environment"]
    if not any(isinstance(e, dict) and e.get("Name") == "AF_ECS_HOME_OPS_SECURITY_GROUP"
               and "HomeOpsSgId" in str(e.get("Value")) for e in env):
        fails.append("30-ingress must pass 10-data's HomeOpsSgId as AF_ECS_HOME_OPS_SECURITY_GROUP")
    slot = pool["Resources"].get("SlotEfsIngress", {}).get("Properties", {})
    if not nfs_rule(slot, "SlotSg") or "EfsSgId" not in str(slot.get("GroupId")):
        fails.append("40-ec2-pool must admit SlotSg to 10-data's EfsSgId on 2049")
    return fails


def check(data, ingress):
    fails = []
    fs = data["Resources"]["FileSystem"]["Properties"]
    pol = resolve(fs.get("FileSystemPolicy"))
    if not pol:
        return ["10-data FileSystem has no FileSystemPolicy"]
    for st in pol["Statement"]:
        if st["Effect"] == "Deny" and set(as_list(st["Action"])) != CLIENT:
            fails.append("Deny %s must name exactly the client actions, got %s"
                         % (st.get("Sid"), st["Action"]))
    cases = [
        # (what, ctx, wanted)
        ("workspace mount: access point, TLS, anonymous", mount(), True),
        ("anonymous mount without an access point", mount(ap=False), False),
        ("workspace mount without TLS", mount(tls=False), False),
        ("workspace mount not through a mount target", mount(mt=False), False),
        ("home task root mount (IAM role, no access point)", mount(ap=False, role=ROLE), True),
        ("another role's root mount", mount(ap=False, role=OTHER), False),
        ("home task root mount without TLS", mount(ap=False, tls=False, role=ROLE), False),
        ("identity-allowed role, TLS, no access point (KNOWN LIMIT: no explicit Deny)",
         mount(ap=False, role=OTHER), True, True),
        ("identity-allowed role without TLS", mount(ap=False, tls=False, role=OTHER), False, True),
    ]
    for what, ctx, want, *ident in cases:
        for action in sorted(CLIENT):
            try:
                got = allowed(pol, action, ctx, bool(ident and ident[0]))
            except UnsupportedKey as e:
                return fails + ["condition key %s is not enforced by EFS for NFS clients" % e]
            if got != want:
                fails.append("%s: %s allowed=%s, want %s" % (what, action, got, want))
    for st in pol["Statement"]:
        if st["Effect"] == "Deny" and "elasticfilesystem:CreateAccessPoint" in as_list(st["Action"]):
            fails.append("a Deny covers CreateAccessPoint")

    role = data["Resources"].get("HomeOpsEfsRole")
    if not role or data["Outputs"].get("HomeOpsEfsRoleArn", {}).get("Export") is None:
        fails.append("10-data must declare HomeOpsEfsRole and export its ARN")

    td = ingress["Resources"]["HomeOpsTaskDef"]["Properties"]
    if "HomeOpsEfsRoleArn" not in str(td.get("TaskRoleArn")):
        fails.append("HomeOpsTaskDef must run as 10-data's HomeOpsEfsRoleArn")
    vol = td["Volumes"][0]["EFSVolumeConfiguration"]
    if vol.get("RootDirectory") != "/" or vol.get("TransitEncryption") != "ENABLED":
        fails.append("HomeOpsTaskDef must mount the root with TLS: %r" % vol)
    if vol.get("AuthorizationConfig", {}).get("IAM") != "ENABLED":
        fails.append("HomeOpsTaskDef volume must ask for IAM authorization")
    grants = ingress["Resources"]["CpHomeOpsManagedPolicy"]["Properties"]["PolicyDocument"]["Statement"]
    if not any(g.get("Action") == "iam:PassRole" and "HomeOpsEfsRoleArn" in str(g.get("Resource"))
               and g.get("Condition", {}).get("StringEquals", {}).get("iam:PassedToService")
               == "ecs-tasks.amazonaws.com" for g in grants):
        fails.append("the CP must be allowed to pass only HomeOpsEfsRole to ecs-tasks")
    return fails


def check_go():
    path = os.path.join(ROOT, "control-plane", "internal", "runtime", "runtime_ecs.go")
    src = open(path).read()
    i = src.index("func efsVolume(")
    body = src[i:i + 1500]
    fails = []
    for need in ("EFSTransitEncryptionEnabled", "AccessPointId"):
        if need not in body:
            fails.append("efsVolume no longer sets %s; the file-system policy refuses such a mount" % need)
    rt = os.path.join(ROOT, "control-plane", "internal", "runtime")
    if "efsVolume(" in open(os.path.join(rt, "runtime_ecs_ec2_taskdef.go")).read():
        fails.append("the ecs-ec2 task definition declares an EFS volume; ECS mounts it through the "
                     "task's ENI, which EfsWorkspaceIngress=deny closes")
    if "homeTaskSecurityGroup()" not in open(os.path.join(rt, "runtime_ecs_home_task.go")).read():
        fails.append("the home task no longer runs in its own security group")
    return fails


def main():
    data, ingress, pool = load("10-data.yaml"), load("30-ingress.yaml"), load("40-ec2-pool.yaml")
    fails = check(data, ingress) + check_sg(data, ingress, pool) + check_go()
    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1

    def broken(mutate):
        d, i = copy.deepcopy(data), copy.deepcopy(ingress)
        mutate(d, i)
        return check(d, i) + check_sg(d, i, pool)

    def sg_rules(d):
        return d["Resources"]["EfsSg"]["Properties"]["SecurityGroupIngress"]

    def unconditional_ws(d, i):
        rules = sg_rules(d)
        n = next(n for n, r in enumerate(rules) if "WsSgId" in str(r))
        rules[n] = rules[n]["!If"][1]

    def broken_pool(mutate):
        p = copy.deepcopy(pool)
        mutate(p)
        return check_sg(data, ingress, p)

    def stmt(d, sid):
        return next(s for s in d["Resources"]["FileSystem"]["Properties"]["FileSystemPolicy"]["Statement"]
                    if s["Sid"] == sid)

    def drop(sid):
        def m(d, i):
            st = d["Resources"]["FileSystem"]["Properties"]["FileSystemPolicy"]["Statement"]
            st[:] = [s for s in st if s["Sid"] != sid]
        return m

    controls = {
        "policy removed": lambda d, i: d["Resources"]["FileSystem"]["Properties"].pop("FileSystemPolicy"),
        "TLS deny removed": drop("DenyWithoutTls"),
        "home role allow removed": drop("AllowHomeOpsRole"),
        "access point condition removed": lambda d, i: stmt(d, "AllowAccessPointClients")["Condition"]
        .pop("Null"),
        "mount target condition removed": lambda d, i: stmt(d, "AllowAccessPointClients")["Condition"]
        .pop("Bool"),
        "root mount open to every principal": lambda d, i: stmt(d, "AllowHomeOpsRole")["Principal"]
        .__setitem__("AWS", "*"),
        "key EFS does not enforce": lambda d, i: stmt(d, "AllowAccessPointClients")["Condition"]
        .__setitem__("StringEquals", {"aws:PrincipalArn": ROLE}),
        "deny on every action": lambda d, i: stmt(d, "DenyWithoutTls").__setitem__(
            "Action", ["elasticfilesystem:*"]),
        "home task without a role": lambda d, i: i["Resources"]["HomeOpsTaskDef"]["Properties"].pop("TaskRoleArn"),
        "home task without IAM": lambda d, i: i["Resources"]["HomeOpsTaskDef"]["Properties"]["Volumes"][0][
            "EFSVolumeConfiguration"].pop("AuthorizationConfig"),
        "pass-role grant removed": lambda d, i: i["Resources"]["CpHomeOpsManagedPolicy"]["Properties"][
            "PolicyDocument"]["Statement"].__delitem__(
                next(n for n, g in enumerate(i["Resources"]["CpHomeOpsManagedPolicy"]["Properties"][
                    "PolicyDocument"]["Statement"]) if g.get("Action") == "iam:PassRole")),
    }
    controls.update({
        "workspace NFS rule unconditional": unconditional_ws,
        "home task NFS rule removed": lambda d, i: sg_rules(d).__delitem__(
            next(n for n, r in enumerate(sg_rules(d)) if "HomeOpsSg" in str(r))),
        "EfsWorkspaceIngress defaults to deny": lambda d, i: d["Parameters"]["EfsWorkspaceIngress"]
        .__setitem__("Default", "deny"),
        "EfsSgId export removed": lambda d, i: d["Outputs"].pop("EfsSgId"),
        "home task group not passed to the CP": lambda d, i: i["Resources"]["TaskDef"]["Properties"][
            "ContainerDefinitions"][0]["Environment"].__delitem__(
                next(n for n, e in enumerate(i["Resources"]["TaskDef"]["Properties"]["ContainerDefinitions"][0][
                    "Environment"]) if isinstance(e, dict) and e.get("Name") == "AF_ECS_HOME_OPS_SECURITY_GROUP")),
    })
    for name, m in controls.items():
        if not broken(m):
            fails.append("positive control %r was not caught" % name)
    pool_controls = {
        "slot NFS rule removed": lambda p: p["Resources"].pop("SlotEfsIngress"),
        "slot NFS rule on another port": lambda p: p["Resources"]["SlotEfsIngress"]["Properties"]
        .__setitem__("FromPort", 0),
    }
    for name, m in pool_controls.items():
        if not broken_pool(m):
            fails.append("positive control %r was not caught" % name)
    controls.update(pool_controls)
    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1
    print("cfn-efs-policy-test: ok (%d positive controls caught)" % len(controls))
    return 0


if __name__ == "__main__":
    sys.exit(main())
