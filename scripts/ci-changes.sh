#!/usr/bin/env bash
# Decides whether a pull request touches anything ci.yml's build/test jobs read.
#
#   git diff --name-only --no-renames A B | scripts/ci-changes.sh   # prints code=true|false
#
# ci.yml's `changes` job appends the line to $GITHUB_OUTPUT, and the control-plane,
# workspace-agent, deploy-scripts and console jobs run only when it says `code=true`. A
# docs-only PR otherwise waits on the full Go and Console suites, none of which can change
# its outcome (#1573). The scans (secret-scan, release-scan) and docs.yml run regardless.
#
# The list below names what is inert, not what each job reads: the jobs read across each
# other's trees (a Console test parses workspace/agent Go source), so per-job path filters
# would skip a job that a change does break. Anything not listed counts as code, and so
# does an empty diff — when in doubt, answer true.
#
# Before adding a path here, grep the tests and scripts the four jobs run for it. Two files
# under guide/ are read by a test and are therefore code:
#   guide/ref/agents.md, guide/ref/agents.ja.md  (console/src/agents/guideTable.test.ts)
# The positive and negative controls are scripts/ci-changes-test.sh.
set -euo pipefail

inert() {
  case "$1" in
    guide/ref/agents.md | guide/ref/agents.ja.md) return 1 ;;
    docs/* | guide/*) return 0 ;;
    scripts/docs-check.py | scripts/test_docs_check.py) return 0 ;;
    */*) return 1 ;;
    *.md | LICENSE | NOTICE) return 0 ;;
  esac
  return 1
}

seen=0
while IFS= read -r f; do
  [ -n "$f" ] || continue
  seen=1
  if ! inert "$f"; then
    echo "code=true"
    exit 0
  fi
done

if [ "$seen" -eq 0 ]; then
  echo "code=true"
else
  echo "code=false"
fi
