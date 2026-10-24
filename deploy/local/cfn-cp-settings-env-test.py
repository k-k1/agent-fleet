#!/usr/bin/env python3
"""CP settings that guide/ref/settings.md calls operator-configurable, checked offline
against 30-ingress.yaml (#2596).

    python3 deploy/local/cfn-cp-settings-env-test.py

Fails when

  - a new parameter does not default to empty (plain) / off (secret switch), or a default
    stack emits any of the new environment entries or Secrets (a stack update that only
    overrides ImageTag/EfsBackup must change nothing);
  - a plain parameter does not arrive verbatim in its variable, or leaks into another;
  - a secret is delivered other than through the task's Secrets, from SSM under SsmPrefix,
    when ITS OWN switch is on - a sibling URL being set must not pull the secret in (a Secrets
    entry for a parameter that does not exist stops the task from starting);
  - the default-prefix secrets fall outside what the execution role may read in 20-platform;
  - WS_ENV is exposed (deliberately not: PARAMETERS.md);
  - the template leaves less than 1 KiB under CloudFormation's 51,200-byte limit.

Positive controls at the end run the same checks against broken copies.

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import copy
import fnmatch
import importlib.util
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("dek", os.path.join(HERE, "cfn-workspace-dek-test.py"))
dek = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dek)

PLAIN = {
    "SessionIdleTimeout": "AF_SESSION_IDLE_TIMEOUT",
    "InteractionIdleTimeout": "AF_INTERACTION_IDLE_TIMEOUT",
    "WsIdleTimeout": "AF_WS_IDLE_TIMEOUT",
    "PresenceIdleTimeout": "AF_PRESENCE_IDLE_TIMEOUT",
    "BackgroundHoldMax": "AF_BACKGROUND_HOLD_MAX",
    "WorkspaceStartDeadline": "AF_WORKSPACE_START_DEADLINE",
    "UsageSampleInterval": "AF_USAGE_SAMPLE_INTERVAL",
    "GithubLoginClientId": "AF_GITHUB_LOGIN_CLIENT_ID",
    "GithubLabelJa": "AF_GITHUB_LABEL_JA",
    "GithubLabelEn": "AF_GITHUB_LABEL_EN",
    "GithubMembershipTtl": "AF_GITHUB_MEMBERSHIP_TTL",
    "GithubMembershipGrace": "AF_GITHUB_MEMBERSHIP_GRACE",
    "LlmUrl": "AF_LLM_URL",
    "ComfyUrl": "AF_COMFY_URL",
    "RemoteEngineUrl": "AF_REMOTE_ENGINE_URL",
    "RemoteEngineKeys": "AF_REMOTE_ENGINE_KEYS",
}
# switch parameter -> (variable, SSM leaf under SsmPrefix)
SECRETS = {
    "GithubLoginSecret": ("AF_GITHUB_LOGIN_CLIENT_SECRET", "github-login-client-secret"),
    "ComfyApiKey": ("AF_COMFY_API_KEY", "comfy-api-key"),
    "RemoteEngineToken": ("AF_REMOTE_ENGINE_TOKEN", "remote-engine-token"),
}
NEW_VARS = set(PLAIN.values()) | {v for v, _ in SECRETS.values()}
MAX_BYTES = 51200 - 1024


def cp_container(tpl):
    for c in tpl["Resources"]["TaskDef"]["Properties"]["ContainerDefinitions"]:
        if c.get("Name") == "cp":
            return c
    raise SystemExit("cfn-cp-settings-env-test: no container named cp in TaskDef")


def secrets(ev):
    """{Name: ValueFrom} of the CP container's Secrets whose If-condition holds."""
    out = {}
    for e in cp_container(ev.tpl)["Secrets"]:
        if "!If" in e:
            c, yes, no = e["!If"]
            e = yes if ev.cond({"!Condition": c}) else no
        if e == {"!Ref": "AWS::NoValue"}:
            continue
        out[e["Name"]] = e["ValueFrom"]
    return out


