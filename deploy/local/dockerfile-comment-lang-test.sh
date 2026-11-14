#!/usr/bin/env bash
# Every comment in a tracked Dockerfile is written in English.
#
#   deploy/local/dockerfile-comment-lang-test.sh
#
# AGENTS.md requires English comments in every language, and review alone let 197 Japanese
# comment lines pile up in the Dockerfiles. This check flags a full-line comment (`#` as the
# first non-blank character, which is the only comment form a Dockerfile has) that contains
# Japanese script: kana, CJK ideographs (all planes), CJK punctuation or fullwidth forms.
# Non-comment lines are left alone, and so is the body of a here-document: Japanese in a
# user-visible string or a file the build writes is allowed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PY="$(mktemp -d -t dockerfile-lang-XXXXXX)"
trap 'rm -rf "$PY"' EXIT

cat > "$PY/lang.py" <<'EOF'
import re, shlex, subprocess, sys

JAPANESE = re.compile(
    '[\u3000-\u303f\u3040-\u309f\u30a0-\u30ff\u31f0-\u31ff\u3400-\u4dbf'
    '\u4e00-\u9fff\uf900-\ufaff\uff00-\uffef\U00020000-\U0003ffff]')
NAME = re.compile(r'(^|/)(Dockerfile[^/]*|[^/]*\.Dockerfile|Containerfile[^/]*)$', re.I)
# Here-documents are found the way BuildKit's parser finds them (frontend/dockerfile/parser:
# canContainHeredoc, heredocsFromLine): only in RUN / COPY / ADD, only in an unquoted shell
# word of the form `[fd]<<[-]word`, with the terminator being that whole word unquoted, and
# only once the instruction's continuation lines are complete.
HEREDOC_INSTR = {'RUN', 'COPY', 'ADD'}
HEREDOC_WORD = re.compile(r'^\d*<<(-?)(.+)$')


def is_dockerfile(path):
    return bool(NAME.search(path)) and not path.endswith('.sh')


def heredocs(logical):
    """(strip_tabs, terminator) for each here-document an instruction line opens."""
    parts = logical.split(None, 1)
    if len(parts) < 2 or parts[0].upper() not in HEREDOC_INSTR:
        return []
    try:
        words = shlex.split(parts[1], posix=False)  # keeps quotes, so "<<EOF" stays a string
    except ValueError:
        return []
    found = []
    for w in words:
        m = HEREDOC_WORD.match(w)
        if m:
            term = m.group(2)
            if len(term) >= 2 and term[0] == term[-1] and term[0] in '"\'':
                term = term[1:-1]
            found.append((m.group(1) == '-', term))
    return found


def scan(path):
    bad = 0
    pending = []  # here-documents still open, in order
    logical = []  # physical pieces of the instruction being assembled
    with open(path, encoding='utf-8') as fh:
        for n, line in enumerate(fh, 1):
            body = line.rstrip('\n')
            if pending:
                strip, term = pending[0]
                if (body.lstrip('\t') if strip else body) == term:
                    pending.pop(0)
                continue
            if body.lstrip().startswith('#'):  # also between continuation lines, as Docker strips them
                if JAPANESE.search(body):
                    bad += 1
                    print(f'{path}:{n}: {body}')
                continue
            if body.rstrip().endswith('\\'):
                logical.append(body.rstrip()[:-1])
                continue
            logical.append(body)
            pending.extend(heredocs(' '.join(logical)))
            logical = []
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
printf 'COPY <<EOF /a\ntext\nEOF\n# \xe6\x97\xa5\xe6\x9c\xac after the heredoc closed\n' > "$d/after"
printf 'ENV NOTE <<EOF\n# \xe6\x97\xa5 not a heredoc: ENV cannot open one\n' > "$d/env"
printf 'RUN echo "<<EOF"\n# \xe6\x97\xa5 not a heredoc: quoted word\n' > "$d/quoted"
printf 'COPY <<EOF.txt /a\n# \xe6\x97\xa5\nEOF.txt\n# \xe6\x97\xa5 after a punctuated terminator\n' > "$d/dotted"
printf 'RUN <<EOF \\\n# \xe6\x97\xa5 comment inside the continued header\n    sh\necho hello\nEOF\n' > "$d/continued"
for f in kana compat supp after env quoted dotted continued; do
  scan "$d/$f" && fail "a planted Japanese comment ($f) was not flagged; the check is inert"
done
printf '# English \xe2\x80\x94 with an em dash\nRUN echo "\xe6\x97\xa5\xe6\x9c\xac\xe8\xaa\x9e"\n' > "$d/string"
printf 'COPY <<EOF /help.md\n# \xe6\x97\xa5\xe6\x9c\xac\nEOF\n' > "$d/heredoc"
printf 'RUN <<-"A" cat > /x && <<'"'"'B'"'"' cat > /y\n\t# \xe6\x97\xa5\n\tA\n# \xe6\x97\xa5\nB\n' > "$d/heredocs"
printf 'COPY <<"EOF.txt" /a\n# \xe6\x97\xa5\nEOF.txt\nCOPY <<1 /b\n# \xe6\x97\xa5\n1\n' > "$d/terms"
for f in string heredoc heredocs terms; do
  scan "$d/$f" || fail "flagged an English comment, a string or a here-document body ($f)"
done
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
