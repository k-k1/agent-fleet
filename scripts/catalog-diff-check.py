#!/usr/bin/env python3
"""Check that a wording rewrite of the Japanese Console catalogue kept its structure.

Compares console/src/lib/i18n/locales/ja/<domain>.ts with its content at a git ref and
fails when a rewrite changed anything but the wording of values. Local use only; it is
not wired into CI (the same stance as scripts/guide-diff-check.py).

    python3 scripts/catalog-diff-check.py <ref> [--allow-labels] [--list-pinned] [files...]

With no files, it checks every ja/*.ts file that differs from <ref> in the working tree.
Per file, it fails (category in brackets) when:

  skeleton   anything outside string-literal contents changed: comments, `+` structure,
             quote style, key literals, order of entries
  keys       the key set (or its order) changed, or a file was added or removed
  outside    anything under locales/ other than ja/ changed (en/ catalogues, en.ts, ja.ts)

and, for every value whose text changed, when the multiset of any of these differs:

  placeholders  {name}                 slots   Trans slots <n>…</n> and <n/>
  digits        runs of digits         latin   ASCII words
  kagi          「…」 contents          code    `code` spans
  newlines      line breaks            edge    leading/trailing whitespace
  glossary      terms of guide/ref/glossary.ja.md (first column; "（…）" notes dropped,
                a 「…」 inside them kept; terms shorter than 2 characters are skipped)

Labels (UI labels: at most 15 characters, no 「。」) may be reworded only on purpose:
without --allow-labels each changed label fails [label]; with it, every `old -> new` pair
is printed (the review list) and the value is still held to every check above.

PINNED: when the old text of a changed value is still quoted elsewhere, the quote is now
stale. Searched: guide/**/*.ja.md, console tests (console/**/*.test.*, console-e2e/),
Go sources, workspace/agent/knowledge/af-usage.md. A clause of the old value (split at
「。」, line breaks and placeholders; at least 8 characters; not still present in the new
value) counts, and so does a label quoted exactly as 「label」 or **label** (in code also
as "label", 'label' or `label`). Each hit is printed with file:line and fails the run [pinned];
update that citation in the same PR. `--list-pinned` prints only those locations (key,
file:line, old text) and exits 0, for the label-sync step.
A short common-word label can also be quoted for another purpose (a test asserting a
reply language, say). After reading the hit and confirming it does not cite the Console
string, accept exactly that hit with `--exempt-pin KEY@PATH[:LINE]` (repeatable; without
LINE every hit of the key in that file). Exempted hits are printed as EXEMPT and counted,
unmatched exemptions are warned about, and other hits of the key still fail.
Limitation: a guide quote that reproduces only the start of a sentence (under 8 characters,
or cut before the part the rewrite changed) is invisible here, because there is no old
fragment to match. After a rewrite, also search the guide for the first words of each
changed value that is cited by sentence.

Counts are always printed: values checked, values changed, failures per category.
Exit status: 0 clean, 1 any FAIL or PINNED, 2 the check could not run (bad ref, no node).
It does not judge meaning. Pair it with a read of the diff.
"""
import argparse
import bisect
import collections
import fnmatch
import json
import os
import re
import subprocess
import sys
import tempfile

CATALOGUE = 'console/src/lib/i18n/locales'
JA_DIR = CATALOGUE + '/ja/'
GLOSSARY = 'guide/ref/glossary.ja.md'
LABEL_MAX = 15
FRAGMENT_MIN = 8

INVARIANTS = {
    'placeholders': re.compile(r'\{\w+\}'),
    'slots': re.compile(r'</?\d+/?>'),
    'digits': re.compile(r'\d+'),
    'latin': re.compile(r'[A-Za-z]+'),
    'kagi': re.compile(r'「[^」]*」'),
    'code': re.compile(r'`[^`]*`'),
}
SPLIT_CLAUSE = re.compile(r'\{\w+\}|</?\d+/?>|[。\n]')