def sub_text(x):
    return x.get("!Sub") if isinstance(x, dict) else x


def exec_role_patterns():
    path = os.path.join(dek.ROOT, "deploy", "aws", "ecs", "cfn", "20-platform.yaml")
    with open(path) as f:
        plat = dek.yaml.load(f, Loader=dek.CfnLoader)
    pol = plat["Resources"]["ExecRole"]["Properties"]["Policies"]
    out = []
    for p in pol:
        if p["PolicyName"] != "ssm-read":
            continue
        for st in p["PolicyDocument"]["Statement"]:
            acts = st["Action"] if isinstance(st["Action"], list) else [st["Action"]]
            if "ssm:GetParameters" in acts:
                res = st["Resource"] if isinstance(st["Resource"], list) else [st["Resource"]]
                out += [r["!Sub"].split("parameter", 1)[1] for r in res]
    return out


def check(tpl, size, patterns):
    fails = []
    P = tpl["Parameters"]
    for n in PLAIN:
        if (P.get(n) or {}).get("Default") != "" or "AllowedValues" in P[n]:
            fails.append("%s must default to empty: %r" % (n, P.get(n)))
    for n in SECRETS:
        p = P.get(n) or {}
        if p.get("Default") != "off" or p.get("AllowedValues") != ["off", "on"]:
            fails.append("%s must default to off with AllowedValues [off, on]: %r" % (n, p))
    base = dek.Ev(tpl, {})
    names = base.cp_env_names()
    for v in sorted(NEW_VARS & set(names)):
        fails.append("%s is in the CP environment with every parameter at its default" % v)
    for v in sorted(NEW_VARS & set(secrets(base))):
        fails.append("%s is in the CP Secrets with every parameter at its default" % v)
    for n, var in PLAIN.items():
        ev = dek.Ev(tpl, {n: "x-" + n})
        env = ev.cp_env_names()
        if env.get(var) != {"!Ref": n} and ev.val(env.get(var)) != "x-" + n:
            fails.append("%s does not arrive verbatim in %s: %r" % (n, var, env.get(var)))
        extra = (set(env) & NEW_VARS) - {var}
        if extra:
            fails.append("%s also sets %s" % (n, sorted(extra)))
        if secrets(ev).keys() & NEW_VARS:
            fails.append("%s pulled a secret in" % n)
    for n, (var, leaf) in SECRETS.items():
        # The sibling non-secret parameter set, the switch off: no Secrets entry.
        sib = {"GithubLoginSecret": "GithubLoginClientId", "ComfyApiKey": "ComfyUrl",
               "RemoteEngineToken": "RemoteEngineUrl"}[n]
        ev = dek.Ev(tpl, {sib: "https://engine.example.com"})
        if var in secrets(ev):
            fails.append("%s got a Secrets entry from %s alone, without %s=on" % (var, sib, n))
        ev = dek.Ev(tpl, {n: "on"})
        sec = secrets(ev)
        want = "arn:aws:ssm:${AWS::Region}:${AWS::AccountId}:parameter${SsmPrefix}/" + leaf
        if sub_text(sec.get(var)) != want:
            fails.append("%s=on: ValueFrom is %r, want %r" % (n, sec.get(var), want))
        if set(sec) & (NEW_VARS - {var}):
            fails.append("%s=on pulled in other secrets: %s" % (n, sorted(sec)))
        if var in ev.cp_env_names():
            fails.append("%s=on put the secret %s in plain Environment" % (n, var))
        # Default SsmPrefix must stay inside the execution role's grant.
        full = P["SsmPrefix"]["Default"] + "/" + leaf
        if not any(fnmatch.fnmatchcase(full, pat) for pat in patterns):
            fails.append("%s is outside the ExecRole ssm-read grant %s" % (full, patterns))
    # GitHub sign-in secrets: the dedicated App replaces the legacy secret (injecting both would
    # need both in SSM and stop the task when only one exists); the legacy one is unchanged.
    for params, legacy, login in (({}, False, False),
                                  ({"GithubAllowedOrgs": "example-org"}, True, False),
                                  ({"GithubAllowedOrgs": "example-org", "GithubLoginSecret": "on"}, False, True),
                                  ({"GithubLoginSecret": "on"}, False, True)):
        got = secrets(dek.Ev(tpl, params))
        if ("GITHUB_OAUTH_CLIENT_SECRET" in got) != legacy or ("AF_GITHUB_LOGIN_CLIENT_SECRET" in got) != login:
            fails.append("GitHub secrets for %r: %s, want legacy=%s login=%s" % (params, sorted(got), legacy, login))
    if "WS_ENV" in base.cp_env_names() or any("WS_ENV" in str(e) for e in cp_container(tpl)["Environment"]):
        fails.append("WS_ENV is exposed; it is deliberately not (PARAMETERS.md)")
    if size > MAX_BYTES:
        fails.append("30-ingress.yaml is %d bytes; the budget is %d (1 KiB under 51,200)" % (size, MAX_BYTES))
    return fails


