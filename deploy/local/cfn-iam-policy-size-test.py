#!/usr/bin/env python3
"""IAM policy size per role, across every ECS template, checked offline.

    python3 deploy/local/cfn-iam-policy-size-test.py
    python3 deploy/local/cfn-iam-policy-size-test.py --upgrade-from <git-ref> [--upgrade-from <slug>=<git-ref> ...]
        [--stack-name-len 0]

## Why this exists

IAM caps a role's inline policies at 10,240 characters in total (whitespace not counted)
and each managed policy at 6,144; the managed policies attached to one role are capped by an
adjustable quota, held here to the long-standing default of 10 as a conservative baseline. Five stacks put policies on the Control Plane task role - 20-platform's own
cp-runtime, and what 30-ingress, 40-ec2-pool and 60-engines attach through the imported
CpTaskRoleArn - so no one template shows the total. The first time it passed the limit
was the first deployment that tried (#1576): CreatePolicy failed with ServiceLimitExceeded
and the ingress stack rolled back.

This renders every template with every optional policy present (both branches of an !If
are tried and the longer one counts; every stack name is as long as the names it creates
allow, MAX_STACK_NAME), attributes
each policy to the role it lands on, and fails when

  - a role's inline policies together exceed INLINE_MAX;
  - one managed policy exceeds MANAGED_MAX;
  - a role carries more than MANAGED_COUNT_MAX managed policies;
  - a git ref given to --upgrade-from does not resolve (exit 2);
  - a policy names a role this script cannot find, or uses an intrinsic it cannot render
    (exit 2: a value it guessed could only make the total look smaller).

The negative controls at the end put the shape #1576 shipped back - every managed policy
on the CP task role inlined again - and a policy the size of cp-runtime into a managed one,
and require both to fail, so a check that measures nothing cannot pass.

--upgrade-from replays update.sh's order (40 -> 20 -> 50 -> 60 -> 30) from the templates at
a git ref to the working tree, and prints each role's inline total at every step. While a
stack updates, its old and new policies are both counted: CloudFormation creates a new
resource before it deletes the one it replaces, and a policy updated in place is counted at
the larger of its two sizes. A step fails only when it takes a role past IAM's limit and
past where the deployment started (which, existing, was under it with its real names).

exit 0 pass, 1 a check failed, 2 the check could not run.
"""
import argparse
import json
import os
import subprocess
import sys

try:
    import yaml
except ImportError:
    sys.exit("cfn-iam-policy-size-test: PyYAML is required (pip install pyyaml)")

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CFN = os.path.join("deploy", "aws", "ecs", "cfn")
SLUGS = ("00-network", "10-data", "20-platform", "40-ec2-pool", "50-tts", "60-engines", "30-ingress")
# update.sh's order for the stacks that exist before a release (README "One command").
UPDATE_ORDER = ("40-ec2-pool", "20-platform", "50-tts", "60-engines", "30-ingress")

# The first two hard limits are IAM's; the managed count is the older default of an adjustable
# quota, kept as a baseline every account has. The margins below them are room for the next
# statement, so the test goes red on the change that eats the room rather than on the
# deployment after it.
INLINE_HARD, MANAGED_HARD, MANAGED_COUNT_HARD = 10240, 6144, 10
INLINE_MAX, MANAGED_MAX, MANAGED_COUNT_MAX = 9500, 5800, 8

REGION, ACCOUNT = "ap-southeast-1", "123456789012"
# Every ARN in these policies repeats a stack name, so each stack is given the longest name
# the physical names it creates allow (the defaults are 13 to 16 characters):
#   20-platform  RoleName af-<stack>-cp-task / -ws-task <= 64
#   30-ingress   the ALB's Name af-<stack> <= 32
#   40-ec2-pool  RoleName af-<stack>-slot <= 64
#   60-engines   BucketName af-<stack>-models-<account id> <= 63
#   the others   no physical-name cap: CloudFormation's own 128
MAX_STACK_NAME = {"20-platform": 53, "30-ingress": 29, "40-ec2-pool": 56, "60-engines": 40}
STACK_NAME_CAP = 128
STACK_NAME_LEN = None  # None: MAX_STACK_NAME; 0: the README's af-ecs-* names; n: n each
PLACEHOLDER_LEN = 64
NOVALUE = object()