CATEGORIES = ['skeleton', 'keys', 'outside', *INVARIANTS, 'newlines', 'edge', 'glossary',
              'label', 'pinned']

# Where an old quote may still live. Matched against `git ls-files` paths.
PIN_GLOBS = ['guide/*.ja.md', 'console/*.test.*', 'console-e2e/*.ts', 'control-plane/*.go',
             'workspace/*.go', 'e2e/*.go', 'deploy/*.go', 'workspace/agent/knowledge/af-usage.md']
CODE_SUFFIXES = ('.go', '.ts', '.tsx', '.js', '.jsx', '.mjs')


class Fail(Exception):
    pass


def git(*args, check=True):
    p = subprocess.run(['git', *args], capture_output=True, text=True)
    if check and p.returncode:
        raise Fail(f'git {" ".join(args)}: {p.stderr.strip()}')
    return p.stdout


def skeleton(text):
    """The file with string-literal contents emptied. Keys stay verbatim, comments too."""
    out, i, n = [], 0, len(text)
    while i < n:
        c = text[i]
        if text.startswith('//', i):
            j = text.find('\n', i)
            j = n if j < 0 else j
            out.append(text[i:j])
            i = j
        elif text.startswith('/*', i):
            j = text.find('*/', i + 2)
            if j < 0:
                raise Fail('unterminated block comment')
            out.append(text[i:j + 2])
            i = j + 2
        elif c in '"\'`':
            j = i + 1
            while j < n and text[j] != c:
                if text[j] == '\\':
                    j += 1
                elif text[j] == '\n' and c != '`':
                    raise Fail('unterminated string literal')
                elif c == '`' and text.startswith('${', j):
                    raise Fail('template substitution is not supported')
                j += 1
            if j >= n:
                raise Fail('unterminated string literal')
            k = j + 1
            while k < n and text[k] in ' \t':
                k += 1
            is_key = k < n and text[k] == ':'
            out.append(text[i:j + 1] if is_key else c + c)
            i = j + 1
        else:
            out.append(c)
            i += 1
    return ''.join(out)


NODE_DUMP = ("const m=await import(process.argv[1]);"
             "process.stdout.write(JSON.stringify(Object.values(m)[0]))")


def evaluate(path):
    try:
        p = subprocess.run(['node', '--experimental-strip-types', '--no-warnings',
                            '--input-type=module', '-e', NODE_DUMP, os.path.abspath(path)],
                           capture_output=True, text=True)
    except FileNotFoundError:
        raise Fail('node is required (it evaluates the catalogue files)')
    if p.returncode:
        raise Fail(f'node could not evaluate {path}: {p.stderr.strip()[:300]}')
    return json.loads(p.stdout)


def glossary_terms(text):
    terms = []
    for line in text.split('\n'):
        if not line.startswith('|'):
            continue
        cell = line.strip('|').split('|')[0].replace('`', '').strip()
        if not cell or set(cell) <= set('-: ') or cell == '画面':
            continue
        base = re.split(r'[（(]', cell)[0].strip()
        terms += [base, *re.findall(r'「([^」]+)」', cell)]
    return sorted({t for t in terms if len(t) >= 2 and '/' not in t})


def label_like(v):
    return len(v) <= LABEL_MAX and '。' not in v


def clauses(old, new):
    return [s for s in SPLIT_CLAUSE.split(old) if len(s) >= FRAGMENT_MIN and s not in new]


