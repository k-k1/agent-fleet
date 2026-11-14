#!/usr/bin/env bash
# Every comment in a tracked Dockerfile is written in English.
#
#   deploy/local/dockerfile-comment-lang-test.sh
#
# AGENTS.md requires English comments in every language, and review alone let 197 Japanese
# comment lines pile up in the Dockerfiles. This check flags a full-line comment (`#` as the
# first non-blank character, which is the only comment form a Dockerfile has) that contains
# Japanese script: hiragana, katakana, CJK ideographs, CJK punctuation or fullwidth forms.
# Non-comment lines are left alone: Japanese in a user-visible string is allowed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# scan <file>... -- prints every offending line, returns 1 if any were found.
scan() {
  python3 - "$@" <<'PY'
import re, sys
japanese = re.compile('[\u3000-\u303f\u3040-\u309f\u30a0-\u30ff\u31f0-\u31ff'
                      '\u3400-\u4dbf\u4e00-\u9fff\uff00-\uffef]')
bad = 0
for p in sys.argv[1:]:
    with open(p, encoding='utf-8') as fh:
        for n, line in enumerate(fh, 1):
            if line.lstrip().startswith('#') and japanese.search(line):
                bad += 1
                print(f'{p}:{n}: {line.rstrip()}')
sys.exit(1 if bad else 0)
PY
}

# Controls first: a scanner that never ran and a clean tree look identical.
probe_dir="$(mktemp -d -t dockerfile-lang-probe-XXXXXX)"
trap 'rm -rf "$probe_dir"' EXIT
printf 'FROM scratch\n  # \xe6\x97\xa5\xe6\x9c\xac\xe8\xaa\x9e comment\n' > "$probe_dir/Dockerfile.ja"
if scan "$probe_dir/Dockerfile.ja" > /dev/null 2>&1; then
  echo "dockerfile-comment-lang: FAIL -- a planted Japanese comment was not flagged; the check is inert" >&2
  exit 1
fi
# An English comment with non-ASCII punctuation, and Japanese outside a comment, must pass.
printf '# English \xe2\x80\x94 with an em dash\nRUN echo "\xe6\x97\xa5\xe6\x9c\xac\xe8\xaa\x9e"\n' > "$probe_dir/Dockerfile.ok"
if ! scan "$probe_dir/Dockerfile.ok" > /dev/null 2>&1; then
  echo "dockerfile-comment-lang: FAIL -- flagged an English comment or a non-comment line" >&2
  exit 1
fi
echo "dockerfile-comment-lang: controls OK (Japanese comment caught, English comment and string passed)"

mapfile -t FILES < <(git -C "$ROOT" ls-files \
  | grep -iE '(^|/)(Dockerfile[^/]*|[^/]*\.Dockerfile|Containerfile[^/]*)$' \
  | grep -vE '\.sh$' | sed "s|^|$ROOT/|")
if [ "${#FILES[@]}" -eq 0 ]; then
  echo "dockerfile-comment-lang: FAIL -- no Dockerfiles found; the check is inert" >&2
  exit 1
fi

if ! scan "${FILES[@]}"; then
  echo >&2
  echo "dockerfile-comment-lang: FAIL -- write these comments in English (AGENTS.md, Comments)." >&2
  exit 1
fi

echo "dockerfile-comment-lang: OK -- ${#FILES[@]} Dockerfile(s), no Japanese comments"
