#!/usr/bin/env python3
"""Prove that an edit to a CloudFormation template changed only PROSE.

`60-engines.yaml` lives against the 51,200-byte inline limit (`ecs-lifecycle-stub-test.sh`
case 3b-2), and a YAML comment costs exactly what a `Description` does — so the way to make
room is to move prose into `cfn/PARAMETERS-*.md`. That is a large, mechanical diff across a
file that builds IAM, capacity providers and an embedded shell, and "I only touched comments"
is not something a reviewer can check by eye at that size. This makes it checkable:

    git show HEAD:deploy/aws/ecs/cfn/60-engines.yaml > /tmp/before.yaml
    deploy/local/cfn-equiv.py /tmp/before.yaml deploy/aws/ecs/cfn/60-engines.yaml

WHAT IS DROPPED BEFORE COMPARING — exactly four things, each of them prose CloudFormation
shows a human and never acts on:

    $.Description                      the stack description
    $.Metadata                         template-level metadata
    $.Parameters.<name>.Description
    $.Outputs.<name>.Description

Comments are dropped by the YAML parser itself, before this file sees anything, so a
comment-only edit is invisible here by construction. That is the point: anything this DOES
report is a change to what the stack builds.

🔴 WHAT IS NOT DROPPED, and why. A `Description` that is a RESOURCE PROPERTY is compared like
any other value — `AWS::SSM::Parameter`, `AWS::SecretsManager::Secret` and a security-group
rule each carry one to AWS, and a `Metadata` block on a resource is functional (cfn-init).
Exempting `Description` by NAME rather than by PATH would leave a hole big enough for a real
change to hide in, in the one file where nobody is reading the whole diff.

The short-form intrinsics (`!Sub`, `!Ref`, `!If`, `!GetAtt`, …) survive as data, so retargeting
one is a difference and not a parse error. A block scalar is compared as the string it loads
to, which is why re-folding one (`|-` -> `>-`, where newlines become spaces) shows up here as
well — see `PARAMETERS-60-engines.md`, "The fetch sidecar", for why that must never happen
quietly.

--self-test plants seven mutations in a template and checks the verdict on each: five that must
be reported and two that must not. Run it whenever this file is edited — a comparator that has
stopped comparing looks exactly like a clean diff.

usage: cfn-equiv.py <before.yaml> <after.yaml>
       cfn-equiv.py <template.yaml>            # compare against `git show HEAD:<path>`
       cfn-equiv.py --self-test <template.yaml>
exit 0 identical (or every control behaved), 1 different, 2 the check could not run.
"""
import copy
import subprocess
import sys

try:
    import yaml
except ImportError:  # a check that could not run has not passed
    sys.exit("cfn-equiv: PyYAML is required (pip install pyyaml)")


class CfnLoader(yaml.SafeLoader):
    """SafeLoader that keeps CFN's short-form tags (!Ref, !Sub, …) as plain data."""


def _tag(loader, suffix, node):
    name = "Fn::" + suffix if suffix not in ("Ref", "Condition") else suffix
    if isinstance(node, yaml.ScalarNode):
        return {name: loader.construct_scalar(node)}
    if isinstance(node, yaml.SequenceNode):
        return {name: loader.construct_sequence(node, deep=True)}
    return {name: loader.construct_mapping(node, deep=True)}


CfnLoader.add_multi_constructor("!", _tag)

PROSE = ("Description", "Metadata")


def load_text(text):
    doc = yaml.load(text, Loader=CfnLoader)
    if not isinstance(doc, dict):
        raise ValueError("not a CloudFormation template (top level is not a mapping)")
    return doc


def load_path(path):
    with open(path, encoding="utf-8") as fh:
        return load_text(fh.read())


def strip(doc):
    """Drop the four prose fields listed in this file's header, and nothing else."""
    out = {k: v for k, v in doc.items() if k not in PROSE}
    for section in ("Parameters", "Outputs"):
        if isinstance(out.get(section), dict):
            out[section] = {
                name: ({k: v for k, v in spec.items() if k != "Description"}
                       if isinstance(spec, dict) else spec)
                for name, spec in out[section].items()
            }
    return out