class Sources:
    """Every file a quote of a catalogue value could live in, with hard wraps folded away so
    a sentence wrapped across lines (even indented) still matches."""

    def __init__(self):
        self.files = {}
        names = git('ls-files', '-co', '--exclude-standard', '-z').split('\0')
        for name in names:
            if not name or name.startswith(CATALOGUE + '/'):
                continue
            if not any(fnmatch.fnmatch(name, g) for g in PIN_GLOBS):
                continue
            try:
                with open(name, encoding='utf-8') as fh:
                    text = fh.read()
            except (OSError, UnicodeDecodeError):
                continue
            # Fold hard wraps: a continuation line loses its indentation, and a blank
            # line is a barrier so text from two paragraphs never joins into a quote.
            starts, flat, pos = [], [], 0
            for ln in text.split('\n'):
                ln = ln.strip() and ln.lstrip(' \t') or '\0'
                starts.append(pos)
                flat.append(ln)
                pos += len(ln)
            self.files[name] = (''.join(flat), starts)

    def find(self, needle):
        hits = []
        for name, (flat, starts) in self.files.items():
            at = flat.find(needle)
            while at >= 0:
                hits.append((name, bisect.bisect_right(starts, at)))
                at = flat.find(needle, at + 1)
        return hits

    def find_label(self, label):
        hits = []
        for name in self.files:
            forms = ['「%s」' % label, '**%s**' % label]
            if name.endswith(CODE_SUFFIXES):
                forms += ['"%s"' % label, "'%s'" % label, '`%s`' % label]
            for f in forms:
                hits += [h for h in self.find_in(name, f)]
        return hits

    def find_in(self, name, needle):
        flat, starts = self.files[name]
        at = flat.find(needle)
        while at >= 0:
            yield name, bisect.bisect_right(starts, at)
            at = flat.find(needle, at + 1)