class Unrenderable(Exception):
    pass


# --- template loading -------------------------------------------------------------------

class CfnLoader(yaml.SafeLoader):
    pass


def _intrinsic(tag):
    key = "Ref" if tag == "Ref" else "Fn::" + tag

    def construct(loader, node):
        if isinstance(node, yaml.ScalarNode):
            v = loader.construct_scalar(node)
            if tag == "GetAtt":
                v = v.split(".", 1)
        elif isinstance(node, yaml.SequenceNode):
            v = loader.construct_sequence(node, deep=True)
        else:
            v = loader.construct_mapping(node, deep=True)
        return {key: v}
    return construct


for _t in ("Ref", "Sub", "GetAtt", "ImportValue", "Select", "Split", "If", "Equals", "Not",
           "Join", "FindInMap", "Base64", "Condition", "And", "Or", "GetAZs", "Cidr", "Length",
           "ToJsonString"):
    CfnLoader.add_constructor("!" + _t, _intrinsic(_t))


def commit_of(ref):
    """The commit a ref names. A typo or an unfetched tag must stop the replay: read as "no
    stacks at that ref" it starts from nothing and passes."""
    p = subprocess.run(["git", "-C", ROOT, "rev-parse", "--verify", "--quiet", ref + "^{commit}"],
                       capture_output=True, text=True)
    if p.returncode != 0:
        raise Unrenderable("git ref %r does not resolve to a commit here (fetch it first?)" % ref)
    return p.stdout.strip()


def load_templates(ref=None, overrides=None):
    """{slug: template} from the working tree, or from a git ref (per slug via overrides)."""
    for slug in overrides or {}:
        if slug not in SLUGS:
            raise Unrenderable("--upgrade-from %s=...: no such stack (one of %s)" % (slug, ", ".join(SLUGS)))
    out = {}
    for slug in SLUGS:
        r = (overrides or {}).get(slug, ref)
        path = os.path.join(CFN, slug + ".yaml")
        if r is None:
            with open(os.path.join(ROOT, path), encoding="utf-8") as fh:
                text = fh.read()
        else:
            c = commit_of(r)
            if subprocess.run(["git", "-C", ROOT, "cat-file", "-e", "%s:%s" % (c, path)],
                              capture_output=True).returncode != 0:
                continue  # the stack did not exist at that commit
            p = subprocess.run(["git", "-C", ROOT, "show", "%s:%s" % (c, path)],
                               capture_output=True, text=True)
            if p.returncode != 0:
                raise Unrenderable("git show %s:%s failed: %s" % (r, path, p.stderr.strip()))
            text = p.stdout
        out[slug] = yaml.load(text, Loader=CfnLoader)
    return out


def stack_name(slug):
    if STACK_NAME_LEN == 0:  # the README's names: what a deployment measured in the field has
        return "af-ecs-" + slug.split("-", 1)[1]
    n = STACK_NAME_LEN if STACK_NAME_LEN is not None else MAX_STACK_NAME.get(slug, STACK_NAME_CAP)
    return ("af-" + slug.split("-", 1)[1] + "-").ljust(n, "x")


# --- rendering --------------------------------------------------------------------------

class Ctx:
    def __init__(self, slug, tpls):
        self.slug, self.tpls, self.tpl = slug, tpls, tpls[slug]

    def stack(self):
        return stack_name(self.slug)


def _stack_param(name):
    for prefix, slug in (("Network", "00-network"), ("Data", "10-data"), ("Platform", "20-platform"),
                         ("Pool", "40-ec2-pool"), ("Tts", "50-tts"), ("Engines", "60-engines")):
        if name == prefix + "StackName":
            return stack_name(slug)
    return None