def _short(value):
    text = repr(value)
    return text if len(text) <= 160 else text[:80] + " ... " + text[-60:]


def diff(a, b, path="$"):
    """The FIRST difference as a JSON-ish path, or None. First rather than all: at this size
    a list of 400 differences is not read, and one located difference is acted on."""
    if type(a) is not type(b):
        return f"{path}: type {type(a).__name__} vs {type(b).__name__}"
    if isinstance(a, dict):
        for key in sorted(set(a) | set(b)):
            if key not in a:
                return f"{path}.{key}: only in the SECOND template"
            if key not in b:
                return f"{path}.{key}: only in the FIRST template"
            found = diff(a[key], b[key], f"{path}.{key}")
            if found:
                return found
        return None
    if isinstance(a, list):
        if len(a) != len(b):
            return f"{path}: length {len(a)} vs {len(b)}"
        for i, (x, y) in enumerate(zip(a, b)):
            found = diff(x, y, f"{path}[{i}]")
            if found:
                return found
        return None
    return None if a == b else f"{path}: {_short(a)} vs {_short(b)}"


def compare(before, after):
    return diff(strip(before), strip(after))


# --- the self-test -------------------------------------------------------------------
#
# Every mutation is found structurally rather than by naming a field of one template, so this
# runs against any of them. A control that cannot find its fixture FAILS: a skipped control
# and a passing one are indistinguishable in the output, which is the failure this whole file
# exists to prevent.

def _first_where(node, want, path=()):
    """(container, key, path) of the first entry `want(key, value)` accepts, depth first."""
    if isinstance(node, dict):
        for key, value in node.items():
            here = path + (key,)
            if want(key, value):
                return node, key, here
            found = _first_where(value, want, here)
            if found:
                return found
    elif isinstance(node, list):
        for i, value in enumerate(node):
            here = path + (i,)
            if want(i, value):
                return node, i, here
            found = _first_where(value, want, here)
            if found:
                return found
    return None


def _longest_string(node, best=None):
    if isinstance(node, str):
        return node if best is None or len(node) > len(best) else best
    if isinstance(node, dict):
        for value in node.values():
            best = _longest_string(value, best)
    elif isinstance(node, list):
        for value in node:
            best = _longest_string(value, best)
    return best


def _mutate(doc, finder, change):
    out = copy.deepcopy(doc)
    found = finder(out)
    if not found:
        return None, None
    container, key, path = found
    container[key] = change(container[key])
    return out, "$." + ".".join(str(p) for p in path)


