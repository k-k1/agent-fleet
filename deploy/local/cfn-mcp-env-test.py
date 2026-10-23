#!/usr/bin/env python3
"""AF_MCP_* on the ECS control-plane task, checked offline against the template.

    python3 deploy/local/cfn-mcp-env-test.py

30-ingress passes /mcp and MCP OAuth to the CP through McpEnabled / McpOAuth /
McpOAuthTrustedHosts. This evaluates the template's Rules, Conditions and the CP task's
Environment and fails when

  - a default is not off / empty, or an off parameter still emits an environment entry (a
    stack update must not change what the CP does);
  - the values differ from the ones the CP accepts: exactly "true" for AF_MCP_ENABLED and
    exactly "1" for AF_MCP_OAUTH (control-plane/routes.go, mcp_oauth.go);
  - McpOAuth=on without McpEnabled=on is accepted by the Rules;
  - the trusted-hosts list does not arrive verbatim.

Positive controls at the end run the same checks against broken copies of the template.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import importlib.util
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("dek", os.path.join(HERE, "cfn-workspace-dek-test.py"))
dek = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dek)
ROOT = dek.ROOT
HOSTS = "claude.ai,in-house.example.com"
MCP = ("AF_MCP_ENABLED", "AF_MCP_OAUTH", "AF_MCP_OAUTH_TRUSTED_HOSTS")


def check(tpl):
    fails = []
    P = tpl["Parameters"]
    for name, allowed in (("McpEnabled", ["off", "on"]), ("McpOAuth", ["off", "on"]),
                          ("McpOAuthTrustedHosts", None)):
        p = P.get(name) or {}
        want = "off" if allowed else ""
        if p.get("Default") != want or p.get("AllowedValues") != allowed:
            fails.append("%s must default to %r with AllowedValues %r: %r" % (name, want, allowed, p))
    base = dek.Ev(tpl, {}).cp_env_names()
    for k in MCP:
        if k in base:
            fails.append("%s is in the CP environment with every MCP parameter at its default" % k)
    for en in ("off", "on"):
        for oa in ("off", "on"):
            for hosts in ("", HOSTS):
                ev = dek.Ev(tpl, {"McpEnabled": en, "McpOAuth": oa, "McpOAuthTrustedHosts": hosts})
                want_ok = not (oa == "on" and en == "off")
                if ev.rules_ok() != want_ok:
                    fails.append("Rules verdict for McpEnabled=%s McpOAuth=%s: want accepted=%s"
                                 % (en, oa, want_ok))
                env = ev.cp_env_names()
                for var, got_want in (("AF_MCP_ENABLED", "true" if en == "on" else None),
                                      ("AF_MCP_OAUTH", "1" if oa == "on" else None),
                                      ("AF_MCP_OAUTH_TRUSTED_HOSTS", hosts or None)):
                    got = ev.val(env[var]) if var in env else None
                    if got != got_want:
                        fails.append("%s=%r for McpEnabled=%s McpOAuth=%s hosts=%r, want %r"
                                     % (var, got, en, oa, hosts, got_want))
    # Pass-throughs from the settings audit: empty by default (the CP reads empty as unset).
    for name, var in (("SessionTtl", "AF_SESSION_TTL"), ("GithubAllowedEmails", "AF_GITHUB_ALLOWED_EMAILS"),
                      ("KmsDataKeyCacheTtl", "AF_KMS_DATA_KEY_CACHE_TTL")):
        if (P.get(name) or {}).get("Default") != "":
            fails.append("%s must default to empty" % name)
        ev = dek.Ev(tpl, {name: "x"})
        if ev.val(ev.cp_env_names().get(var)) != "x":
            fails.append("%s does not reach %s" % (name, var))
    return fails


def check_go():
    fails = []
    for f, needle in (("routes.go", 'envx.Or("AF_MCP_ENABLED", "") == "true"'),
                      ("mcp_oauth.go", 'envx.Or("AF_MCP_OAUTH", "") == "1"')):
        if needle not in open(os.path.join(ROOT, "control-plane", f)).read():
            fails.append("control-plane/%s no longer contains %s" % (f, needle))
    return fails


def main():
    tpl = dek.load()
    fails = check(tpl) + check_go()
    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1

    def cp_env(t):
        for c in t["Resources"]["TaskDef"]["Properties"]["ContainerDefinitions"]:
            if c.get("Name") == "cp":
                return c["Environment"]

    def no_wiring(t):
        env = cp_env(t)
        env[:] = [e for e in env if not ("!If" in e and e["!If"][0] == "McpOn")]

    def always_on(t):
        t["Conditions"]["McpOn"] = {"!Equals": ["a", "a"]}

    def oauth_value(t):
        for e in cp_env(t):
            if "!If" in e and e["!If"][0] == "McpOAuthOn":
                e["!If"][1]["Value"] = "true"

    controls = {
        "wiring removed": no_wiring,
        "env always set": always_on,
        "OAuth value the CP rejects": oauth_value,
        "audit pass-through unwired": lambda t: [
            e.__setitem__("Name", "X") for e in cp_env(t)
            if isinstance(e, dict) and e.get("Name") == "AF_SESSION_TTL"],
        "rule removed": lambda t: t.pop("Rules"),
        "default on": lambda t: t["Parameters"]["McpEnabled"].__setitem__("Default", "on"),
    }
    for name, m in controls.items():
        t = copy.deepcopy(tpl)
        m(t)
        if not check(t):
            fails.append("positive control %r was not caught" % name)
    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1
    print("cfn-mcp-env-test: ok (%d positive controls caught)" % len(controls))
    return 0


if __name__ == "__main__":
    sys.exit(main())
