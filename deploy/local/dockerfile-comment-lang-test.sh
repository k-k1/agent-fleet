#!/usr/bin/env bash
# Every comment in a tracked Dockerfile is written in English.
#
#   deploy/local/dockerfile-comment-lang-test.sh
#
# AGENTS.md requires English comments in every language, and review alone let 197 Japanese
# comment lines pile up in the Dockerfiles. This check flags a full-line comment (`#` as the
# first non-blank character, which is the only comment form a Dockerfile has) that contains
# Japanese script: kana, CJK ideographs (all planes), CJK punctuation or fullwidth forms.
# Non-comment lines are left alone: Japanese in a user-visible string is allowed.
#
# Every `#` line counts, including one inside a here-document body. Telling those apart takes
# BuildKit's full parser (quote removal, continuation joining, ONBUILD, `<<<`), and each
# approximation of it hid real comments in review. Erring this way can only fail the build,
# never pass a Japanese comment; no tracked Dockerfile uses a here-document today.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PY="$(mktemp -d -t dockerfile-lang-XXXXXX)"
trap 'rm -rf "$PY"' EXIT

cat > "$PY/lang.py" <<'EOF'
import re, subprocess, sys

JAPANESE = re.compile(
    '[\u3000-\u303f\u3040-\u309f\u30a0-\u30ff\u31f0-\u31ff\u3400-\u4dbf'
    '\u4e00-\u9fff\uf900-\ufaff\uff00-\uffef\U00020000-\U0003ffff]')
NAME = re.compile(r'(^|/)(Dockerfile[^/]*|[^/]*\.Dockerfile|Containerfile[^/]*)$', re.I)


def is_dockerfile(path):
    return bool(NAME.search(path)) and not path.endswith('.sh')


def scan(path):
    bad = 0
    with open(path, encoding='utf-8') as fh:
        for n, line in enumerate(fh, 1):
            body = line.rstrip('\n')
            if body.lstrip().startswith('#') and JAPANESE.search(body):
                bad += 1
                print(f'{path}:{n}: {body}')
    return bad


mode, args = sys.argv[1], sys.argv[2:]
if mode == 'names':  # NUL-separated paths on stdin -> the ones taken as Dockerfiles
    for p in sys.stdin.buffer.read().decode('utf-8').split('\0'):
        if p and is_dockerfile(p):
            print(p)
    sys.exit(0)
if mode == 'tracked':  # args: repo root
    out = subprocess.run(['git', '-C', args[0], 'ls-files', '-z'],
                         check=True, capture_output=True).stdout.decode('utf-8')
    files = [f'{args[0]}/{p}' for p in out.split('\0') if p and is_dockerfile(p)]
    if not files:
        print('no Dockerfiles found; the check is inert', file=sys.stderr)
        sys.exit(2)
    print(f'{len(files)} Dockerfile(s)', file=sys.stderr)
else:  # scan: args are files
    files = args
sys.exit(1 if sum(scan(f) for f in files) else 0)
EOF

fail() { echo "dockerfile-comment-lang: FAIL -- $*" >&2; exit 1; }
scan() { python3 "$PY/lang.py" scan "$@" > /dev/null 2>&1; }

# Controls first: a scanner that never ran and a clean tree look identical.
d="$PY/probe"; mkdir "$d"
printf 'FROM scratch\n  # \xe6\x97\xa5\xe6\x9c\xac\xe8\xaa\x9e comment\n' > "$d/kana"
printf '# \xef\xa8\x91 compatibility ideograph\n' > "$d/compat"
printf '# \xf0\xa0\xae\xb7 supplementary-plane ideograph\n' > "$d/supp"
printf 'COPY <<EOF /a\n# \xe6\x97\xa5 in a here-document body, flagged on purpose\nEOF\n' > "$d/heredoc"
for f in kana compat supp heredoc; do
  scan "$d/$f" && fail "a planted Japanese comment ($f) was not flagged; the check is inert"
done
printf '# English \xe2\x80\x94 with an em dash\nRUN echo "\xe6\x97\xa5\xe6\x9c\xac\xe8\xaa\x9e"\n' > "$d/string"
scan "$d/string" || fail "flagged an English comment or a non-comment line"
names="$(printf '%s\0' 'a/Dockerfile' 'b/jvm.Dockerfile' $'\xe6\x97\xa5/jvm.Dockerfile' \
  $'\xe6\x97\xa5/Dockerfile' 'c/Containerfile' 'w/dockerfile-x-test.sh' 'd/notes.md' \
  | python3 "$PY/lang.py" names | wc -l)"
[ "$names" -eq 5 ] || fail "file selection took $names of 5 Dockerfile names (non-ASCII paths?)"
echo "dockerfile-comment-lang: controls OK"

rc=0
python3 "$PY/lang.py" tracked "$ROOT" || rc=$?
case "$rc" in
  0) echo "dockerfile-comment-lang: OK -- no Japanese comments" ;;
  1) fail "write the comments above in English (AGENTS.md, Comments)" ;;
  *) fail "the scanner did not run (exit $rc)" ;;
esac