def main(argv):
    ap = argparse.ArgumentParser(prog='catalog-diff-check.py', usage='%(prog)s <ref> [--allow-labels] [--list-pinned] [files...]')
    ap.add_argument('ref')
    ap.add_argument('files', nargs='*')
    ap.add_argument('--allow-labels', action='store_true')
    ap.add_argument('--list-pinned', action='store_true')
    ap.add_argument('--exempt-pin', action='append', default=[], metavar='KEY@PATH[:LINE]',
                    help='accept one reviewed PINNED hit that is not a citation of the value')
    args = ap.parse_args(argv)

    exempt = []
    for spec in args.exempt_pin:
        key, sep, loc = spec.partition('@')
        path, _, line = loc.partition(':')
        if not (sep and key and path) or (line and not line.isdigit()):
            print(f'error: bad --exempt-pin {spec!r} (want KEY@PATH[:LINE])', file=sys.stderr)
            return 2
        exempt.append((key, path, line, spec))
    used = set()
    exempted = 0
    fails = collections.Counter()
    listing = args.list_pinned

    def fail(cat, msg):
        fails[cat] += 1
        if not listing:
            print(f'{"PINNED" if cat == "pinned" else "FAIL " + cat}: {msg}')

    try:
        root = git('rev-parse', '--show-toplevel').strip()
        os.chdir(root)
        if subprocess.run(['git', 'rev-parse', '--verify', '--quiet', args.ref + '^{commit}'],
                          capture_output=True).returncode:
            raise Fail(f'unknown ref {args.ref!r}')
        changed = [f for f in git('diff', '--name-only', '-z', args.ref, '--', JA_DIR).split('\0') if f]
        changed += [f for f in git('ls-files', '-o', '--exclude-standard', '-z', '--', JA_DIR).split('\0') if f]
        files = args.files or sorted(set(f for f in changed if f.endswith('.ts')))
        for f in files:
            if not (f.startswith(JA_DIR) and f.endswith('.ts')):
                raise Fail(f'{f}: not a {JA_DIR}*.ts file')
        with open(GLOSSARY, encoding='utf-8') as fh:
            terms = glossary_terms(fh.read())
        sources = Sources()
    except Fail as e:
        print(f'error: {e}', file=sys.stderr)
        return 2

    # Anything under locales/ outside ja/: en catalogues must not move in a ja rewrite.
    outside = set(git('diff', '--name-only', '-z', args.ref, '--', CATALOGUE).split('\0'))
    outside |= set(git('ls-files', '-o', '--exclude-standard', '-z', '--', CATALOGUE).split('\0'))
    for f in sorted(x for x in outside if x and not x.startswith(JA_DIR)):
        fail('outside', f'{f} changed (only ja/*.ts may change)')

    checked = changed_n = 0
    labels = []
    pins = []
    tmpdir = tempfile.TemporaryDirectory(dir=os.environ.get('AF_WORK_DIR') or None)
    try:
        for path in files:
            old_p = subprocess.run(['git', 'show', f'{args.ref}:{path}'], capture_output=True, text=True)
            if old_p.returncode:
                fail('keys', f'{path}: not present at {args.ref} (added file)')
                continue
            if not os.path.exists(path):
                fail('keys', f'{path}: deleted')
                continue
            with open(path, encoding='utf-8') as fh:
                new_t = fh.read()
            old_t = old_p.stdout
            try:
                if skeleton(old_t) != skeleton(new_t):
                    fail('skeleton', f'{path}: something outside string-literal contents changed (comment, key, order, `+`, quotes)')
                old_f = os.path.join(tmpdir.name, 'old.ts')
                with open(old_f, 'w', encoding='utf-8') as fh:
                    fh.write(old_t)
                old, new = evaluate(old_f), evaluate(path)
            except Fail as e:
                print(f'error: {path}: {e}', file=sys.stderr)
                return 2
            if set(old) != set(new):
                fail('keys', f'{path}: removed={sorted(set(old) - set(new))[:8]} added={sorted(set(new) - set(old))[:8]}')
            elif list(old) != list(new):
                fail('keys', f'{path}: key order differs')
            for key, o in old.items():
                checked += 1
                n = new.get(key)
                if n is None or n == o:
                    continue
                changed_n += 1
                where = f'{path} {key}'
                for name, rx in INVARIANTS.items():
                    a, b = collections.Counter(rx.findall(o)), collections.Counter(rx.findall(n))
                    if a != b:
                        fail(name, f'{where}: removed={list((a - b).elements())[:6]} added={list((b - a).elements())[:6]}')
                if o.count('\n') != n.count('\n'):
                    fail('newlines', f'{where}: {o.count(chr(10))} -> {n.count(chr(10))}')
                if o[:len(o) - len(o.lstrip())] != n[:len(n) - len(n.lstrip())] or \
                        o[len(o.rstrip()):] != n[len(n.rstrip()):]:
                    fail('edge', f'{where}: leading/trailing whitespace changed')
                for t in terms:
                    if o.count(t) != n.count(t):
                        fail('glossary', f'{where}: 「{t}」 {o.count(t)} -> {n.count(t)}')
                if label_like(o) or label_like(n):
                    labels.append((where, o, n))
                    if not args.allow_labels:
                        fail('label', f'{where}: label {o!r} -> {n!r} (pass --allow-labels to reword labels)')
                found = set()
                for frag in clauses(o, n):
                    found.update((h, frag) for h in sources.find(frag))
                if label_like(o):
                    found.update((h, o) for h in sources.find_label(o))
                for (name, line), text in sorted(found):
                    hit = next((e for e in exempt if e[0] == key and e[1] == name and e[2] in ('', str(line))), None)
                    if hit:
                        used.add(hit[3])
                        exempted += 1
                        if not listing:
                            print(f'EXEMPT: {where}: reviewed, not a citation: {name}:{line}')
                        continue
                    pins.append((key, f'{name}:{line}', text))
                    fail('pinned', f'{where}: old text still at {name}:{line}: {text[:40]!r}')
    finally:
        tmpdir.cleanup()

    if listing:
        for key, loc, text in pins:
            print(f'{loc}\t{key}\t{text}')
        print(f'{len(pins)} pinned location(s)', file=sys.stderr)
        return 0
    if labels and args.allow_labels:
        print('changed labels:')
        for where, o, n in labels:
            print(f'  LABEL {where}: {o} -> {n}')
    for _, _, _, spec in exempt:
        if spec not in used:
            print(f'WARN: --exempt-pin {spec} matched nothing (stale exemption)')
    print(f'{len(files)} file(s), {checked} value(s) checked, {changed_n} changed, {exempted} pin(s) exempted')
    print('failures: ' + ', '.join(f'{c}={fails[c]}' for c in CATEGORIES))
    return 1 if fails else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
