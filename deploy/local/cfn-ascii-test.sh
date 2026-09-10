#!/usr/bin/env bash
# Every CloudFormation template stays pure ASCII.
#
#   deploy/local/cfn-ascii-test.sh
#
# ## Why this exists
#
# CloudFormation does not preserve non-ASCII in a template body. It replaces **every
# non-ASCII codepoint with `?`**, and it does so server-side: the source file on disk is
# fine, the AWS CLI sends correct UTF-8 with `Content-Type: charset=utf-8`, and what comes
# back out of `get-template` is mangled. Measured 2026-09-10 against two live deployments,
# both of which held zero non-ASCII characters where the source had 69.
#
# The positive control that pins the blame: the *same* CLI, account and machine round-trips
# the identical string through SSM `put-parameter` / `get-parameter` untouched. So this is
# not a locale problem, not botocore, and not `af_cfn_deploy` -- there is nothing to fix on
# our side, only a rule to keep.
#
# It matters beyond looks. A `?` lands in parameter Descriptions (which operators read in
# the CloudFormation console) and inside the embedded shell of `Mappings` -- 60-engines had
# four `echo` lines shipping a literal `?` to the engine log. A non-ASCII character in a
# path, a pattern or a comparison would be a real defect rather than a cosmetic one.
#
# Writing `--`, `!!` and `->` instead is also cheaper: dropping the 281 characters this
# check was written for freed 454 bytes, and 60-engines.yaml lives 208 bytes under the
# 51,200-byte inline limit that `af_cfn_deploy` measures.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FAILED=0

# scan <label> <file>... -- prints every offending line, returns 1 if any were found.
scan() {
  local label="$1"; shift
  python3 - "$label" "$@" <<'PY'
import sys
label, paths = sys.argv[1], sys.argv[2:]
bad = 0
for p in paths:
    with open(p, encoding='utf-8') as fh:
        for n, line in enumerate(fh, 1):
            offenders = sorted({c for c in line if ord(c) > 127})
            if offenders:
                bad += 1
                shown = ' '.join(f'U+{ord(c):04X} {c!r}' for c in offenders)
                print(f'{p}:{n}: non-ASCII: {shown}')
sys.exit(1 if bad else 0)
PY
}

# The positive control comes first. A scanner that never ran and a clean tree look identical,
# so prove the detector fires before trusting it on the real templates.
probe="$(mktemp -t cfn-ascii-probe-XXXXXX.yaml)"
trap 'rm -f "$probe"' EXIT
printf 'Description: em-dash \xe2\x80\x94 here\n' > "$probe"
if scan "probe" "$probe" > /dev/null 2>&1; then
  echo "cfn-ascii: FAIL -- the detector did not flag a known non-ASCII line; the check is inert" >&2
  exit 1
fi
echo "cfn-ascii: positive control OK (a planted em-dash is caught)"

mapfile -t TEMPLATES < <(find "$HERE/deploy" -name '*.yaml' -type f | sort)
if [ "${#TEMPLATES[@]}" -eq 0 ]; then
  echo "cfn-ascii: FAIL -- no templates found under deploy/; the check is inert" >&2
  exit 1
fi

if ! scan "templates" "${TEMPLATES[@]}"; then
  FAILED=1
fi

if [ "$FAILED" -ne 0 ]; then
  cat >&2 <<'MSG'

cfn-ascii: FAIL -- CloudFormation would replace each character above with `?`.
  Use ASCII: `-` for an em dash, `!!` for a warning sign, `->` for an arrow,
  `...` for an ellipsis, `sec.` for a section sign. Comments are English anyway
  (AGENTS.md), so Japanese belongs in docs/, never in a template.
MSG
  exit 1
fi

echo "cfn-ascii: OK -- ${#TEMPLATES[@]} template(s), 0 non-ASCII characters"
