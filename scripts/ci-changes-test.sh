#!/usr/bin/env bash
# Positive and negative controls for scripts/ci-changes.sh. Run by ci.yml's `changes` job
# before it classifies the PR, so a broken classifier fails there instead of quietly
# skipping the build/test jobs.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FAILED=0

check() {
  local want="$1" input="$2" got
  got="$(printf '%b' "$input" | "$HERE/ci-changes.sh")"
  if [ "$got" != "code=$want" ]; then
    echo "FAIL: want code=$want, got $got for: $(printf '%b' "$input" | tr '\n' ' ')"
    FAILED=1
  fi
}

# Inert only.
check false 'docs/log/123-x.md\n'
check false 'docs/img/a.png\nguide/member/x.ja.md\n'
check false 'README.md\nREADME.ja.md\nCONTRIBUTING.md\nAGENTS.md\nLICENSE\nNOTICE\n'
check false 'scripts/docs-check.py\nscripts/test_docs_check.py\n'
check false 'guide/ref/agents-other.md\n'

# Code, alone or mixed in with inert files.
check true 'control-plane/main.go\n'
check true 'docs/a.md\nconsole/src/App.tsx\n'
check true 'guide/ref/agents.md\n'
check true 'docs/a.md\nguide/ref/agents.ja.md\n'
check true 'workspace/workspace-notes.md\n'
check true 'workspace/agent/knowledge/af-usage.md\n'
check true 'deploy/kubernetes/README.md\n'
check true '.github/workflows/ci.yml\n'
check true 'scripts/ci-changes.sh\n'
check true 'go.work\n'

# Nothing to classify.
check true ''
check true '\n'

if [ "$FAILED" -ne 0 ]; then
  exit 1
fi
echo "ci-changes: all checks passed"
