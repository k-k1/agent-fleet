#!/usr/bin/env python3
"""The execution-role split and the slot role's parameter Deny (#3446), read from the templates.

  - 40-ec2-pool's SlotRole carries an explicit Deny of the four ssm:GetParameter* actions on
    /af-cp and /af-ws (and their children), under an inline policy name that is not the
    hand-applied `af-deny-af-parameters`;
  - WsExecRole reads /af-ws and nothing under /af-cp; ExecRole (the CP's) keeps /af-cp and, for
    one release, /af-ws (running services still name it); neither carries a Deny (the CP's own secret injection goes through ExecRole);
  - the CP task definition keeps ExecRoleArn, AF_ECS_EXEC_ROLE and the 50-tts / 60-engines
    task definitions import WsExecRoleArn, and the CP may pass WsExecRole;
  - the ExecRoleArn export stays, so no stack update removes an export that is still imported.

The optional customer-managed key for /af-cp/master-key (#3470) is checked in both states:
  - MasterKeyKmsKeyArn defaults to empty and only accepts a key ARN (no alias, no wildcard);
  - empty: ExecRole carries no kms statement (the `!If` resolves to nothing);
  - set: ExecRole carries exactly kms:Decrypt on that parameter's ARN, only via SSM, for the
    master-key parameter's encryption context; no other role in 20-platform names a kms action.

Each rule is shown failing on a mutated copy of the templates (positive controls).
"""
import copy
import importlib.util
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("dek", os.path.join(HERE, "cfn-workspace-dek-test.py"))
dek = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dek)

ACTIONS = {"ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssm:GetParameterHistory"}
HAND_APPLIED = "af-deny-af-parameters"
KEY_DEFAULT = "arn:aws:kms:ap-northeast-1:123456789012:key/1234abcd"


def load(name):
    with open(os.path.join(dek.ROOT, "deploy", "aws", "ecs", "cfn", name)) as f:
        return dek.yaml.load(f, Loader=dek.CfnLoader)


def as_list(x):
    return x if isinstance(x, list) else [x]


def text(x):
    return x.get("!Sub") if isinstance(x, dict) and "!Sub" in x else json.dumps(x)


def ssm_resources(role):
    out = []
    for p in role["Properties"].get("Policies", []):
        for st in as_list(p["PolicyDocument"]["Statement"]):
            if st.get("Effect") == "Allow" and any(a.startswith("ssm:") for a in as_list(st["Action"])):
                out += [text(r) for r in as_list(st["Resource"])]
    return out


def has_deny(role):
    return any(st.get("Effect") == "Deny"
               for p in role["Properties"].get("Policies", [])
               for st in as_list(p["PolicyDocument"]["Statement"]))


def imports(x):
    return json.dumps(x)


