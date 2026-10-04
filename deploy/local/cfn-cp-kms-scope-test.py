#!/usr/bin/env python3
"""The kms key custodian's grants, checked against the Go that uses them, offline.

    python3 deploy/local/cfn-cp-kms-scope-test.py

## Why this exists

The kms custodian (control-plane/custodian_kms.go, ADR 0005) sends every GenerateDataKey and
Decrypt with an encryption context, and the deploy templates key the Control Plane task
role's grant (30-ingress CpCustodianKmsPolicy) and the key policy's deny (10-data
CustodianKey) on those exact names. A rename on either side shows up only on a real
deployment, as AccessDenied on every seal and every open: the CP fails closed, so nothing is
lost, but nothing works either. And a grant widened to "*" or to any context would let the
CP role decrypt with keys it has no business with. So this check fails when

  - the CP role's kms grant is not exactly GenerateDataKey + Decrypt, on the parameter's key
    ARN, with the purpose value, a key_ref present, and no other context keys;
  - any other statement attached to the CP task role names a kms action;
  - the key policy stops denying the crypto calls outside the custodian's context, or the key
    loses rotation or its Persistence-following deletion policy;
  - the context names and values here differ from the Go constants, or the env names the
    template sets differ from the ones the CP reads;
  - CustodianKmsKeyArn's pattern accepts an alias ARN or a wildcard (IAM scopes a key's
    calls by key ARN only, so an alias there is a grant that matches nothing).

The positive controls at the end run the same checks against broken copies, to show each
can fail at all.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import os
import re
import sys

try:
    import yaml
except ImportError:
    sys.exit("cfn-cp-kms-scope-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CFN = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn")
GO = os.path.join(ROOT, "control-plane", "custodian_kms.go")
MAIN = os.path.join(ROOT, "control-plane", "main.go")


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


def go_consts():
    src = open(GO).read()
    out = {}
    for name in ("kmsContextPurposeKey", "kmsContextPurpose", "kmsContextKeyRefKey"):
        m = re.search(r'\b%s\s*=\s*"([^"]+)"' % name, src)
        if not m:
            raise SystemExit("cfn-cp-kms-scope-test: %s not found in %s" % (name, GO))
        out[name] = m.group(1)
    return out


def as_list(v):
    return v if isinstance(v, list) else [v]


def imports_cp_task_role(roles):
    return "CpTaskRoleArn" in repr(roles)


def cp_role_statements(templates):
    """(template, policy name, statement) for everything attached to the CP task role."""
    out = []
    for tname, t in templates.items():
        for rname, r in (t.get("Resources") or {}).items():
            props = r.get("Properties") or {}
            if r.get("Type") == "AWS::IAM::Role" and rname == "CpTaskRole":
                for p in props.get("Policies") or []:
                    for s in p["PolicyDocument"]["Statement"]:
                        out.append((tname, rname + "/" + p["PolicyName"], s))
            elif r.get("Type") in ("AWS::IAM::ManagedPolicy", "AWS::IAM::Policy") \
                    and imports_cp_task_role(props.get("Roles")):
                for s in props["PolicyDocument"]["Statement"]:
                    out.append((tname, rname, s))
    return out


def check(templates, consts, main_src, go_src):
    fails = []
    purpose_key = "kms:EncryptionContext:" + consts["kmsContextPurposeKey"]
    keyref_key = "kms:EncryptionContext:" + consts["kmsContextKeyRefKey"]
    ing = templates["30-ingress.yaml"]
    data = templates["10-data.yaml"]

    # --- the CP role's grant ---
    pol = (ing.get("Resources") or {}).get("CpCustodianKmsPolicy")
    if not pol:
        return ["30-ingress: CpCustodianKmsPolicy is missing"]
    if pol.get("Condition") != "UseKmsCustodian":
        fails.append("CpCustodianKmsPolicy is not conditioned on UseKmsCustodian")
    stmts = pol["Properties"]["PolicyDocument"]["Statement"]
    if len(stmts) != 1:
        fails.append("CpCustodianKmsPolicy has %d statements, want 1" % len(stmts))
    s = stmts[0]
    if s.get("Effect") != "Allow":
        fails.append("CpCustodianKmsPolicy statement is not an Allow")
    if sorted(as_list(s.get("Action"))) != ["kms:Decrypt", "kms:GenerateDataKey"]:
        fails.append("CpCustodianKmsPolicy actions = %r, want GenerateDataKey + Decrypt" % s.get("Action"))
    if s.get("Resource") != {"!Ref": "CustodianKmsKeyArn"}:
        fails.append("CpCustodianKmsPolicy resource = %r, want !Ref CustodianKmsKeyArn" % s.get("Resource"))
    cond = s.get("Condition") or {}
    if (cond.get("StringEquals") or {}).get(purpose_key) != consts["kmsContextPurpose"]:
        fails.append("CpCustodianKmsPolicy does not require %s = %s" % (purpose_key, consts["kmsContextPurpose"]))
    if (cond.get("Null") or {}).get(keyref_key) != "false":
        fails.append("CpCustodianKmsPolicy does not require %s to be present" % keyref_key)
    keys = (cond.get("ForAllValues:StringEquals") or {}).get("kms:EncryptionContextKeys")
    if sorted(as_list(keys or [])) != sorted([consts["kmsContextPurposeKey"], consts["kmsContextKeyRefKey"]]):
        fails.append("CpCustodianKmsPolicy context keys = %r, want exactly the custodian's two" % keys)
    if s not in [st for _, _, st in cp_role_statements({"30-ingress.yaml": ing})]:
        fails.append("CpCustodianKmsPolicy is not attached to the CP task role")

    # --- nothing else gives the CP role kms ---
    for tname, pname, st in cp_role_statements(templates):
        if pname == "CpCustodianKmsPolicy":
            continue
        for a in as_list(st.get("Action")):
            if str(a).lower().startswith("kms:") or a == "*":
                fails.append("%s %s grants %s to the CP task role" % (tname, pname, a))

    # --- the parameter ---
    param = ing["Parameters"].get("CustodianKmsKeyArn") or {}
    pat = param.get("AllowedPattern")
    if param.get("Default", None) != "" or not pat:
        fails.append("CustodianKmsKeyArn must default to empty and carry an AllowedPattern")
    else:
        rx = re.compile(pat)
        for ok in ("", "arn:aws:kms:ap-northeast-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"):
            if not rx.fullmatch(ok):
                fails.append("CustodianKmsKeyArn pattern refuses %r" % ok)
        for bad in ("*", "arn:aws:kms:ap-northeast-1:111122223333:alias/af", "arn:aws:kms:*:*:key/*",
                    "alias/af"):
            if rx.fullmatch(bad):
                fails.append("CustodianKmsKeyArn pattern accepts %r" % bad)

    # --- the env the CP reads ---
    env = repr(ing["Resources"]["TaskDef"]["Properties"]["ContainerDefinitions"][0]["Environment"])
    for name in ("AF_KEY_CUSTODIAN", "AF_KMS_KEY_ID"):
        if name not in env:
            fails.append("30-ingress TaskDef does not set %s" % name)
        if name not in main_src + go_src:
            fails.append("the CP does not read %s" % name)
    if "{'!Ref': 'CustodianKmsKeyArn'}" not in env:
        fails.append("AF_KMS_KEY_ID is not the CustodianKmsKeyArn parameter")

    # --- the key ---
    key = (data.get("Resources") or {}).get("CustodianKey")
    if not key:
        return fails + ["10-data: CustodianKey is missing"]
    if key.get("DeletionPolicy") != {"!If": ["IsRetain", "Retain", "Delete"]}:
        fails.append("CustodianKey DeletionPolicy = %r, want to follow Persistence" % key.get("DeletionPolicy"))
    kp = key["Properties"]
    if kp.get("EnableKeyRotation") is not True:
        fails.append("CustodianKey rotation is off")
    denies = [st for st in kp["KeyPolicy"]["Statement"] if st.get("Effect") == "Deny"]
    ok = False
    for st in denies:
        acts = as_list(st.get("Action"))
        if st.get("Principal") == "*" and "kms:Decrypt" in acts and "kms:GenerateDataKey*" in acts \
                and (st.get("Condition") or {}).get("StringNotEquals", {}).get(purpose_key) == consts["kmsContextPurpose"]:
            ok = True
    if not ok:
        fails.append("CustodianKey policy does not deny Decrypt / GenerateDataKey* outside %s = %s"
                     % (purpose_key, consts["kmsContextPurpose"]))
    return fails


def main():
    try:
        templates = {n: load(n) for n in sorted(os.listdir(CFN)) if n.endswith(".yaml")}
        consts = go_consts()
        main_src, go_src = open(MAIN).read(), open(GO).read()
    except (OSError, yaml.YAMLError) as e:
        print("cfn-cp-kms-scope-test: cannot run: %s" % e)
        return 2

    failed = False
    fails = check(templates, consts, main_src, go_src)
    for f in fails:
        print("FAIL  " + f)
    failed |= bool(fails)
    if not fails:
        print("ok    the CP role's kms grant, the key policy and the Go agree")

    def mutated(fn):
        t = copy.deepcopy(templates)
        fn(t)
        return t

    def stmt(t):
        return t["30-ingress.yaml"]["Resources"]["CpCustodianKmsPolicy"]["Properties"]["PolicyDocument"]["Statement"][0]

    controls = [
        ("resource widened to *", mutated(lambda t: stmt(t).update(Resource="*")), consts),
        ("kms:* granted", mutated(lambda t: stmt(t).update(Action="kms:*")), consts),
        ("context condition dropped", mutated(lambda t: stmt(t).pop("Condition")), consts),
        ("Go renames the purpose", templates, dict(consts, kmsContextPurpose="renamed")),
        ("Go renames key_ref", templates, dict(consts, kmsContextKeyRefKey="af:tenant")),
        ("key policy deny dropped", mutated(lambda t: t["10-data.yaml"]["Resources"]["CustodianKey"]["Properties"]
                                            ["KeyPolicy"]["Statement"].pop()), consts),
        ("key deleted with the stack always", mutated(lambda t: t["10-data.yaml"]["Resources"]["CustodianKey"]
                                                      .update(DeletionPolicy="Delete")), consts),
        ("pattern accepts aliases", mutated(lambda t: t["30-ingress.yaml"]["Parameters"]["CustodianKmsKeyArn"]
                                            .update(AllowedPattern="^(arn:aws:kms:.*)?$")), consts),
        ("another statement grants kms", mutated(lambda t: t["20-platform.yaml"]["Resources"]["CpTaskRole"]
                                                 ["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]
                                                 .append({"Effect": "Allow", "Action": "kms:Decrypt", "Resource": "*"})),
         consts),
    ]
    for name, t, c in controls:
        if check(t, c, main_src, go_src):
            print("ok    control: %s fails" % name)
        else:
            print("FAIL  control: %s passed - the check cannot see it" % name)
            failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