def _ref(name, ctx):
    pseudo = {"AWS::Region": REGION, "AWS::AccountId": ACCOUNT, "AWS::StackName": ctx.stack(),
              "AWS::Partition": "aws", "AWS::URLSuffix": "amazonaws.com", "AWS::NoValue": NOVALUE}
    if name in pseudo:
        return pseudo[name]
    if _stack_param(name):
        return _stack_param(name)
    params = ctx.tpl.get("Parameters", {})
    if name in params:
        p = params[name]
        d = p.get("Default")
        if isinstance(d, (str, int)) and str(d) != "" and not str(p.get("Type", "")).startswith("AWS::SSM"):
            return str(d)
        return "p" * 32
    res = ctx.tpl.get("Resources", {}).get(name)
    if res is None:
        raise Unrenderable("!Ref %s in %s: no such parameter or resource" % (name, ctx.slug))
    t, props = res.get("Type"), res.get("Properties", {})
    if t == "AWS::SecretsManager::Secret":
        n = render(props["Name"], ctx) if "Name" in props else (ctx.stack() + "-" + name + "-" + "x" * 12)
        return "arn:aws:secretsmanager:%s:%s:secret:%s-AbCdEf" % (REGION, ACCOUNT, n)
    for key in ("RoleName", "ClusterName", "RepositoryName", "BucketName", "LogGroupName", "Name",
                "ManagedPolicyName"):
        if key in props:
            return render(props[key], ctx)
    return "%s-%s-%s" % (ctx.stack(), name, "x" * 13)  # CloudFormation's generated physical id


def _getatt(name, attr, ctx):
    res = ctx.tpl.get("Resources", {}).get(name)
    if res is None:
        raise Unrenderable("!GetAtt %s.%s in %s: no such resource" % (name, attr, ctx.slug))
    t, phys = res.get("Type"), _ref(name, ctx)
    if attr != "Arn":
        return "x" * PLACEHOLDER_LEN
    arns = {
        "AWS::IAM::Role": "arn:aws:iam::%s:role/%s" % (ACCOUNT, phys),
        "AWS::IAM::InstanceProfile": "arn:aws:iam::%s:instance-profile/%s" % (ACCOUNT, phys),
        "AWS::ECR::Repository": "arn:aws:ecr:%s:%s:repository/%s" % (REGION, ACCOUNT, phys),
        "AWS::S3::Bucket": "arn:aws:s3:::%s" % phys,
        "AWS::ECS::Cluster": "arn:aws:ecs:%s:%s:cluster/%s" % (REGION, ACCOUNT, phys),
        "AWS::Logs::LogGroup": "arn:aws:logs:%s:%s:log-group:%s:*" % (REGION, ACCOUNT, phys),
    }
    return arns.get(t, "arn:aws:%s:%s:%s:%s" % ("x" * 16, REGION, ACCOUNT, "x" * PLACEHOLDER_LEN))


def _import(export, ctx):
    for slug, tpl in ctx.tpls.items():
        c = Ctx(slug, ctx.tpls)
        for out in tpl.get("Outputs", {}).values():
            if "Export" in out and render(out["Export"]["Name"], c) == export:
                return render(out["Value"], c)
    raise Unrenderable("!ImportValue %s in %s: no template exports it" % (export, ctx.slug))


def _size(v):
    return 0 if v is NOVALUE else len(json.dumps(v, separators=(",", ":"), ensure_ascii=False))


def _sub(template, local, ctx):
    out, i = [], 0
    while i < len(template):
        j = template.find("${", i)
        if j < 0:
            out.append(template[i:])
            break
        out.append(template[i:j])
        k = template.index("}", j)
        var = template[j + 2:k]
        if var.startswith("!"):
            out.append("${" + var[1:] + "}")
        elif var in local:
            out.append(str(local[var]))
        elif "." in var and not var.startswith("AWS::"):
            out.append(str(_getatt(*var.split(".", 1), ctx)))
        else:
            out.append(str(_ref(var, ctx)))
        i = k + 1
    return "".join(out)


