#!/usr/bin/env python3
"""Print the engine-tools contract number a CloudFormation template asks for.

The engine fetch sidecar and the two ingest steps live in an image now
(`deploy/aws/ecs/engine-tools/`), not inline in `60-engines.yaml`, so the template and the
script it runs are shipped by two separate acts. What binds them is a version number: the
template sets `ENGINE_TOOLS_CONTRACT` on every container that runs one of those scripts, and
each script refuses a number it does not recognise (exit 78).

That gate is only worth having if the two numbers are actually kept together, which is what
`deploy/local/engine-sidecar-test.sh` uses this for. Reading it needs a YAML parser that
tolerates CloudFormation's short-form tags, which is why it is a file rather than three lines
of `grep`.

⚠️ Two things are errors rather than something to pick between:

  - two DIFFERENT numbers in one template. No single image can satisfy it, and printing either
    one would make the harness agree with a template that cannot work;
  - a container that RUNS one of the scripts (`Command` naming `/opt/af/…`) and declares no
    contract at all. That one is the reason this looks at commands instead of just collecting
    environment variables: the gate would still stop it, but only on the real deployment, at the
    next cold start, in a log nobody is reading yet.

usage: cfn-contract.py <template.yaml>
exit 0 and the number on stdout; 1 otherwise, with what is wrong on stderr.
"""
import sys

try:
    import yaml
except ImportError:  # a check that could not run has not passed
    sys.exit("cfn-contract: PyYAML is required (pip install pyyaml)")


class CfnLoader(yaml.SafeLoader):
    """SafeLoader that tolerates CFN's short-form tags (!Ref, !Sub, …)."""


def _tag(loader, suffix, node):
    if isinstance(node, yaml.ScalarNode):
        return loader.construct_scalar(node)
    if isinstance(node, yaml.SequenceNode):
        return loader.construct_sequence(node, deep=True)
    return loader.construct_mapping(node, deep=True)


CfnLoader.add_multi_constructor("!", _tag)


TOOL_DIR = "/opt/af/"


def scan(path):
    """(the declared numbers, the names of containers that run a script without declaring one)."""
    doc = yaml.load(open(path, encoding="utf-8"), Loader=CfnLoader)
    found, undeclared = set(), []
    for name, res in (doc.get("Resources") or {}).items():
        if not isinstance(res, dict) or res.get("Type") != "AWS::ECS::TaskDefinition":
            continue
        for c in res.get("Properties", {}).get("ContainerDefinitions") or []:
            declared = None
            for env in c.get("Environment") or []:
                if isinstance(env, dict) and env.get("Name") == "ENGINE_TOOLS_CONTRACT":
                    declared = str(env.get("Value"))
            runs_tool = any(isinstance(w, str) and w.startswith(TOOL_DIR)
                            for w in (c.get("Command") or []))
            if declared is not None:
                found.add(declared)
            elif runs_tool:
                undeclared.append(f"{name}/{c.get('Name')}")
    return found, undeclared


def main(argv):
    if len(argv) != 2:
        print(__doc__.strip().splitlines()[-2], file=sys.stderr)
        return 1
    found, undeclared = scan(argv[1])
    if undeclared:
        print(f"cfn-contract: {argv[1]}: these run an engine-tools script and declare no "
              f"ENGINE_TOOLS_CONTRACT: {', '.join(undeclared)}", file=sys.stderr)
        return 1
    if len(found) != 1:
        print(f"cfn-contract: {argv[1]} declares {len(found) or 'no'} ENGINE_TOOLS_CONTRACT "
              f"value(s){': ' + ', '.join(sorted(found)) if found else ''}", file=sys.stderr)
        return 1
    print(found.pop())
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
