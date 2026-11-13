#!/usr/bin/env python3
"""The Control Plane's own data root on ECS, checked offline against the templates (#4089).

    python3 deploy/local/cfn-cp-data-efs-test.py

## Why this exists

The internal git provider keeps its bare repositories and LFS objects under the CP's data root
(WS_DATA), and for anything a member pushed only there that is the only copy. On ECS the data
root used to be the CP task's own disk, so every task replacement (deploy, scale, failure)
emptied it while the repositories' rows stayed in RDS. 10-data now declares a file system of
the CP's own and 30-ingress mounts it at WS_DATA.

It must stay the CP's alone: the shared FileSystem admits any client that names an access
point, and its mount targets take NFS from every workspace task, so the repositories of every
tenant would be one access-point id away from any workspace. This fails when

  - 10-data loses the file system, its encryption, its access point, its Persistence-driven
    Retain/Delete, or its place in the daily backup selection;
  - its mount targets accept NFS from anything but the CP's security group;
  - its policy admits an anonymous mount, or one without an access point, TLS or a mount
    target, or carries any Allow at all (the CP's identity grant is meant to be the only one);
  - the CP task stops mounting it with TLS and IAM through that access point, WS_DATA stops
    being the mount path, or AF_DATA_DURABLE_REQUIRED is dropped;
  - the CP task role's grant is lost, widened past the access point, or not ordered before
    the task definition.

Positive controls at the end run the same checks against broken copies.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import importlib.util
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("efs_policy", os.path.join(HERE, "cfn-efs-policy-test.py"))
efs = importlib.util.module_from_spec(_spec)
try:
    _spec.loader.exec_module(efs)
except SystemExit as e:  # PyYAML missing
    print(e)
    sys.exit(2)

CP_ROLE = "arn:aws:iam::111122223333:role/af-plat-cp-task"
MOUNT_PATH = "/var/lib/af-cp"


def env_of(container):
    return {e["Name"]: e.get("Value") for e in container.get("Environment", []) if isinstance(e, dict) and "Name" in e}


def check(data, ingress):
    fails = []
    res = data["Resources"]
    fs = res.get("CpDataFileSystem")
    if not fs:
        return ["10-data has no CpDataFileSystem"]
    p = fs["Properties"]
    if p.get("Encrypted") is not True:
        fails.append("CpDataFileSystem must be encrypted")
    if p.get("ThroughputMode") != "elastic":
        fails.append("CpDataFileSystem must be elastic (bursting falls to 1 MiB/s, ADR 0087)")
    retain = {"!If": ["IsRetain", "Retain", "Delete"]}
    if fs.get("DeletionPolicy") != retain or fs.get("UpdateReplacePolicy") != retain:
        fails.append("CpDataFileSystem's Deletion/UpdateReplacePolicy must follow Persistence")

    sel = res["EfsBackupSelection"]["Properties"]["BackupSelection"]["Resources"]
    if not any("${CpDataFileSystem}" in str(r) for r in sel):
        fails.append("the daily backup selection does not cover CpDataFileSystem")

    sg = res.get("CpDataEfsSg", {}).get("Properties", {})
    ingress_rules = sg.get("SecurityGroupIngress", [])
    if not ingress_rules:
        fails.append("CpDataEfsSg has no ingress rule")
    for r in ingress_rules:
        if r.get("FromPort") != 2049 or r.get("ToPort") != 2049 or "CpSgId" not in str(r.get("SourceSecurityGroupId")) \
                or "CidrIp" in r:
            fails.append("CpDataEfsSg must take NFS from the CP's SG only: %r" % r)
    mts = [v["Properties"] for v in res.values()
           if v.get("Type") == "AWS::EFS::MountTarget" and v["Properties"].get("FileSystemId") == {"!Ref": "CpDataFileSystem"}]
    if len(mts) < 2:
        fails.append("CpDataFileSystem needs a mount target in each private subnet, found %d" % len(mts))
    for mt in mts:
        if mt.get("SecurityGroups") != [{"!Ref": "CpDataEfsSg"}]:
            fails.append("a CpDataFileSystem mount target uses %r, not CpDataEfsSg alone" % mt.get("SecurityGroups"))

    ap = res.get("CpDataAccessPoint", {}).get("Properties", {})
    if ap.get("FileSystemId") != {"!Ref": "CpDataFileSystem"} or ap.get("RootDirectory", {}).get("Path") != "/cp-data":
        fails.append("CpDataAccessPoint must root CpDataFileSystem at /cp-data: %r" % ap)
    if ap.get("PosixUser") != {"Uid": "0", "Gid": "0"}:
        fails.append("CpDataAccessPoint must present uid/gid 0 (git refuses a repository owned by another uid)")

    pol = p.get("FileSystemPolicy")
    if not pol:
        fails.append("CpDataFileSystem has no FileSystemPolicy (without one EFS admits every client)")
    else:
        if any(st["Effect"] == "Allow" for st in pol["Statement"]):
            fails.append("CpDataFileSystem's policy must carry no Allow; the CP's identity grant is the only one")
        cases = [
            ("anonymous, access point, TLS", efs.mount(), False, False),
            ("anonymous, no access point", efs.mount(ap=False), False, False),
            ("CP role (identity grant), access point, TLS", efs.mount(role=CP_ROLE), True, True),
            ("CP role without an access point", efs.mount(ap=False, role=CP_ROLE), False, True),
            ("CP role without TLS", efs.mount(tls=False, role=CP_ROLE), False, True),
            ("CP role not through a mount target", efs.mount(mt=False, role=CP_ROLE), False, True),
        ]
        for what, ctx, want, ident in cases:
            for action in sorted(efs.CLIENT):
                try:
                    got = efs.allowed(pol, action, ctx, ident)
                except efs.UnsupportedKey as e:
                    return fails + ["condition key %s is not enforced by EFS for NFS clients" % e]
                if got != want:
                    fails.append("%s: %s allowed=%s, want %s" % (what, action, got, want))

    outs = data.get("Outputs", {})
    for name in ("CpDataEfsId", "CpDataAccessPointId", "CpDataAccessPointArn"):
        if outs.get(name, {}).get("Export") is None:
            fails.append("10-data must export %s" % name)

    ires = ingress["Resources"]
    td = ires["TaskDef"]
    tp = td["Properties"]
    vols = {v["Name"]: v for v in tp.get("Volumes", [])}
    vol = vols.get("cp-data", {}).get("EFSVolumeConfiguration")
    if not vol:
        fails.append("the CP task definition has no cp-data EFS volume")
    else:
        if "CpDataEfsId" not in str(vol.get("FilesystemId")):
            fails.append("the cp-data volume does not mount 10-data's CpDataEfsId")
        if vol.get("TransitEncryption") != "ENABLED":
            fails.append("the cp-data volume must use TLS")
        auth = vol.get("AuthorizationConfig", {})
        if auth.get("IAM") != "ENABLED" or "CpDataAccessPointId" not in str(auth.get("AccessPointId")):
            fails.append("the cp-data volume must mount through CpDataAccessPointId with IAM: %r" % auth)
    cp = next((c for c in tp["ContainerDefinitions"] if c["Name"] == "cp"), None)
    mps = [m for m in (cp or {}).get("MountPoints", []) if m.get("SourceVolume") == "cp-data"]
    if not mps or mps[0].get("ContainerPath") != MOUNT_PATH or mps[0].get("ReadOnly"):
        fails.append("the cp container must mount cp-data read-write at %s: %r" % (MOUNT_PATH, mps))
    env = env_of(cp or {})
    if env.get("WS_DATA") != MOUNT_PATH:
        fails.append("WS_DATA is %r, not the cp-data mount %s" % (env.get("WS_DATA"), MOUNT_PATH))
    if env.get("AF_DATA_DURABLE_REQUIRED") != "1":
        fails.append("the CP environment must set AF_DATA_DURABLE_REQUIRED=1")

    grant = ires.get("CpDataEfsMount", {})
    gp = grant.get("Properties", {})
    if "CpTaskRoleArn" not in str(gp.get("Roles")):
        fails.append("CpDataEfsMount must attach to the CP task role")
    sts = gp.get("PolicyDocument", {}).get("Statement", [])
    ok = False
    for st in sts:
        acts = set(efs.as_list(st.get("Action", [])))
        if st.get("Effect") != "Allow" or not acts & efs.CLIENT:
            continue
        if "CpDataEfsId" not in str(st.get("Resource")) or "*" in str(st.get("Resource")).replace("${", ""):
            fails.append("CpDataEfsMount must name CpDataFileSystem alone: %r" % st.get("Resource"))
        cond = st.get("Condition", {}).get("StringEquals", {}).get("elasticfilesystem:AccessPointArn")
        if "CpDataAccessPointArn" not in str(cond):
            fails.append("CpDataEfsMount must be bound to CpDataAccessPointArn")
        ok = acts == efs.CLIENT
    if not ok:
        fails.append("CpDataEfsMount must grant ClientMount, ClientWrite and ClientRootAccess")
    if "CpDataEfsMount" not in efs.as_list(td.get("DependsOn", [])):
        fails.append("TaskDef must DependsOn CpDataEfsMount, or a first task can start without the grant")
    return fails


def main():
    data, ingress = efs.load("10-data.yaml"), efs.load("30-ingress.yaml")
    fails = check(data, ingress)
    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1

    def broken(mutate):
        d, i = copy.deepcopy(data), copy.deepcopy(ingress)
        mutate(d, i)
        return check(d, i)

    def fsp(d):
        return d["Resources"]["CpDataFileSystem"]["Properties"]

    def stmts(d):
        return fsp(d)["FileSystemPolicy"]["Statement"]

    def drop(sid):
        return lambda d, i: stmts(d).__setitem__(slice(None), [s for s in stmts(d) if s["Sid"] != sid])

    def cp(i):
        return next(c for c in i["Resources"]["TaskDef"]["Properties"]["ContainerDefinitions"] if c["Name"] == "cp")

    def set_env(name, value):
        def m(d, i):
            for e in cp(i)["Environment"]:
                if isinstance(e, dict) and e.get("Name") == name:
                    e["Value"] = value
        return m

    def grant(i):
        return i["Resources"]["CpDataEfsMount"]["Properties"]["PolicyDocument"]["Statement"][0]

    controls = {
        "file system removed": lambda d, i: d["Resources"].pop("CpDataFileSystem"),
        "not encrypted": lambda d, i: fsp(d).__setitem__("Encrypted", False),
        "always deleted": lambda d, i: d["Resources"]["CpDataFileSystem"].__setitem__("DeletionPolicy", "Delete"),
        "not backed up": lambda d, i: d["Resources"]["EfsBackupSelection"]["Properties"]["BackupSelection"][
            "Resources"].pop(),
        "NFS from workspaces": lambda d, i: d["Resources"]["CpDataEfsSg"]["Properties"]["SecurityGroupIngress"][0]
        .__setitem__("SourceSecurityGroupId", {"!ImportValue": {"!Sub": "${NetworkStackName}-WsSgId"}}),
        "mount target on the shared SG": lambda d, i: d["Resources"]["CpDataMountTarget0"]["Properties"]
        .__setitem__("SecurityGroups", [{"!Ref": "EfsSg"}]),
        "policy removed": lambda d, i: fsp(d).pop("FileSystemPolicy"),
        "TLS deny removed": drop("DenyWithoutTls"),
        "access point deny removed": drop("DenyWithoutAccessPoint"),
        "mount target deny removed": drop("DenyOutsideMountTarget"),
        "anonymous allow added": lambda d, i: stmts(d).append({
            "Sid": "Open", "Effect": "Allow", "Principal": {"AWS": "*"}, "Action": sorted(efs.CLIENT),
            "Condition": {"Bool": {"elasticfilesystem:AccessedViaMountTarget": "true"}}}),
        "access point at the root": lambda d, i: d["Resources"]["CpDataAccessPoint"]["Properties"]["RootDirectory"]
        .__setitem__("Path", "/"),
        "no volume": lambda d, i: i["Resources"]["TaskDef"]["Properties"].pop("Volumes"),
        "volume without TLS": lambda d, i: i["Resources"]["TaskDef"]["Properties"]["Volumes"][0][
            "EFSVolumeConfiguration"].__setitem__("TransitEncryption", "DISABLED"),
        "volume without IAM": lambda d, i: i["Resources"]["TaskDef"]["Properties"]["Volumes"][0][
            "EFSVolumeConfiguration"]["AuthorizationConfig"].pop("IAM"),
        "no mount point": lambda d, i: cp(i).pop("MountPoints"),
        "WS_DATA back on /tmp": set_env("WS_DATA", "/tmp"),
        "durability flag dropped": set_env("AF_DATA_DURABLE_REQUIRED", "0"),
        "grant removed": lambda d, i: i["Resources"].pop("CpDataEfsMount"),
        "grant on every file system": lambda d, i: grant(i).__setitem__(
            "Resource", "arn:aws:elasticfilesystem:r:a:file-system/*"),
        "grant without the access point condition": lambda d, i: grant(i).pop("Condition"),
        "grant without root access": lambda d, i: grant(i).__setitem__(
            "Action", ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"]),
        "task not ordered after the grant": lambda d, i: i["Resources"]["TaskDef"].__setitem__(
            "DependsOn", ["HomeOpsTaskDef", "CpHomeOpsManagedPolicy"]),
    }
    for name, m in controls.items():
        if not broken(m):
            fails.append("positive control %r was not caught" % name)
    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1
    print("cfn-cp-data-efs-test: ok (%d positive controls caught)" % len(controls))
    return 0


if __name__ == "__main__":
    sys.exit(main())
