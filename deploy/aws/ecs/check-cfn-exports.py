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


def offenders(path):
    with open(path) as fh:
        doc = yaml.load(fh, Loader=CfnLoader) or {}
    may_be_empty = empty_parameters(doc)
    for name, spec in (doc.get("Outputs") or {}).items():
        if not isinstance(spec, dict) or "Export" not in spec:
            continue
        if spec.get("Condition"):          # gated — cannot export the empty case
            continue
        value = spec.get("Value")
        if isinstance(value, dict) and value.get("Ref") in may_be_empty:
            yield name, value["Ref"]


def main(argv):
    paths = argv[1:]
    if not paths:
        sys.exit(__doc__.strip().splitlines()[-2])
    bad = False
    for path in paths:
        for output, param in offenders(path):
            bad = True
            print(
                f"{path}: Output {output} exports !Ref {param}, which can be empty — "
                f"the stack rolls back at CREATE. Gate the Output with a Condition.",
                file=sys.stderr,
            )
    if bad:
        return 1
    print(f"check-cfn-exports: {len(paths)} templates, no empty exports")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