def main():
    tpl = dek.load()
    size = os.path.getsize(dek.TEMPLATE)
    patterns = exec_role_patterns()
    if not patterns:
        print("cfn-cp-settings-env-test: no ssm:GetParameters grant found on ExecRole")
        return 2
    fails = check(tpl, size, patterns)
    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1

    def env(t):
        return cp_container(t)["Environment"]

    def sec(t):
        return cp_container(t)["Secrets"]

    def unwire_env(t):
        env(t)[:] = [e for e in env(t) if "AF_WS_IDLE_TIMEOUT" not in str(e)]

    def always_set(t):
        t["Conditions"]["HasLlmUrl"] = {"!Equals": ["a", "a"]}

    def secret_follows_sibling(t):
        t["Conditions"]["ComfyApiKeyOn"] = {"!Not": [{"!Equals": [{"!Ref": "ComfyUrl"}, ""]}]}

    def secret_in_env(t):
        env(t).append({"Name": "AF_COMFY_API_KEY", "Value": {"!Ref": "ComfyApiKey"}})

    def wrong_leaf(t):
        for e in sec(t):
            if "!If" in e and e["!If"][0] == "RemoteEngineTokenOn":
                e["!If"][1]["ValueFrom"]["!Sub"] = e["!If"][1]["ValueFrom"]["!Sub"].replace(
                    "remote-engine-token", "elsewhere/token")

    def default_on(t):
        t["Parameters"]["ComfyApiKey"]["Default"] = "on"

    def ws_env(t):
        env(t).append({"Name": "WS_ENV", "Value": "A=b"})

    def both_github(t):
        t["Conditions"]["GithubLegacySecret"] = {"!Condition": "HasGithub"}

    controls = {
        "legacy GitHub secret kept beside the login one": both_github,
        "wiring removed": unwire_env,
        "env always set": always_set,
        "secret follows the sibling URL": secret_follows_sibling,
        "secret in plain Environment": secret_in_env,
        "secret outside the ExecRole grant": wrong_leaf,
        "switch defaults on": default_on,
        "WS_ENV exposed": ws_env,
    }
    for name, m in controls.items():
        t = copy.deepcopy(tpl)
        m(t)
        if not check(t, size, patterns):
            fails.append("positive control %r was not caught" % name)
    if not check(tpl, MAX_BYTES + 1, patterns):
        fails.append("positive control 'template over budget' was not caught")
    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1
    print("cfn-cp-settings-env-test: ok (%d positive controls caught, %d bytes of %d)"
          % (len(controls) + 1, size, MAX_BYTES))
    return 0


if __name__ == "__main__":
    sys.exit(main())
