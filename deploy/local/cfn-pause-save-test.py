#!/usr/bin/env python3
"""What pause.sh saves money with, checked against the templates and the Lambda, offline.

    python3 deploy/local/cfn-pause-save-test.py

## Why this exists

pause.sh deletes the NAT gateway through 00-network's NatEnabled and keeps a stopped RDS
instance stopped through 10-data's re-stop Lambda. Both break without a sound on a real
deployment: a NAT gateway left outside the condition is re-created by every update, and a
Lambda that stops a database it should not leaves a running Control Plane with no database.
So this fails when

  - Nat or the private default route is not behind NatOn, or something else still points at
    Nat unconditionally (the stack would refuse to update while NatEnabled=false);
  - the egress address is no longer retained (NatEip) or no longer exported;
  - the re-stop rule stops matching RDS-EVENT-0154 for this instance, or the role can do more
    than describe / list tags / stop on this one instance;
  - the Lambda stops an instance that lacks the af-paused=true tag pause.sh sets, stops one
    that is not available, or keeps going after the tag is taken off mid-wait.

The positive controls at the end run the same checks against broken copies.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import contextlib
import copy
import io
import os
import re
import sys
import types

try:
    import yaml
except ImportError:
    sys.exit("cfn-pause-save-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CFN = os.path.join(ROOT, "deploy", "aws", "ecs", "cfn")
PAUSE = os.path.join(ROOT, "deploy", "aws", "ecs", "pause.sh")


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


def refs_to(node, name):
    """Does anything under node Ref / GetAtt the logical id `name`?"""
    r = repr(node)
    return ("'!Ref': '%s'" % name) in r or ("'!GetAtt': '%s." % name) in r \
        or ("'!GetAtt': '%s'" % name) in r


class Ctx:
    """The fake boto3 the Lambda runs against, scripted by a list of (status, tags)."""
    def __init__(self, steps):
        self.steps, self.i, self.stopped, self.slept, self.stop_error = steps, 0, 0, 0, ""
        self.stop_cfg = None
        self.tag_cfgs = []

    def client(self, _svc, config=None):
        ctx = self

        class RDS:
            cfg = config

            def describe_db_instances(self, DBInstanceIdentifier):
                st, _ = ctx.steps[min(ctx.i, len(ctx.steps) - 1)]
                return {"DBInstances": [{"DBInstanceStatus": st, "DBInstanceArn": "arn:db"}]}

            def list_tags_for_resource(self, ResourceName):
                ctx.tag_cfgs.append(self.cfg)
                _, tags = ctx.steps[min(ctx.i, len(ctx.steps) - 1)]
                ctx.i += 1  # one poll = one describe + one list
                return {"TagList": [{"Key": k, "Value": v} for k, v in tags.items()]}

            def stop_db_instance(self, DBInstanceIdentifier):
                ctx.stop_cfg = self.cfg
                if ctx.stop_error:
                    raise RuntimeError(ctx.stop_error)
                ctx.stopped += 1
        return RDS()


def run_lambda(code, steps, remaining_ms=900000, stop_error=""):
    ctx = Ctx(steps)
    ctx.stop_error = stop_error
    boto3 = types.ModuleType("boto3")
    boto3.client = ctx.client
    sys.modules["boto3"] = boto3
    # botocore.config.Config, recording what the Lambda asks for.
    botocore, bconfig = types.ModuleType("botocore"), types.ModuleType("botocore.config")
    bconfig.Config = lambda **kw: kw
    botocore.config = bconfig
    sys.modules["botocore"], sys.modules["botocore.config"] = botocore, bconfig
    os.environ["DB_ID"] = "db-1"
    mod = {"__name__": "restop"}
    real_sleep = __import__("time").sleep
    __import__("time").sleep = lambda s: setattr(ctx, "slept", ctx.slept + s)
    try:
        exec(compile(code, "<ZipFile>", "exec"), mod)
        # Time only passes through sleep(), so a loop that never gives up is a loop that
        # runs out of "remaining time" instead of hanging the test.
        lc = types.SimpleNamespace(get_remaining_time_in_millis=lambda: remaining_ms - ctx.slept * 1000)
        try:
            with contextlib.redirect_stdout(io.StringIO()):
                out = mod["handler"]({}, lc)
        except RuntimeError as e:
            out = "raised:" + str(e)
    finally:
        __import__("time").sleep = real_sleep
    return out, ctx


def check(net, data, pause_src):
    f = []
    # --- 00-network
    res, conds = net["Resources"], net.get("Conditions", {})
    if "NatOn" not in conds:
        f.append("00-network: no NatOn condition")
    for rid in ("Nat", "PrivateDefaultRoute"):
        if res[rid].get("Condition") != "NatOn":
            f.append("00-network: %s is not behind NatOn" % rid)
    for rid, r in res.items():
        if rid not in ("Nat", "PrivateDefaultRoute") and refs_to(r, "Nat"):
            f.append("00-network: %s still points at Nat unconditionally" % rid)
    for oid, o in (net.get("Outputs") or {}).items():
        if refs_to(o, "Nat"):
            f.append("00-network: output %s points at Nat" % oid)
    eip = res["NatEip"]
    if eip.get("DeletionPolicy") != "Retain" or eip.get("UpdateReplacePolicy") != "Retain":
        f.append("00-network: NatEip is no longer retained (the egress IP would change on resume)")
    if "NatEipAllocationId" not in net["Outputs"]:
        f.append("00-network: NatEipAllocationId output is gone")
    if conds.get("NatOn") != {"!Equals": [{"!Ref": "NatEnabled"}, "true"]}:
        f.append("00-network: NatOn is not exactly NatEnabled == true (%s)" % conds.get("NatOn"))
    p = net["Parameters"].get("NatEnabled", {})
    if p.get("Default") != "true" or p.get("AllowedValues") != ["true", "false"]:
        f.append("00-network: NatEnabled must default to true and allow only true/false")

    # --- 10-data
    dres = data["Resources"]
    rule = dres["RestopRule"]["Properties"]["EventPattern"]
    if rule.get("source") != ["aws.rds"] or \
            sorted(rule["detail"].get("EventID") or []) != ["RDS-EVENT-0088", "RDS-EVENT-0154"]:
        f.append("10-data: the rule does not match aws.rds RDS-EVENT-0154 and RDS-EVENT-0088")
    if data.get("Conditions", {}).get("RestopOn") != \
            {"!Equals": [{"!Ref": "RdsRestopOnForcedStart"}, "true"]}:
        f.append("10-data: RestopOn is not exactly RdsRestopOnForcedStart == true")
    if rule["detail"].get("SourceIdentifier") != [{"!Ref": "Db"}]:
        f.append("10-data: the rule is not limited to this database")
    stmts = [s for pol in dres["RestopRole"]["Properties"]["Policies"]
             for s in pol["PolicyDocument"]["Statement"]]
    acts = sorted(a for s in stmts for a in as_list(s["Action"]))
    if acts != ["rds:DescribeDBInstances", "rds:ListTagsForResource", "rds:StopDBInstance"]:
        f.append("10-data: the role's actions are %s" % acts)
    if any(not (isinstance(s["Resource"], dict) and ":db:${Db}" in repr(s["Resource"])) for s in stmts):
        f.append("10-data: a role statement is not scoped to the one database")
    for rid in ("RestopRole", "RestopFunction", "RestopLogGroup", "RestopRule", "RestopPermission"):
        if dres[rid].get("Condition") != "RestopOn":
            f.append("10-data: %s is not behind RestopOn" % rid)
    if "af-paused" not in pause_src or "Key=af-paused,Value=true" not in pause_src:
        f.append("pause.sh no longer sets the af-paused=true tag the Lambda looks for")

    # --- the Lambda
    code = dres["RestopFunction"]["Properties"]["Code"]["ZipFile"]
    P = {"af-paused": "true"}
    out, c = run_lambda(code, [("available", P)])
    if (out, c.stopped) != ("stopping", 1):
        f.append("lambda: a tagged available instance was not stopped (%s, %d)" % (out, c.stopped))
    out, c = run_lambda(code, [("available", {})])
    if c.stopped or out != "skipped":
        f.append("lambda: stopped an instance without the af-paused tag (%s)" % out)
    out, c = run_lambda(code, [("available", {"af-paused": "false"})])
    if c.stopped:
        f.append("lambda: stopped an instance tagged af-paused=false")
    for st in ("stopped", "stopping"):
        out, c = run_lambda(code, [(st, P)])
        if c.stopped or out != "already " + st:
            f.append("lambda: did not leave an instance that is already %s alone (%s)" % (st, out))
    out, c = run_lambda(code, [("starting", P), ("starting", P), ("available", P)])
    if c.stopped != 1 or c.slept <= 0:
        f.append("lambda: did not wait for 'starting' to become available (%s, slept %s)" % (out, c.slept))
    out, c = run_lambda(code, [("starting", P), ("available", {})])
    if c.stopped:
        f.append("lambda: kept going after the tag was removed mid-wait")
    out, c = run_lambda(code, [("available", P), ("available", {})])
    if c.stopped or out != "skipped":
        f.append("lambda: stopped although the tag went between the check and the stop (%s)" % out)
    # A refused stop is not "already stopping": only a state seen to be stopping ends it.
    err = "InvalidDBInstanceStateFault: DB instance is backing-up"
    out, c = run_lambda(code, [("available", P)], stop_error=err)
    if c.stopped or not str(out).startswith("raised:"):
        f.append("lambda: a stop refused in a non-stopping state was treated as done (%s)" % out)
    out, c = run_lambda(code, [("available", P), ("available", P), ("stopping", P)], stop_error=err)
    if c.stopped or out != "already stopping":
        f.append("lambda: did not settle on 'already stopping' after a refused stop (%s)" % out)
    # The stop call is bounded: its worst case must be shorter than the wait pause.sh --up
    # puts between clearing the tag and reading the status.
    m = re.search(r"AF_TAG_GRACE_SEC:-(\d+)", pause_src)
    out, c = run_lambda(code, [("available", P)])
    # Both the last tag read before the stop and the stop itself.
    for what, cfg in (("stop call", c.stop_cfg), ("last tag read", c.tag_cfgs[-1] if c.tag_cfgs else None)):
        cfg = cfg or {}
        worst = cfg.get("connect_timeout", 60) + cfg.get("read_timeout", 60)
        if (cfg.get("retries") or {}).get("max_attempts", 99) != 0 or not m or worst >= int(m.group(1)):
            f.append("lambda: the %s is not bounded below pause.sh's wait (%s vs %s)" % (what, cfg, m and m.group(1)))
    out, c = run_lambda(code, [("starting", P)], remaining_ms=30000)
    if c.stopped or not str(out).startswith("raised:"):
        f.append("lambda: did not give up (to be retried) when out of time (%s)" % out)
    return f


def as_list(v):
    return v if isinstance(v, list) else [v]


def main():
    try:
        net, data = load("00-network.yaml"), load("10-data.yaml")
        pause_src = open(PAUSE).read()
    except Exception as e:  # noqa: BLE001
        print("cfn-pause-save-test: cannot load: %s" % e)
        return 2
    fails = check(net, data, pause_src)
    for m in fails:
        print("NG: " + m)
    if fails:
        return 1
    print("ok: checks pass")

    # Positive controls: each broken copy must be caught.
    def broken(label, mutate, want):
        n, d, s = copy.deepcopy(net), copy.deepcopy(data), pause_src
        n, d, s = mutate(n, d, s) or (n, d, s)
        got = check(n, d, s)
        if not any(want in g for g in got):
            print("NG: positive control '%s' was not caught (got %s)" % (label, got))
            return False
        return True

    def code_of(d):
        return d["Resources"]["RestopFunction"]["Properties"]["Code"]

    def m_route(n, d, s):
        del n["Resources"]["PrivateDefaultRoute"]["Condition"]
    def m_eip(n, d, s):
        del n["Resources"]["NatEip"]["DeletionPolicy"]
    def m_event(n, d, s):
        d["Resources"]["RestopRule"]["Properties"]["EventPattern"]["detail"]["EventID"] = ["RDS-EVENT-0153"]
    def m_act(n, d, s):
        d["Resources"]["RestopRole"]["Properties"]["Policies"][0]["PolicyDocument"]["Statement"][0]["Action"].append("rds:DeleteDBInstance")
    def m_nat_inverted(n, d, s):
        n["Conditions"]["NatOn"] = {"!Equals": [{"!Ref": "NatEnabled"}, "false"]}
    def m_nat_always(n, d, s):
        n["Conditions"]["NatOn"] = {"!Equals": ["true", "true"]}
    def m_restop_inverted(n, d, s):
        d["Conditions"]["RestopOn"] = {"!Equals": [{"!Ref": "RdsRestopOnForcedStart"}, "false"]}
    def m_no_0088(n, d, s):
        d["Resources"]["RestopRule"]["Properties"]["EventPattern"]["detail"]["EventID"] = ["RDS-EVENT-0154"]
    def m_no_recheck(n, d, s):
        c = code_of(d)
        c["ZipFile"] = c["ZipFile"].replace('st == "available" and paused(i["DBInstanceArn"], rds_fast)', 'st == "available"')
    def m_no_swallow(n, d, s):
        c = code_of(d)
        c["ZipFile"] = c["ZipFile"].replace('print("stop refused, polling again: " + str(e))', 'return "already stopping"')
    def m_unbounded(n, d, s):
        c = code_of(d)
        c["ZipFile"] = c["ZipFile"].replace("rds_fast.stop_db_instance", "rds.stop_db_instance")
    def m_tag_unbounded(n, d, s):
        c = code_of(d)
        c["ZipFile"] = c["ZipFile"].replace('paused(i["DBInstanceArn"], rds_fast)', 'paused(i["DBInstanceArn"])')
    def m_retries(n, d, s):
        c = code_of(d)
        c["ZipFile"] = c["ZipFile"].replace('"max_attempts": 0', '"max_attempts": 10')
    def m_grace(n, d, s):
        return n, d, s.replace("AF_TAG_GRACE_SEC:-20", "AF_TAG_GRACE_SEC:-5")
    def m_notag(n, d, s):
        c = code_of(d)
        c["ZipFile"] = c["ZipFile"].replace('.get("af-paused") == "true"', '.get("af-paused") != "never"')
    def m_nowait(n, d, s):
        c = code_of(d)
        c["ZipFile"] = c["ZipFile"].replace('st == "available" and', 'st in ("available", "starting") and')
    def m_stopped(n, d, s):
        c = code_of(d)
        c["ZipFile"] = c["ZipFile"].replace('if st in ("stopped", "stopping"):', 'if False:')
    def m_tagkey(n, d, s):
        return n, d, s.replace("Key=af-paused,Value=true", "Key=x,Value=y")

    ok = True
    for label, fn, want in (
        ("route unconditional", m_route, "PrivateDefaultRoute is not behind NatOn"),
        ("EIP not retained", m_eip, "no longer retained"),
        ("wrong event", m_event, "RDS-EVENT-0088"),
        ("wider role", m_act, "role's actions"),
        ("NatOn inverted", m_nat_inverted, "NatOn is not exactly"),
        ("NatOn always true", m_nat_always, "NatOn is not exactly"),
        ("RestopOn inverted", m_restop_inverted, "RestopOn is not exactly"),
        ("0088 dropped", m_no_0088, "RDS-EVENT-0088"),
        ("no re-check before stop", m_no_recheck, "between the check and the stop"),
        ("refused stop taken as done", m_no_swallow, "refused in a non-stopping state"),
        ("stop on the unbounded client", m_unbounded, "not bounded"),
        ("last tag read on the unbounded client", m_tag_unbounded, "last tag read is not bounded"),
        ("SDK retries on the stop", m_retries, "not bounded"),
        ("--up wait shorter than the bound", m_grace, "not bounded"),
        ("tag ignored", m_notag, "without the af-paused tag"),
        ("starting not waited for", m_nowait, "did not wait"),
        ("stopping a stopped one", m_stopped, "alone"),
        ("pause.sh tag renamed", m_tagkey, "pause.sh no longer"),
    ):
        ok = broken(label, fn, want) and ok
    if not ok:
        return 1
    print("ok: positive controls caught")
    return 0


if __name__ == "__main__":
    sys.exit(main())