def check(t):
    f = []
    plat, ingress, pool = t["20-platform"], t["30-ingress"], t["40-ec2-pool"]
    tts, eng = t["50-tts"], t["60-engines"]
    R = plat["Resources"]

    # --- slot role Deny ---
    slot = pool["Resources"]["SlotRole"]["Properties"]
    pols = slot.get("Policies", [])
    if not pols:
        f.append("SlotRole has no inline policy")
    for p in pols:
        if p["PolicyName"] == HAND_APPLIED:
            f.append("SlotRole policy reuses the hand-applied name %s" % HAND_APPLIED)
    denied = set()
    resources = set()
    for p in pols:
        for st in as_list(p["PolicyDocument"]["Statement"]):
            if st.get("Effect") == "Deny" and ACTIONS <= set(as_list(st["Action"])):
                denied |= set(as_list(st["Action"]))
                resources |= {text(r).split("parameter", 1)[1] for r in as_list(st["Resource"])}
    if not ACTIONS <= denied:
        f.append("SlotRole does not Deny all of %s" % sorted(ACTIONS - denied))
    for need in ("/af-cp", "/af-cp/*", "/af-ws", "/af-ws/*"):
        if need not in resources:
            f.append("SlotRole Deny does not cover parameter%s" % need)

    # --- exec roles ---
    if "WsExecRole" not in R:
        return f + ["20-platform has no WsExecRole"]
    cp_res, ws_res = ssm_resources(R["ExecRole"]), ssm_resources(R["WsExecRole"])
    if not any(r.endswith("parameter/af-ws/*") for r in cp_res):
        f.append("ExecRole must keep /af-ws/* for one release (running services still name it)")
    for pol in ("ExecHfTokenPolicy", "ExecCivitaiTokenPolicy"):
        if len(t["60-engines"]["Resources"][pol]["Properties"]["Roles"]) != 2:
            f.append("%s must attach to both execution roles" % pol)
    if not any(r.endswith("parameter/af-cp/*") for r in cp_res):
        f.append("ExecRole lost /af-cp/* (the CP would not start)")
    if not any(r.endswith("parameter/af-ws/*") for r in ws_res) or any("/af-cp" in r for r in ws_res):
        f.append("WsExecRole must read /af-ws/* and nothing under /af-cp: %r" % ws_res)
    for n in ("ExecRole", "WsExecRole"):
        if has_deny(R[n]):
            f.append("%s carries a Deny" % n)
    if "ExecRoleArn" not in plat["Outputs"] or "WsExecRoleArn" not in plat["Outputs"]:
        f.append("20-platform must export both ExecRoleArn and WsExecRoleArn")
    passed = json.dumps(R["CpTaskRole"]["Properties"]["Policies"])
    if "WsExecRole.Arn" not in passed:
        f.append("CpTaskRole cannot iam:PassRole WsExecRole")

    # --- wiring ---
    iR = ingress["Resources"]
    if "ExecRoleArn" not in imports(iR["TaskDef"]["Properties"]["ExecutionRoleArn"]) \
            or "WsExec" in imports(iR["TaskDef"]["Properties"]["ExecutionRoleArn"]):
        f.append("the CP task definition must run as ExecRole")
    env = [e for c in iR["TaskDef"]["Properties"]["ContainerDefinitions"] for e in c.get("Environment", [])
           if isinstance(e, dict) and e.get("Name") == "AF_ECS_EXEC_ROLE"]
    if len(env) != 1 or "WsExecRoleArn" not in imports(env[0]["Value"]):
        f.append("AF_ECS_EXEC_ROLE must import WsExecRoleArn")
    for name, tpl in (("50-tts", tts), ("60-engines", eng)):
        for rid, r in tpl["Resources"].items():
            if r["Type"] == "AWS::ECS::TaskDefinition":
                v = imports(r["Properties"].get("ExecutionRoleArn"))
                if "WsExecRoleArn" not in v:
                    f.append("%s %s does not run as WsExecRole" % (name, rid))
    return f


def resolve_if(stmts, on):
    """The Statement list as CloudFormation would build it with HasMasterKeyCmk true / false."""
    out = []
    for st in stmts:
        if isinstance(st, dict) and "!If" in st:
            cond, yes, no = st["!If"]
            if cond != "HasMasterKeyCmk":
                out.append(st)
                continue
            st = yes if on else no
            if st == {"!Ref": "AWS::NoValue"}:
                continue
        out.append(st)
    return out