def render(v, ctx):
    if isinstance(v, list):
        return [x for x in (render(i, ctx) for i in v) if x is not NOVALUE]
    if not isinstance(v, dict):
        return v
    if len(v) == 1:
        (k, x), = v.items()
        if k == "Ref":
            return _ref(x, ctx)
        if k == "Fn::GetAtt":
            return _getatt(x[0], x[1], ctx)
        if k == "Fn::Sub":
            if isinstance(x, str):
                return _sub(x, {}, ctx)
            return _sub(x[0], {n: render(e, ctx) for n, e in x[1].items()}, ctx)
        if k == "Fn::ImportValue":
            return _import(render(x, ctx), ctx)
        if k == "Fn::Join":
            return x[0].join(str(i) for i in render(x[1], ctx))
        if k == "Fn::Split":
            return render(x[1], ctx).split(x[0])
        if k == "Fn::Select":
            return render(x[1], ctx)[int(render(x[0], ctx))]
        if k == "Fn::If":
            # Every optional grant present: whichever branch is longer.
            a, b = render(x[1], ctx), render(x[2], ctx)
            return a if _size(a) >= _size(b) else b
        if k == "Fn::FindInMap":
            m, a, b = (render(i, ctx) for i in x)
            return render(ctx.tpl["Mappings"][m][a][b], ctx)
        if k.startswith("Fn::"):
            raise Unrenderable("%s in %s is not modelled" % (k, ctx.slug))
    return {k: r for k, r in ((k, render(i, ctx)) for k, i in v.items()) if r is not NOVALUE}


# --- what lands on which role -----------------------------------------------------------

class Policy:
    def __init__(self, kind, key, size, slug):
        self.kind, self.key, self.size, self.slug = kind, key, size, slug


def attachments(tpls):
    """{role: [Policy]} for every role in tpls. A role is "<slug>/<LogicalId>"; a policy's key
    is what identifies it across a stack update (same key: updated in place)."""
    roles, names, out = {}, {}, {}
    for slug, tpl in tpls.items():
        ctx = Ctx(slug, tpls)
        for name, res in tpl.get("Resources", {}).items():
            if res.get("Type") == "AWS::IAM::Role":
                rid = "%s/%s" % (slug, name)
                roles[(slug, name)] = rid
                names[_ref(name, ctx)] = rid
                out[rid] = []

    def role_of(slug, ref, ctx):
        if isinstance(ref, dict) and set(ref) == {"Ref"} and (slug, ref["Ref"]) in roles:
            return roles[(slug, ref["Ref"])]
        n = render(ref, ctx)
        if n not in names:
            raise Unrenderable("%s attaches a policy to role %r, which no template defines" % (slug, n))
        return names[n]

    for slug, tpl in tpls.items():
        ctx = Ctx(slug, tpls)
        for name, res in tpl.get("Resources", {}).items():
            t, props = res.get("Type"), res.get("Properties", {})
            if t == "AWS::IAM::Role":
                rid = roles[(slug, name)]
                for pol in props.get("Policies", []):
                    pname = render(pol["PolicyName"], ctx)
                    out[rid].append(Policy("inline", (slug, name, pname),
                                           _size(render(pol["PolicyDocument"], ctx)), slug))
                for arn in props.get("ManagedPolicyArns", []):
                    if isinstance(arn, dict) and set(arn) == {"Ref"}:
                        continue  # counted from the ManagedPolicy resource, which must name its roles
                    out[rid].append(Policy("aws-managed", (slug, name, render(arn, ctx)), 0, slug))
            elif t in ("AWS::IAM::Policy", "AWS::IAM::RolePolicy", "AWS::IAM::ManagedPolicy"):
                targets = props.get("Roles", []) + ([props["RoleName"]] if "RoleName" in props else [])
                size = _size(render(props["PolicyDocument"], ctx))
                kind = "managed" if t == "AWS::IAM::ManagedPolicy" else "inline"
                for ref in targets:
                    out[role_of(slug, ref, ctx)].append(Policy(kind, (slug, name, t), size, slug))
        for name, res in tpl.get("Resources", {}).items():
            if res.get("Type") == "AWS::IAM::Role":
                for arn in res.get("Properties", {}).get("ManagedPolicyArns", []):
                    if isinstance(arn, dict) and set(arn) == {"Ref"}:
                        pol = tpl["Resources"][arn["Ref"]]
                        rid = roles[(slug, name)]
                        key = (slug, arn["Ref"], "AWS::IAM::ManagedPolicy")
                        if not any(p.key == key for p in out[rid]):
                            out[rid].append(Policy("managed", key,
                                                   _size(render(pol["Properties"]["PolicyDocument"], ctx)), slug))
    return out


