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

Each rule is shown failing on a mutated copy of the templates (positive controls).
"""
import copy
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("dek", os.path.join(HERE, "cfn-workspace-dek-test.py"))
dek = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dek)

ACTIONS = {"ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssm:GetParameterHistory"}
HAND_APPLIED = "af-deny-af-parameters"


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


def main():
    t = {n: load(n + ".yaml") for n in ("20-platform", "30-ingress", "40-ec2-pool", "50-tts", "60-engines")}
    fails = check(t)

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
    for name, fn in controls.items():
        if not check(mut(fn)):
            fails.append("positive control %r was not caught" % name)
    if fails:
        for x in fails:
            print("FAIL:", x)
        return 1
    print("cfn-exec-role-split-test: ok (%d positive controls caught)" % len(controls))
    return 0


if __name__ == "__main__":
    sys.exit(main())
