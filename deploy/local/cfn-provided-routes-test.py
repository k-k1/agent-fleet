#!/usr/bin/env python3
"""ProvidedRoutes on the ECS control plane (ADR 0129 Q1), checked offline against 30-ingress.

    python3 deploy/local/cfn-provided-routes-test.py

ProvidedRoutes=on turns on the CP's route gateway and the path its calls need. This fails when

  - the default is not off, or off still emits anything: an environment entry, the internal
    port mapping, the Service Connect server entry, the route role, the CP's grant to assume
    it, the security-group rule or the output (off must leave the stack as it was);
  - on does not set AF_PROVIDED_ROUTES exactly "1" (routegateway.Enabled), the 15-minute
    deadline, the workspace listener and AF_CP_INTERNAL_URL at the Service Connect alias and
    port the service declares;
  - a Service Connect timeout on that path does not outlast the deadline (ADR 0129 D2: no
    silence the CP allows may outlast a hop; 0 disables a timeout);
  - Workspaces cannot reach the listener port (the CP security group from the workspace one);
  - the route role grants anything but InvokeModel* and GetInferenceProfile, trusts anyone but
    the CP task role, or lets the CP assume it without the af-membership session tag (D5);
  - the CP's grant reaches anything but that role.

Positive controls at the end run the same checks against broken copies of the template.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import importlib.util
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("dek", os.path.join(HERE, "cfn-workspace-dek-test.py"))
dek = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dek)
ROOT = dek.ROOT
ENV = ("AF_PROVIDED_ROUTES", "AF_PROVIDED_ROUTE_DEADLINE", "AF_CP_INTERNAL_LISTEN", "AF_CP_INTERNAL_URL")
GATED = ("CpInternalFromWs", "DeploymentRouteRole", "CpAssumeRoutePolicy")
DEADLINE_S = 15 * 60


def pick(ev, x):
    """x with its !If resolved under ev; None for AWS::NoValue."""
    if isinstance(x, dict) and "!If" in x:
        c, yes, no = x["!If"]
        x = yes if ev.cond({"!Condition": c}) else no
    return None if x == {"!Ref": "AWS::NoValue"} else x


def cp_ports(ev, tpl):
    for c in tpl["Resources"]["TaskDef"]["Properties"]["ContainerDefinitions"]:
        if c.get("Name") == "cp":
            return [p for p in (pick(ev, m) for m in c["PortMappings"]) if p is not None]
    raise SystemExit("cfn-provided-routes-test: no cp container")


def sc_services(ev, tpl):
    sc = tpl["Resources"]["Service"]["Properties"]["ServiceConnectConfiguration"]
    return pick(ev, sc.get("Services", {"!Ref": "AWS::NoValue"})) or []


def statements(doc):
    st = doc["Statement"]
    return st if isinstance(st, list) else [st]


def as_list(x):
    return x if isinstance(x, list) else [x]


def check(tpl):
    fails = []
    p = tpl["Parameters"].get("ProvidedRoutes") or {}
    if p.get("Default") != "off" or p.get("AllowedValues") != ["off", "on"]:
        fails.append("ProvidedRoutes must default to off with AllowedValues [off, on]: %r" % p)
    res, outs = tpl["Resources"], tpl.get("Outputs", {})
    for name in GATED:
        if (res.get(name) or {}).get("Condition") != "ProvidedRoutesOn":
            fails.append("%s must exist only under ProvidedRoutesOn" % name)
    if (outs.get("DeploymentRouteRoleArn") or {}).get("Condition") != "ProvidedRoutesOn":
        fails.append("the DeploymentRouteRoleArn output must exist only under ProvidedRoutesOn")

    off = dek.Ev(tpl, {})
    env = off.cp_env_names()
    for k in ENV:
        if k in env:
            fails.append("%s is in the CP environment with ProvidedRoutes off" % k)
    if len(cp_ports(off, tpl)) != 1 or sc_services(off, tpl):
        fails.append("ProvidedRoutes off still adds a port mapping or a Service Connect server")

    on = dek.Ev(tpl, {"ProvidedRoutes": "on"})
    env = {k: on.val(v) for k, v in on.cp_env_names().items() if k in ENV}
    if env.get("AF_PROVIDED_ROUTES") != "1":
        fails.append("AF_PROVIDED_ROUTES=%r, want exactly 1" % env.get("AF_PROVIDED_ROUTES"))
    if env.get("AF_PROVIDED_ROUTE_DEADLINE") != "15m":
        fails.append("AF_PROVIDED_ROUTE_DEADLINE=%r, want 15m (the timeouts below are held to it)" % env.get("AF_PROVIDED_ROUTE_DEADLINE"))
    m = re.fullmatch(r"http://([a-z0-9-]+):(\d+)", env.get("AF_CP_INTERNAL_URL") or "")
    listen = re.fullmatch(r"0\.0\.0\.0:(\d+)", env.get("AF_CP_INTERNAL_LISTEN") or "")
    if not m or not listen:
        fails.append("AF_CP_INTERNAL_URL=%r / AF_CP_INTERNAL_LISTEN=%r are not an alias URL and a port"
                     % (env.get("AF_CP_INTERNAL_URL"), env.get("AF_CP_INTERNAL_LISTEN")))
        return fails
    alias, port = m.group(1), int(m.group(2))
    if int(listen.group(1)) != port:
        fails.append("the listener port %s is not the URL's %d" % (listen.group(1), port))
    named = [x for x in cp_ports(on, tpl) if x.get("Name")]
    if len(named) != 1 or int(named[0]["ContainerPort"]) != port or named[0].get("AppProtocol") != "http":
        fails.append("the CP task maps no http port %d for Service Connect: %r" % (port, named))
    svcs = sc_services(on, tpl)
    if len(svcs) != 1:
        fails.append("ProvidedRoutes on: want one Service Connect server entry, got %r" % svcs)
        return fails
    s = svcs[0]
    aliases = [(a.get("DnsName"), int(a.get("Port", 0))) for a in s.get("ClientAliases", [])]
    if named and s.get("PortName") != named[0].get("Name") or (alias, port) not in aliases:
        fails.append("Service Connect serves %r %r, not %s:%d" % (s.get("PortName"), aliases, alias, port))
    t = s.get("Timeout") or {}
    for k in ("IdleTimeoutSeconds", "PerRequestTimeoutSeconds"):
        v = t.get(k)
        if v is None or not (int(v) == 0 or int(v) > DEADLINE_S):
            fails.append("Service Connect %s=%r does not outlast the %ds deadline (0 = none)" % (k, v, DEADLINE_S))

    ing = res.get("CpInternalFromWs", {}).get("Properties", {})
    if (ing.get("FromPort"), ing.get("ToPort")) != (port, port) or "CpSgId" not in str(ing.get("GroupId")) \
            or "WsSgId" not in str(ing.get("SourceSecurityGroupId")):
        fails.append("no CP security-group rule admits workspaces on %d: %r" % (port, ing))

    role = res.get("DeploymentRouteRole", {}).get("Properties", {})
    for st in statements(role.get("AssumeRolePolicyDocument", {"Statement": []})):
        if "CpTaskRoleArn" not in str(st.get("Principal")):
            fails.append("the route role trusts %r, not the CP task role" % st.get("Principal"))
        if sorted(as_list(st.get("Action"))) != ["sts:AssumeRole", "sts:TagSession"]:
            fails.append("the route role's trust allows %r" % st.get("Action"))
        cond = st.get("Condition") or {}
        if (cond.get("Null") or {}).get("aws:RequestTag/af-membership") != "false" or \
                sorted((cond.get("ForAllValues:StringEquals") or {}).get("aws:TagKeys", [])) != ["af-membership", "af-tenant"]:
            fails.append("the route role can be assumed without the af-membership tag, or with other tags: %r" % cond)
    actions = set()
    for pol in role.get("Policies", []):
        for st in statements(pol["PolicyDocument"]):
            actions |= set(as_list(st["Action"]))
    if actions != {"bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream", "bedrock:GetInferenceProfile"}:
        fails.append("the route role grants %s" % sorted(actions))
    grant = res.get("CpAssumeRoutePolicy", {}).get("Properties", {})
    for st in statements(grant.get("PolicyDocument", {"Statement": []})):
        if sorted(as_list(st.get("Action"))) != ["sts:AssumeRole", "sts:TagSession"] or \
                st.get("Resource") != {"!GetAtt": "DeploymentRouteRole.Arn"}:
            fails.append("the CP's grant is %r on %r, want AssumeRole+TagSession on the route role" % (st.get("Action"), st.get("Resource")))
    if "CpTaskRoleArn" not in str(grant.get("Roles")):
        fails.append("the grant is not attached to the CP task role")
    return fails


def check_go():
    fails = []
    gw = open(os.path.join(ROOT, "control-plane", "internal", "routegateway", "gateway.go")).read()
    for needle in ('EnvSwitch = "AF_PROVIDED_ROUTES"', 'os.Getenv(EnvSwitch) == "1"', '"AF_PROVIDED_ROUTE_DEADLINE"'):
        if needle not in gw:
            fails.append("control-plane/internal/routegateway/gateway.go no longer contains %s" % needle)
    return fails


def main():
    try:
        tpl = dek.load()
    except Exception as e:  # noqa: BLE001
        print("cfn-provided-routes-test: cannot load the template: %s" % e)
        return 2
    fails = check(tpl) + check_go()

    def broken(mutate):
        t = copy.deepcopy(tpl)
        mutate(t)
        return check(t)

    def cp_env(t):
        for c in t["Resources"]["TaskDef"]["Properties"]["ContainerDefinitions"]:
            if c.get("Name") == "cp":
                return c["Environment"]

    def svc(t):
        return t["Resources"]["Service"]["Properties"]["ServiceConnectConfiguration"]["Services"]["!If"][1][0]

    def role(t):
        return t["Resources"]["DeploymentRouteRole"]["Properties"]

    controls = {
        "default on": lambda t: t["Parameters"]["ProvidedRoutes"].update(Default="on"),
        "switch emitted when off": lambda t: cp_env(t).append({"Name": "AF_PROVIDED_ROUTES", "Value": "1"}),
        "role without a condition": lambda t: t["Resources"]["DeploymentRouteRole"].pop("Condition"),
        "idle timeout under the deadline": lambda t: svc(t)["Timeout"].update(IdleTimeoutSeconds=300),
        "per-request timeout dropped": lambda t: svc(t)["Timeout"].pop("PerRequestTimeoutSeconds"),
        "alias not the URL's": lambda t: svc(t)["ClientAliases"][0].update(DnsName="af-cp"),
        "role grants more": lambda t: role(t)["Policies"][0]["PolicyDocument"]["Statement"][0]["Action"].append("bedrock:*"),
        "untagged assume": lambda t: role(t)["AssumeRolePolicyDocument"]["Statement"][0].pop("Condition"),
        "no workspace rule": lambda t: t["Resources"]["CpInternalFromWs"]["Properties"].update(FromPort=8099, ToPort=8099),
    }
    for name, mutate in controls.items():
        if not broken(mutate):
            fails.append("positive control %r was not caught" % name)
    for f in fails:
        print("FAIL:", f)
    if fails:
        return 1
    print("cfn-provided-routes-test: ok (%d positive controls caught)" % len(controls))
    return 0


if __name__ == "__main__":
    sys.exit(main())