def check_cmk(t):
    f = []
    plat = t["20-platform"]
    p = plat["Parameters"].get("MasterKeyKmsKeyArn")
    if not p:
        return ["20-platform has no MasterKeyKmsKeyArn parameter"]
    if p.get("Default") != "":
        f.append("MasterKeyKmsKeyArn must default to empty (today's behaviour)")
    pat = re.compile(p.get("AllowedPattern", "^$"))
    key = "arn:aws:kms:ap-northeast-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"
    for ok in ("", key):
        if not pat.match(ok):
            f.append("MasterKeyKmsKeyArn pattern refuses %r" % ok)
    for bad in (key.replace("key/", "alias/"), key.replace("1234abcd-12ab", "*"), key + "/*"):
        if pat.match(bad):
            f.append("MasterKeyKmsKeyArn pattern accepts %r" % bad)
    if plat["Conditions"].get("HasMasterKeyCmk") != {"!Not": [{"!Equals": [{"!Ref": "MasterKeyKmsKeyArn"}, ""]}]}:
        f.append("HasMasterKeyCmk is not MasterKeyKmsKeyArn != empty")
    role = plat["Resources"]["ExecRole"]["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]
    off = [st for st in resolve_if(role, False) if "kms:Decrypt" in json.dumps(st)]
    if off:
        f.append("ExecRole names kms with MasterKeyKmsKeyArn empty: %r" % off)
    on = [st for st in resolve_if(role, True) if "kms:" in json.dumps(st)]
    if len(on) != 1:
        return f + ["ExecRole must carry exactly one kms statement when set, got %d" % len(on)]
    st = on[0]
    if st.get("Effect") != "Allow" or as_list(st.get("Action")) != ["kms:Decrypt"]:
        f.append("the kms grant is not Allow kms:Decrypt only: %r" % st.get("Action"))
    if st.get("Resource") != {"!Ref": "MasterKeyKmsKeyArn"}:
        f.append("the kms grant does not name exactly MasterKeyKmsKeyArn: %r" % st.get("Resource"))
    cond = st.get("Condition", {}).get("StringEquals", {})
    if cond.get("kms:ViaService") != {"!Sub": "ssm.${AWS::Region}.amazonaws.com"}:
        f.append("the kms grant is not limited to kms:ViaService ssm.<region>")
    ctx = cond.get("kms:EncryptionContext:PARAMETER_ARN")
    if not (isinstance(ctx, dict) and ctx.get("!Sub", "").endswith("parameter/af-cp/master-key")):
        f.append("the kms grant is not limited to the master-key parameter's encryption context")
    for rid in ("WsExecRole", "CpTaskRole", "WsTaskRole"):
        if "kms:" in json.dumps(plat["Resources"][rid]["Properties"].get("Policies", [])):
            f.append("%s names a kms action (only ExecRole decrypts the master key)" % rid)
    return f


def main():
    t = {n: load(n + ".yaml") for n in ("20-platform", "30-ingress", "40-ec2-pool", "50-tts", "60-engines")}
    fails = check(t) + check_cmk(t)

    def mut(fn):
        c = copy.deepcopy(t)
        fn(c)
        return c

    def ssm_stmt(c, role):
        return c["20-platform"]["Resources"][role]["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]

    controls = {
        "slot Deny removed": lambda c: c["40-ec2-pool"]["Resources"]["SlotRole"]["Properties"].pop("Policies"),
        "slot policy renamed to the hand-applied one": lambda c: c["40-ec2-pool"]["Resources"]["SlotRole"][
            "Properties"]["Policies"][0].update(PolicyName=HAND_APPLIED),
        "slot Deny drops GetParameterHistory": lambda c: c["40-ec2-pool"]["Resources"]["SlotRole"]["Properties"][
            "Policies"][0]["PolicyDocument"]["Statement"][0]["Action"].remove("ssm:GetParameterHistory"),
        "slot Deny drops /af-ws": lambda c: c["40-ec2-pool"]["Resources"]["SlotRole"]["Properties"][
            "Policies"][0]["PolicyDocument"]["Statement"][0].update(Resource=[
                {"!Sub": "arn:x:ssm:*:1:parameter/af-cp"}, {"!Sub": "arn:x:ssm:*:1:parameter/af-cp/*"}]),
        "ExecRole drops /af-ws too early": lambda c: ssm_stmt(c, "ExecRole")[0]["Resource"].pop(0),
        "engine token policy only on WsExecRole": lambda c: c["60-engines"]["Resources"][
            "ExecHfTokenPolicy"]["Properties"]["Roles"].pop(),
        "WsExecRole gains /af-cp": lambda c: ssm_stmt(c, "WsExecRole")[0]["Resource"].append(
            {"!Sub": "arn:x:ssm:r:1:parameter/af-cp/*"}),
        "ExecRole gets a Deny": lambda c: ssm_stmt(c, "ExecRole").append(
            {"Effect": "Deny", "Action": ["ssm:GetParameters"], "Resource": "*"}),
        "ExecRoleArn export dropped": lambda c: c["20-platform"]["Outputs"].pop("ExecRoleArn"),
        "AF_ECS_EXEC_ROLE back on ExecRoleArn": lambda c: [
            e.update(Value={"!ImportValue": {"Fn::Sub": "${PlatformStackName}-ExecRoleArn"}})
            for ct in c["30-ingress"]["Resources"]["TaskDef"]["Properties"]["ContainerDefinitions"]
            for e in ct.get("Environment", []) if isinstance(e, dict) and e.get("Name") == "AF_ECS_EXEC_ROLE"],
        "voicevox back on ExecRoleArn": lambda c: c["50-tts"]["Resources"]["TaskDef"]["Properties"].update(
            ExecutionRoleArn={"!ImportValue": {"Fn::Sub": "${PlatformStackName}-ExecRoleArn"}}),
        "PassRole for WsExecRole dropped": lambda c: c["20-platform"]["Resources"]["CpTaskRole"]["Properties"].update(
            Policies=json.loads(json.dumps(c["20-platform"]["Resources"]["CpTaskRole"]["Properties"][
                "Policies"]).replace("WsExecRole.Arn", "WsTaskRole.Arn"))),
    }
    def cmk(c):
        return c["20-platform"]["Resources"]["ExecRole"]["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]

    def kms_stmt(c):
        return [x for x in cmk(c) if isinstance(x, dict) and "!If" in x][0]["!If"][1]

    cmk_controls = {
        "grant unconditional (empty also gets it)": lambda c: cmk(c).__setitem__(
            slice(None), [x["!If"][1] if isinstance(x, dict) and "!If" in x else x for x in cmk(c)]),
        "grant on every key": lambda c: kms_stmt(c).update(Resource="*"),
        "grant adds kms:Encrypt": lambda c: kms_stmt(c).update(Action=["kms:Decrypt", "kms:Encrypt"]),
        "ViaService dropped": lambda c: kms_stmt(c)["Condition"]["StringEquals"].pop("kms:ViaService"),
        "encryption context dropped": lambda c: kms_stmt(c)["Condition"]["StringEquals"].pop(
            "kms:EncryptionContext:PARAMETER_ARN"),
        "grant given to WsExecRole too": lambda c: c["20-platform"]["Resources"]["WsExecRole"]["Properties"][
            "Policies"][0]["PolicyDocument"]["Statement"].append({"Effect": "Allow", "Action": ["kms:Decrypt"], "Resource": "*"}),
        "parameter has a non-empty default": lambda c: c["20-platform"]["Parameters"]["MasterKeyKmsKeyArn"].update(
            Default=KEY_DEFAULT),
        "pattern accepts an alias": lambda c: c["20-platform"]["Parameters"]["MasterKeyKmsKeyArn"].update(
            AllowedPattern="^(arn:aws:kms:.*)?$"),
    }
    for name, fn in controls.items():
        if not check(mut(fn)):
            fails.append("positive control %r was not caught" % name)
    for name, fn in cmk_controls.items():
        if not check_cmk(mut(fn)):
            fails.append("positive control %r was not caught" % name)
    if fails:
        for x in fails:
            print("FAIL:", x)
        return 1
    print("cfn-exec-role-split-test: ok (%d positive controls caught)" % (len(controls) + len(cmk_controls)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
