#!/usr/bin/env python3
"""Check that a prose-only rewrite of guide pages kept everything that is not prose.

Compares each page with its content at a git ref and fails when any of these differ:
front matter, headings, link targets, fenced code, inline code, URLs, or the multiset of
numbers in the body. It exists for wording passes (humanizer, yomiyasu and the like),
where a changed anchor, command or number would break links, the Japanese twins or the
tests that read guide/ref/agents.md and guide/ref/features.md.

    python3 scripts/guide-diff-check.py <ref> [--tables] [files...]

With no files, it checks every changed English page under guide/ against <ref>
(`git diff --name-only <ref> -- guide`, `.ja.md` excluded). `--tables` also requires every
table row to be identical, for pages whose rows are keys of another file
(guide/ref/features.md, guide/ref/agents.md). Em dash counts are printed per file for
reporting; they never fail the run.

This does not judge meaning. Pair it with a read of the diff and with
scripts/docs-check.py. Exit status: 0 when nothing differs, 1 otherwise.
"""
import collections
import re
import subprocess
import sys

FENCE = re.compile(r'^(```.*?^```|~~~.*?^~~~)', re.S | re.M)
FRONT = re.compile(r'---\n.*?\n---\n', re.S)


def features(text, tables):
    front = FRONT.match(text)
    body = FENCE.sub('', text)
    lines = body.split('\n')
    f = {
        'front matter': [front.group(0) if front else ''],
        'headings': [l for l in text.split('\n') if re.match(r'#{1,6} ', l)],
        'fenced code': FENCE.findall(text),
    }
    # Compared as multisets: a rewrite may reorder words, never add or drop one.
    m = {
        'link targets': re.findall(r'\]\(([^)]*)\)', body),
        'inline code': re.findall(r'`[^`\n]+`', body),
        'urls': re.findall(r'https?://[^\s)>\]]+', text),
        'numbers': re.findall(r'\d+', body),
    }
    if tables:
        f['table rows'] = [l for l in lines if l.startswith('|')]
    return f, {k: collections.Counter(v) for k, v in m.items()}


def git(*args):
    return subprocess.run(['git', *args], capture_output=True, text=True, check=True).stdout


def main(argv):
    tables = '--tables' in argv
    args = [a for a in argv if a != '--tables']
    if not args:
        sys.exit(__doc__)
    ref, files = args[0], args[1:]
    if not files:
        files = [f for f in git('diff', '--name-only', ref, '--', 'guide').split()
                 if f.endswith('.md') and not f.endswith('.ja.md')]
    bad = 0
    for path in files:
        old = git('show', f'{ref}:{path}')
        with open(path, encoding='utf-8') as fh:
            new = fh.read()
        (fo, mo), (fn, mn) = features(old, tables), features(new, tables)
        for k in fo:
            if fo[k] != fn[k]:
                bad += 1
                print(f'DIFF {k}: {path}')
        for k in mo:
            if mo[k] != mn[k]:
                bad += 1
                print(f'DIFF {k}: {path}')
                print('  removed:', list((mo[k] - mn[k]).elements())[:8])
                print('  added:  ', list((mn[k] - mo[k]).elements())[:8])
        print(f'{path}: em dash {old.count("—")} -> {new.count("—")}')
    print(f'{len(files)} file(s), {bad} difference(s)')
    return 1 if bad else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
