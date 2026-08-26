#!/usr/bin/env python3
"""Refuse a CloudFormation Output that exports a value which can be empty.

CloudFormation rejects an empty export at CREATE time — *after* every resource in
the stack has been built — with

    Cannot export output <name>. Exported values must not be empty or
    whitespace-only.

and rolls the whole stack back. So an `Export` on an Output whose Value is a `!Ref`
to a parameter that defaults to "" makes the stack **impossible to create** unless
the operator supplies that optional parameter. This shipped in 0.10.0:
`40-ec2-pool.yaml` exported `SlotAmiIdArm64`, whose entire point is to be empty
unless an arm64 slot class is declared, so a fresh `ecs-ec2` deployment rolled back
on its last step.

⚠️ **cfn-lint does not catch this** (measured with 1.53.1 on the broken template:
exit 0). Neither does `validate-template`. Only a real deploy does — which is why
this check exists next to the linter in the release gate.

The fix is a Condition on the Output, not the removal of the export:

    Conditions:
      HasArm64Ami: !Not [ !Equals [ !Ref SlotAmiIdArm64, "" ] ]
    Outputs:
      SlotAmiIdArm64:
        Condition: HasArm64Ami
        ...

`!If` is modelled too, because an optional parameter usually reaches an export
through one: with `CreateX: !Equals [ !Ref P, "" ]`, exporting `!If [CreateX, ...,
!Ref P]` is safe (that branch only runs when P was supplied) while the same export
with the branches swapped is the fatal case. Everything else — `!GetAtt`, `!Sub`, a
literal — is assumed non-empty.

usage: check-cfn-exports.py <template.yaml> [...]
exit 0 clean, 1 a template can produce an empty export, 2 the check could not run.
"""
import sys

try:
    import yaml
except ImportError:  # a check that could not run has not passed
    sys.exit("check-cfn-exports: PyYAML is required (pip install pyyaml)")


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


def empty_parameters(doc):
    """Parameters whose Default is absent-or-empty for a String — i.e. can be ''."""
    out = set()
    for name, spec in (doc.get("Parameters") or {}).items():
        if not isinstance(spec, dict):
            continue
        if spec.get("Type") != "String":
            continue
        default = spec.get("Default")
        if default is None or (isinstance(default, str) and not default.strip()):
            out.add(name)
    return out


def emptiness_conditions(doc):
    """Conditions of the form `!Equals [ !Ref P, "" ]` (and its `!Not`), by name.

    They are how a template says "the operator did not supply P", so inside such a
    Condition's branches the emptiness of P is *known* — which is what makes an
    `!If` export safe or fatal rather than merely suspicious.
    """
    out = {}

    def equals_empty(node):
        if not isinstance(node, dict):
            return None
        args = node.get("Fn::Equals")
        if not isinstance(args, list) or len(args) != 2:
            return None
        refs = [a for a in args if isinstance(a, dict) and isinstance(a.get("Ref"), str)]
        blanks = [a for a in args if isinstance(a, str) and not a.strip()]
        return refs[0]["Ref"] if len(refs) == 1 and len(blanks) == 1 else None

    for name, node in (doc.get("Conditions") or {}).items():
        param = equals_empty(node)
        if param:
            out[name] = (param, True)          # true branch => param IS empty
            continue
        if isinstance(node, dict) and isinstance(node.get("Fn::Not"), list) and len(node["Fn::Not"]) == 1:
            param = equals_empty(node["Fn::Not"][0])
            if param:
                out[name] = (param, False)     # true branch => param is NOT empty
    return out


def value_can_be_empty(value, may_be_empty, conds, known):
    """Can this Output Value evaluate to ''? `known` maps param -> is-empty so far.

    Only `!Ref` and `!If` are modelled: everything else (`!GetAtt`, `!Sub`, a
    literal) is treated as non-empty, exactly as this check has always done.
    """
    if not isinstance(value, dict):
        return None
    ref = value.get("Ref")
    if isinstance(ref, str):
        if ref in known:
            return ref if known[ref] else None
        return ref if ref in may_be_empty else None
    branches = value.get("Fn::If")
    if isinstance(branches, list) and len(branches) == 3:
        cond, when_true, when_false = branches
        fact = conds.get(cond) if isinstance(cond, str) else None
        for taken, is_true_branch in ((when_true, True), (when_false, False)):
            scoped = dict(known)
            if fact:
                param, empty_when_true = fact
                scoped[param] = empty_when_true if is_true_branch else not empty_when_true
            hit = value_can_be_empty(taken, may_be_empty, conds, scoped)
            if hit:
                return hit
    return None


def offenders(path):
    with open(path) as fh:
        doc = yaml.load(fh, Loader=CfnLoader) or {}
    may_be_empty = empty_parameters(doc)
    conds = emptiness_conditions(doc)
    for name, spec in (doc.get("Outputs") or {}).items():
        if not isinstance(spec, dict) or "Export" not in spec:
            continue
        if spec.get("Condition"):          # gated — cannot export the empty case
            continue
        param = value_can_be_empty(spec.get("Value"), may_be_empty, conds, {})
        if param:
            yield name, param


def main(argv):
    paths = argv[1:]
    if not paths:
        sys.exit(__doc__.strip().splitlines()[-2])
    bad = False
    for path in paths:
        for output, param in offenders(path):
            bad = True
            print(
                f"{path}: Output {output} can export {param}, which is empty on that "
                f"path — "
                f"the stack rolls back at CREATE. Gate the Output with a Condition.",
                file=sys.stderr,
            )
    if bad:
        return 1
    print(f"check-cfn-exports: {len(paths)} templates, no empty exports")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