def inline_total(pols):
    return sum(p.size for p in pols if p.kind == "inline")


def managed_count(pols):
    return sum(1 for p in pols if p.kind in ("managed", "aws-managed"))


def violations(att):
    out = []
    for rid, pols in sorted(att.items()):
        if inline_total(pols) > INLINE_MAX:
            out.append("%s: inline policies total %d > %d (IAM refuses past %d)"
                       % (rid, inline_total(pols), INLINE_MAX, INLINE_HARD))
        for p in pols:
            if p.kind == "managed" and p.size > MANAGED_MAX:
                out.append("%s: managed policy %s is %d > %d (IAM refuses past %d)"
                           % (rid, p.key[1], p.size, MANAGED_MAX, MANAGED_HARD))
        if managed_count(pols) > MANAGED_COUNT_MAX:
            out.append("%s: %d managed policies > %d (baseline quota %d)"
                       % (rid, managed_count(pols), MANAGED_COUNT_MAX, MANAGED_COUNT_HARD))
    return out


def report(att):
    for rid, pols in sorted(att.items()):
        if not pols:
            continue
        print("%-28s inline %5d / %d   managed %d / %d" % (rid, inline_total(pols), INLINE_MAX,
                                                         managed_count(pols), MANAGED_COUNT_MAX))
        for p in sorted(pols, key=lambda p: (p.kind, p.key)):
            what = p.key[1] if str(p.key[2]).startswith("AWS::IAM::") else p.key[2]
            print("    %-11s %-34s %5s  (%s)" % (p.kind, what, p.size if p.kind != "aws-managed" else "-", p.slug))


# --- the upgrade replay -----------------------------------------------------------------

def upgrade(old, new):
    """Each role's inline peak and managed count while update.sh moves old -> new, stack by stack."""
    a_old, a_new = attachments(old), attachments(new)
    state = {slug: a_old for slug in SLUGS}
    worst, start = [], {}

    def merged(during=None):
        roles = {}
        for slug in SLUGS:
            if during == slug:
                both = {}
                for src in (a_old, a_new):
                    for rid, pols in src.items():
                        for p in pols:
                            if p.slug != slug:
                                continue
                            k = (rid, p.kind, p.key)
                            if k not in both or both[k].size < p.size:
                                both[k] = p
                for (rid, _, _), p in both.items():
                    roles.setdefault(rid, []).append(p)
                continue
            for rid, pols in state[slug].items():
                roles.setdefault(rid, []).extend(p for p in pols if p.slug == slug)
        return roles

    def line(label, roles):
        cp = roles.get("20-platform/CpTaskRole", [])
        print("  %-34s CpTaskRole inline %5d   managed %d" % (label, inline_total(cp), managed_count(cp)))
        # Only a step that makes a role worse than the deployment already was is the replay's
        # finding: the starting state exists, so it was under the limit with the real names.
        for rid, pols in roles.items():
            before = start.setdefault(rid, (inline_total(pols), managed_count(pols)))
            if ((inline_total(pols) > INLINE_HARD and inline_total(pols) > before[0])
                    or (managed_count(pols) > MANAGED_COUNT_HARD and managed_count(pols) > before[1])):
                worst.append("%s: %s inline %d, managed %d" % (label, rid, inline_total(pols), managed_count(pols)))

    line("before", merged())
    for slug in UPDATE_ORDER:
        line("during %s" % slug, merged(during=slug))
        state[slug] = a_new
        line("after %s" % slug, merged())
    return worst