def self_test(path):
    text = open(path, encoding="utf-8").read()
    base = load_text(text)
    cases = []

    def param_default(doc):
        return _first_where(doc.get("Parameters", {}),
                            lambda k, v: k == "Default" and isinstance(v, (int, str)),
                            ("Parameters",))

    def resource_ref(doc):
        return _first_where(doc.get("Resources", {}),
                            lambda k, v: k == "Ref" and isinstance(v, str), ("Resources",))

    def resource_description(doc):
        """A resource's own Description, PLANTED when the template carries none. Skipping the
        control on a template that happens not to have one would be the silent pass this whole
        file exists to prevent -- and adding one is the same claim: it is not exempt."""
        found = _first_where(doc.get("Resources", {}), lambda k, v: k == "Description",
                             ("Resources",))
        if found:
            return found
        for name, spec in doc.get("Resources", {}).items():
            props = spec.setdefault("Properties", {})
            if isinstance(props, dict):
                props["Description"] = "planted by --self-test"
                return props, "Description", ("Resources", name, "Properties", "Description")
        return None

    def param_description(doc):
        return _first_where(doc.get("Parameters", {}), lambda k, v: k == "Description",
                            ("Parameters",))

    def stack_description(doc):
        return (doc, "Description", ("Description",)) if "Description" in doc else None

    # The longest string that is actually COMPARED -- searched in the stripped copy, or the
    # control lands on the stack description and "invisible" is the correct verdict for it.
    longest = _longest_string(strip(base))

    def biggest_scalar(doc):
        for section in ("Mappings", "Resources", "Conditions", "Outputs", "Parameters"):
            if section not in doc:
                continue
            found = _first_where(doc[section],
                                 lambda k, v: isinstance(v, str) and v == longest, (section,))
            if found and found[1] != "Description":
                return found
        return None

    bump = lambda v: (v + 1) if isinstance(v, int) else (str(v) + "-mutated")
    cases.append(("a parameter Default", param_default, bump, True))
    cases.append(("a !Ref inside a resource", resource_ref, lambda v: v + "Other", True))
    cases.append(("one character of the longest embedded string",
                  biggest_scalar, lambda v: v[:-1] + "X", True))
    cases.append(("a resource property's own Description",
                  resource_description, lambda v: {"Fn::Sub": "mutated"} if isinstance(v, dict)
                  else "mutated", True))
    cases.append(("a parameter Description", param_description, lambda v: "mutated", False))
    cases.append(("the stack Description", stack_description, lambda v: "mutated", False))

    ok = True
    for label, finder, change, must_report in cases:
        other, where = _mutate(base, finder, change)
        if other is None:
            print(f"cfn-equiv: NG control could not be planted: {label} "
                  f"(no such field in {path})", file=sys.stderr)
            ok = False
            continue
        found = compare(base, other)
        good = bool(found) is must_report
        want = "reported" if must_report else "invisible"
        print(f"  [{'ok' if good else 'NG'}] {want:10s} {label} ({where})"
              + (f" -> {found}" if found and good else ""))
        if not good:
            ok = False

    # The seventh runs on the TEXT, because a comment never reaches the loader at all -- which
    # is the whole reason a prose pass can be proven this way.
    lines = text.split("\n")
    lines.insert(1, "# a comment planted by --self-test")
    found = compare(base, load_text("\n".join(lines)))
    good = found is None
    print(f"  [{'ok' if good else 'NG'}] invisible  an added comment line")
    ok = ok and good

    if not ok:
        print("cfn-equiv: SELF-TEST FAILED -- this comparator cannot be trusted", file=sys.stderr)
        return 1
    print(f"cfn-equiv: self-test OK on {path} (5 reported, 2 invisible)")
    return 0


def _usage():
    print("\n".join(__doc__.strip().splitlines()[-4:]), file=sys.stderr)
    return 2


def main(argv):
    args = argv[1:]
    if args and args[0] == "--self-test":
        if len(args) != 2:
            return _usage()
        try:
            return self_test(args[1])
        except (OSError, ValueError, yaml.YAMLError) as err:
            print(f"cfn-equiv: {err}", file=sys.stderr)
            return 2
    if not args or len(args) > 2:
        return _usage()
    try:
        if len(args) == 1:
            head = subprocess.run(["git", "show", f"HEAD:{args[0]}"],
                                  capture_output=True, text=True, check=True).stdout
            before, after = load_text(head), load_path(args[0])
            names = (f"HEAD:{args[0]}", args[0])
        else:
            before, after = load_path(args[0]), load_path(args[1])
            names = (args[0], args[1])
    except subprocess.CalledProcessError as err:
        print(f"cfn-equiv: git show failed: {err.stderr.strip()}", file=sys.stderr)
        return 2
    except (OSError, ValueError, yaml.YAMLError) as err:
        print(f"cfn-equiv: {err}", file=sys.stderr)
        return 2

    found = compare(before, after)
    if found:
        print(f"cfn-equiv: DIFFERENT ({names[0]} vs {names[1]})\n  {found}", file=sys.stderr)
        return 1
    print(f"cfn-equiv: {names[0]} and {names[1]} build the same stack "
          f"(only the stack, parameter and output descriptions differ)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