# --- main -------------------------------------------------------------------------------

def main():
    global STACK_NAME_LEN
    ap = argparse.ArgumentParser()
    ap.add_argument("--upgrade-from", action="append", default=[],
                    help="git ref the deployment is on, or <slug>=<ref> for one stack")
    ap.add_argument("--stack-name-len", type=int, default=None,
                    help="every stack name this long; 0 = the README's af-ecs-* names "
                         "(default: the longest each stack's physical names allow)")
    args = ap.parse_args()
    STACK_NAME_LEN = args.stack_name_len
    try:
        new = load_templates()
        att = attachments(new)
    except (Unrenderable, KeyError, IndexError, TypeError, ValueError) as e:
        print("cfn-iam-policy-size-test: cannot render: %s" % e)
        return 2

    report(att)
    bad = violations(att)
    for v in bad:
        print("FAIL  " + v)

    # Negative controls: the check has to see the two shapes it exists for.
    cp = "20-platform/CpTaskRole"
    if not any(p.kind == "managed" for p in att.get(cp, [])):
        print("FAIL  control: the CP task role has no managed policy to inline back (did the attribution break?)")
        bad.append("control")
    inlined = {r: [Policy("inline" if p.kind == "managed" else p.kind, p.key, p.size, p.slug) for p in pols]
               for r, pols in att.items()}
    if any(v.startswith(cp + ": inline") for v in violations(inlined)):
        print("ok    control: every CP managed policy inlined again (#1576's shape) fails")
    else:
        print("FAIL  control: every CP managed policy inlined again passes - the inline check measures nothing")
        bad.append("control")
    runtime = max((p for p in att.get(cp, []) if p.kind == "inline"), key=lambda p: p.size, default=None)
    big = {cp: [Policy("managed", ("x", "cp-runtime-as-managed", "x"), runtime.size if runtime else 0, "x")]}
    if any("managed policy" in v for v in violations(big)):
        print("ok    control: cp-runtime (%d) as one managed policy fails" % (runtime.size if runtime else 0))
    else:
        print("FAIL  control: cp-runtime as one managed policy passes - the managed check measures nothing")
        bad.append("control")
    try:
        commit_of("no-such-ref-cfn-iam-policy-size-control")
        print("FAIL  control: an unknown git ref resolved - --upgrade-from would replay from nothing")
        bad.append("control")
    except Unrenderable:
        print("ok    control: an unknown git ref is refused")

    if args.upgrade_from:
        base, per = None, {}
        for a in args.upgrade_from:
            if "=" in a:
                s, r = a.split("=", 1)
                per[s] = r
            else:
                base = a
        print("\nupgrade replay (update.sh order; %s):" % ", ".join(args.upgrade_from))
        try:
            old = load_templates(base, per)
            for slug in SLUGS:  # a stack the old ref does not have: the new one is created in its turn
                old.setdefault(slug, {"Resources": {}})
            worst = upgrade(old, new)
        except (Unrenderable, KeyError, IndexError, TypeError, ValueError) as e:
            print("cfn-iam-policy-size-test: cannot render the old templates: %s" % e)
            return 2
        for w in worst:
            print("FAIL  over IAM's limit %s" % w)
        bad += worst

    print("%d role(s), %d failure(s)" % (sum(1 for p in att.values() if p), len(bad)))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
