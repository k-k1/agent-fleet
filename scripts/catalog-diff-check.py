#!/usr/bin/env python3
"""Check that a wording rewrite of the Japanese Console catalogue kept its structure.

Compares console/src/lib/i18n/locales/ja/<domain>.ts with its content at a git ref and
fails when a rewrite changed anything but the wording of values. Local use only; it is
not wired into CI (the same stance as scripts/guide-diff-check.py).

    python3 scripts/catalog-diff-check.py <ref> [--lang en] [--allow-labels] [--allow-term KEY:OLD>NEW] [--list-pinned] [files...]

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
Term changes: a rewrite that applies a terminology decision moves glossary (and, in ja, Latin
word) counts on purpose. Approve it per key with `--allow-term KEY:OLD>NEW[*N]` (repeatable) or
`--allow-terms-file PATH` (lines KEY<TAB>OLD<TAB>NEW[<TAB>N]): in that key's changed value OLD
must fall by exactly N and NEW rise by exactly N (default 1), else the allowance is an error
[allow] and the drift still fails; the same term moving in another key still fails. Applied
allowances are printed as ALLOWED. Nothing else is relaxed. See docs/build/10-development.md §10.6.
Limitation: a guide quote that reproduces only the start of a sentence (under 8 characters,
or cut before the part the rewrite changed) is invisible here, because there is no old
fragment to match. After a rewrite, also search the guide for the first words of each
changed value that is cited by sentence.

--lang en  checks the English catalogue (locales/en/*.ts) instead. ja is the canonical source
and en is derived from it, so an en rewrite must keep meaning parity with ja; the script holds
the structure and the facts, a reviewer judges the meaning (see --triples). Differences:

  target     en/*.ts may change; anything else under locales/ (ja/ included) fails [outside]
  invariants placeholders, slots, digits, code, newlines, edge as in ja, plus (instead of
             latin) caps (ALL_CAPS words), idents (paths, env vars, snake_case, dotted names,
             --flags, camelCase, CLI/product names), quoted ("..." contents), marks (-> and
             the warning sign)
  glossary   the Screen column of guide/ref/glossary.md, matched case-insensitively on word
             boundaries (a plural s is the same word), counted as multisets
  labels     at most 30 characters and no sentence-ending punctuation
  PINNED     clauses split at sentence ends, {x} and line breaks, at least 20 characters;
             searched in guide/**/*.md except *.ja.md and README*.md, console tests, Go
             sources (not af-usage.md); a label also as "label", **label**, 'label', `label`
  warnings   per changed value, a change in the count of a restriction word (only, never,
             must, not, cannot, default, required, unless, except) is printed as WARN and
             counted on a "warnings:" line; it never fails the run
  --triples  prints key, current ja, en old, en new for every changed en value (TSV) and
             exits 0, for the reviewer who judges meaning parity. Read-only.

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
PIN_GLOBS_EN = ['guide/*.md', 'console/*.test.*', 'console-e2e/*.ts', 'control-plane/*.go',
                'workspace/*.go', 'e2e/*.go', 'deploy/*.go']
PIN_GLOBS = ['guide/*.ja.md', 'console/*.test.*', 'console-e2e/*.ts', 'control-plane/*.go',
             'workspace/*.go', 'e2e/*.go', 'deploy/*.go', 'workspace/agent/knowledge/af-usage.md']
CODE_SUFFIXES = ('.go', '.ts', '.tsx', '.js', '.jsx', '.mjs')

EN_DIR = CATALOGUE + '/en/'
GLOSSARY_EN = 'guide/ref/glossary.md'
LABEL_MAX_EN = 30
FRAGMENT_MIN_EN = 20
SPLIT_CLAUSE_EN = re.compile(r'[.!?]\s+|\{\w+\}|</?\d+/?>|\n')
# CLI and product names that must survive a rewrite verbatim (case included).
PRODUCT_NAMES = ['claude', 'codex', 'agy', 'opencode', 'kiro', 'copilot', 'rovo', 'muse',
                 'cursor', 'tmux', 'git', 'gh', 'npm', 'ssh', 'aws', 'gcloud', 'kubectl', 'docker',
                 'github', 'gitlab', 'bitbucket', 'jira', 'svn', 'ssm', 'ecs', 'lcpp', 'ollama']
NOT_PATHS = {'and/or', 'either/or', 'his/her', 'he/she', 'w/o', 'n/a'}
RESTRICTION_WORDS = ['only', 'never', 'must', 'not', 'cannot', 'default', 'required',
                     'unless', 'except']

_PATH = re.compile(r'(?<![\w<])(?:/[\w.~@-]+)+|[\w.~@-]+(?:/[\w.~@-]+)+')
_IDENT = re.compile('|'.join([
    r'\b[A-Za-z0-9]+(?:_[A-Za-z0-9]+)+\b',            # snake_case, AF_MASTER_KEY
    r'\b\w+(?:\.\w{2,})+\b',                          # config.json, 127.0.0.1
    r'(?<!\w)--?[A-Za-z][\w-]*',                      # --flag
    r'\b[a-z]+[A-Z]\w*|\b[A-Z][a-z]+[A-Z]\w*',        # camelCase, ComfyUI
]))
# Case-insensitive to find them, kept verbatim so Codex and codex are different items.
_PRODUCT = re.compile(r'\b(?:%s)\b' % '|'.join(PRODUCT_NAMES), re.I)


def en_idents(v):
    return ([m for m in _PATH.findall(v) if m not in NOT_PATHS] + _IDENT.findall(v)
            + _PRODUCT.findall(v))


def _rx(pattern):
    return re.compile(pattern).findall


INVARIANTS_EN = {
    'placeholders': INVARIANTS['placeholders'].findall,
    'slots': INVARIANTS['slots'].findall,
    'digits': INVARIANTS['digits'].findall,
    'code': INVARIANTS['code'].findall,
    'caps': _rx(r'\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*\b(?<=[A-Z0-9_]{2})'),
    'idents': en_idents,
    'quoted': lambda v: [a or b for a, b in re.findall(r'"([^"]*)"|“([^”]*)”', v)],
    'marks': _rx('→|⚠'),
}
CATEGORIES_EN = ['skeleton', 'keys', 'outside', *INVARIANTS_EN, 'newlines', 'edge', 'glossary',
                 'label', 'pinned']


def en_glossary_terms(text):
    """Screen-column terms of glossary.md as case-insensitive word-boundary patterns."""
    terms = {}
    for line in text.split('\n'):
        if not line.startswith('|'):
            continue
        cell = line.strip('|').split('|')[0].replace('`', '').strip()
        if not cell or set(cell) <= set('-: ') or cell == 'Screen':
            continue
        base = re.split(r'\s*\(', cell)[0].strip()
        if len(base) >= 2 and '/' not in base:
            terms.setdefault(base.lower(), base)
    return {t: re.compile(r'\b%s(?:es|s)?\b' % re.escape(t), re.I) for t in sorted(terms.values(), key=str.lower)}


def label_like_en(v):
    return len(v) <= LABEL_MAX_EN and not re.search(r'[.!?](\s|$)', v)


def clauses_en(old, new):
    out = []
    for s in SPLIT_CLAUSE_EN.split(old):
        s = s.strip().rstrip('.!?').strip()
        if len(s) >= FRAGMENT_MIN_EN and s not in new:
            out.append(s)
    return out


def restriction_counts(v):
    v = v.replace('’', "'")
    v = re.sub(r"\bcan't\b", 'cannot', v, flags=re.I)
    v = re.sub(r"\b(?:\w+)n't\b", 'not', v, flags=re.I)
    return collections.Counter(w for w in re.findall(r'[a-z]+', v.lower()) if w in RESTRICTION_WORDS)


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


ALLOW_SPEC = re.compile(r'^([^:>\s]+):([^>]+)>(.+?)(?:\*(\d+))?$')
CAPS_ITEM = re.compile(r'[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*')


def parse_allowance(spec):
    """KEY:OLD>NEW[*N] -> (key, old, new, n, spec)."""
    m = ALLOW_SPEC.match(spec)
    if not m:
        raise ValueError(spec)
    key, old, new, n = m.groups()
    return make_allowance(key, old, new, int(n) if n else 1)


def make_allowance(key, old, new, n):
    if not (key and old and new) or old == new or n < 1:
        raise ValueError(f'{key}:{old}>{new}*{n}')
    return key, old, new, n, f'{key}:{old}>{new}' + (f'*{n}' if n != 1 else '')


def read_allowance_file(path):
    """Lines `KEY<TAB>OLD<TAB>NEW[<TAB>N]`; blank lines and `#` comments are skipped."""
    out = []
    with open(path, encoding='utf-8') as fh:
        for no, line in enumerate(fh, 1):
            line = line.rstrip('\n')
            if not line.strip() or line.lstrip().startswith('#'):
                continue
            f = line.split('\t')
            if len(f) not in (3, 4) or not all(f) or (len(f) == 4 and not f[3].isdigit()):
                raise ValueError(f'{path}:{no}: want KEY<TAB>OLD<TAB>NEW[<TAB>N]')
            out.append(make_allowance(f[0], f[1], f[2], int(f[3]) if len(f) == 4 else 1))
    return out


def item_domains(en, item, terms):
    """The invariant categories that count `item`: what an allowance for it must also explain."""
    doms = []
    if en:
        if item.lower() in {t.lower() for t in terms}:
            doms.append('glossary')
        if len(item) >= 2 and CAPS_ITEM.fullmatch(item):
            doms.append('caps')
    else:
        if item in terms:
            doms.append('glossary')
        if re.fullmatch(r'[A-Za-z]+', item):
            doms.append('latin')
    return doms


def item_count(en, dom, item, text, terms):
    """How often `item` is counted in `text` by category `dom` (None: no category counts it)."""
    if dom == 'glossary':
        if en:
            rx = next(r for t, r in terms.items() if t.lower() == item.lower())
            return len(rx.findall(text))
        return text.count(item)
    if dom == 'latin':
        return collections.Counter(INVARIANTS['latin'].findall(text))[item]
    if dom == 'caps':
        return collections.Counter(INVARIANTS_EN['caps'](text))[item]
    if en:
        return len(re.findall(r'(?<!\w)%s(?!\w)' % re.escape(item), text))
    return text.count(item)


def label_like(v):
    return len(v) <= LABEL_MAX and '。' not in v


def clauses(old, new):
    return [s for s in SPLIT_CLAUSE.split(old) if len(s) >= FRAGMENT_MIN and s not in new]


class Sources:
    """Every file a quote of a catalogue value could live in, with hard wraps folded away so
    a sentence wrapped across lines (even indented) still matches."""

    def __init__(self, lang='ja'):
        self.lang = lang
        self.files = {}
        names = git('ls-files', '-co', '--exclude-standard', '-z').split('\0')
        for name in names:
            if not name or name.startswith(CATALOGUE + '/'):
                continue
            if lang == 'en':
                base = name.rsplit('/', 1)[-1]
                if name.endswith('.ja.md') or base.startswith('README') or \
                        not any(fnmatch.fnmatch(name, g) for g in PIN_GLOBS_EN):
                    continue
            elif not any(fnmatch.fnmatch(name, g) for g in PIN_GLOBS):
                continue
            try:
                with open(name, encoding='utf-8') as fh:
                    text = fh.read()
            except (OSError, UnicodeDecodeError):
                continue
            # Fold hard wraps: a continuation line loses its indentation, and a blank
            # line is a barrier so text from two paragraphs never joins into a quote.
            # English words are separated by the line break that wrapped them; Japanese has none.
            sep = ' ' if lang == 'en' else ''
            starts, flat, pos = [], [], 0
            for ln in text.split('\n'):
                ln = ln.strip() and ln.lstrip(' \t') or '\0'
                starts.append(pos)
                flat.append(ln + sep)
                pos += len(ln) + len(sep)
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
            if self.lang == 'en':
                forms = ['**%s**' % label, '"%s"' % label, '“%s”' % label, "'%s'" % label,
                         '`%s`' % label]
            elif name.endswith(CODE_SUFFIXES):
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
    ap = argparse.ArgumentParser(prog='catalog-diff-check.py', usage='%(prog)s <ref> [--lang en] [--allow-labels] [--allow-term KEY:OLD>NEW] [--list-pinned] [--triples] [files...]')
    ap.add_argument('ref')
    ap.add_argument('files', nargs='*')
    ap.add_argument('--lang', choices=['ja', 'en'], default='ja',
                    help='catalogue to check (default ja)')
    ap.add_argument('--triples', action='store_true',
                    help='en only: print key, ja, en old, en new for every changed value, exit 0')
    ap.add_argument('--allow-labels', action='store_true')
    ap.add_argument('--list-pinned', action='store_true')
    ap.add_argument('--exempt-pin', action='append', default=[], metavar='KEY@PATH[:LINE]',
                    help='accept one reviewed PINNED hit that is not a citation of the value')
    ap.add_argument('--allow-term', action='append', default=[], metavar='KEY:OLD>NEW[*N]',
                    help='approve one reviewed term change in one key: OLD count -N, NEW count +N')
    ap.add_argument('--allow-terms-file', action='append', default=[], metavar='PATH',
                    help='the same, one KEY<TAB>OLD<TAB>NEW[<TAB>N] per line')
    args = ap.parse_args(argv)
    en = args.lang == 'en'
    if args.triples and not en:
        print('error: --triples needs --lang en', file=sys.stderr)
        return 2
    target = EN_DIR if en else JA_DIR
    cats = CATEGORIES_EN if en else CATEGORIES

    exempt = []
    for spec in args.exempt_pin:
        key, sep, loc = spec.partition('@')
        path, _, line = loc.partition(':')
        if not (sep and key and path) or (line and not line.isdigit()):
            print(f'error: bad --exempt-pin {spec!r} (want KEY@PATH[:LINE])', file=sys.stderr)
            return 2
        exempt.append((key, path, line, spec))
    allowances = []
    try:
        for spec in args.allow_term:
            allowances.append(parse_allowance(spec))
        for path in args.allow_terms_file:
            allowances += read_allowance_file(path)
    except (ValueError, OSError) as e:
        print(f'error: bad term allowance {e} (want KEY:OLD>NEW[*N], OLD != NEW)', file=sys.stderr)
        return 2
    used = set()
    exempted = 0
    fails = collections.Counter()
    listing = args.list_pinned or args.triples

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
        changed = [f for f in git('diff', '--name-only', '-z', args.ref, '--', target).split('\0') if f]
        changed += [f for f in git('ls-files', '-o', '--exclude-standard', '-z', '--', target).split('\0') if f]
        files = args.files or sorted(set(f for f in changed if f.endswith('.ts')))
        for f in files:
            if not (f.startswith(target) and f.endswith('.ts')):
                raise Fail(f'{f}: not a {target}*.ts file')
        with open(GLOSSARY_EN if en else GLOSSARY, encoding='utf-8') as fh:
            terms = en_glossary_terms(fh.read()) if en else glossary_terms(fh.read())
        sources = Sources(args.lang)
    except Fail as e:
        print(f'error: {e}', file=sys.stderr)
        return 2

    # An item that is OLD in one allowance of a key and NEW in another would let the two
    # cancel (A>B with B>A, or a chain), so neither would have to hold on its own.
    for key in {a[0] for a in allowances}:
        fold = lambda t: t.lower() if en and 'glossary' in item_domains(en, t, terms) else t
        olds = {fold(a[1]) for a in allowances if a[0] == key}
        news = {fold(a[2]) for a in allowances if a[0] == key}
        if olds & news:
            print(f'error: {key}: {sorted(olds & news)[0]} is both an OLD and a NEW term of its allowances '
                  '(reversed or chained; state the net change as one allowance)', file=sys.stderr)
            return 2
    allow_applied = {}
    allow_why = collections.defaultdict(list)

    # Anything under locales/ outside the target dir must not move in a rewrite.
    outside = set(git('diff', '--name-only', '-z', args.ref, '--', CATALOGUE).split('\0'))
    outside |= set(git('ls-files', '-o', '--exclude-standard', '-z', '--', CATALOGUE).split('\0'))
    for f in sorted(x for x in outside if x and not x.startswith(target)):
        fail('outside', f'{f} changed (only {"en" if en else "ja"}/*.ts may change)')

    checked = changed_n = 0
    labels = []
    pins = []
    warns = []
    triples = []
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
                if args.triples:
                    triples.append((path, key, o, n))
                    continue
                adj = collections.defaultdict(lambda: collections.defaultdict(int))
                mine = [(i, a) for i, a in enumerate(allowances) if a[0] == key]
                if mine:
                    # An allowance holds only if every category counting OLD and NEW moved by
                    # exactly the stated amount; items shared by several allowances sum up.
                    # en glossary terms are counted case-insensitively: Default and default are one unit.
                    canon = lambda t: t.lower() if en and item_domains(en, t, terms) == ['glossary'] else t
                    want = collections.defaultdict(int)
                    for _, (_, old_t, new_t, cnt, _) in mine:
                        want[canon(old_t)] -= cnt
                        want[canon(new_t)] += cnt
                    bad = set()
                    seen = {}
                    for item, w in want.items():
                        doms = item_domains(en, item, terms) or [None]
                        seen[item] = [(d, item_count(en, d, item, o, terms), item_count(en, d, item, n, terms)) for d in doms]
                        if any(nc - oc != w for _, oc, nc in seen[item]):
                            bad.add(item)
                    for ai, a in mine:
                        spec = ai
                        if canon(a[1]) in bad or canon(a[2]) in bad:
                            allow_why[spec].append(f'{where}: ' + ', '.join(
                                f'{i} {seen[i][0][1]} -> {seen[i][0][2]} (allowed {want[i]:+d})' for i in (canon(a[1]), canon(a[2]))))
                            continue
                        allow_applied.setdefault(spec, []).append(where)
                        for item, sign in ((canon(a[1]), -1), (canon(a[2]), 1)):
                            for d, _, _ in seen[item]:
                                if d:
                                    k = next(t for t in terms if t.lower() == item.lower()) if en and d == 'glossary' else item
                                    adj[d][k] += sign * a[3]
                        if not listing:
                            print(f'ALLOWED term: {where}: {a[1]} -> {a[2]} x{a[3]} '
                                  f'({a[1]} {seen[canon(a[1])][0][1]} -> {seen[canon(a[1])][0][2]}, {a[2]} {seen[canon(a[2])][0][1]} -> {seen[canon(a[2])][0][2]})')
                for name, extract in (INVARIANTS_EN if en else {k: v.findall for k, v in INVARIANTS.items()}).items():
                    a, b = collections.Counter(extract(o)), collections.Counter(extract(n))
                    if name in adj:
                        d = {k: b[k] - a[k] for k in set(a) | set(b)}
                        for k, v in adj[name].items():
                            d[k] = d.get(k, 0) - v
                        d = {k: v for k, v in d.items() if v}
                        if d:
                            fail(name, f'{where}: removed={[k for k, v in d.items() if v < 0 for _ in range(-v)][:6]} added={[k for k, v in d.items() if v > 0 for _ in range(v)][:6]} (after allowances)')
                    elif a != b:
                        fail(name, f'{where}: removed={list((a - b).elements())[:6]} added={list((b - a).elements())[:6]}')
                if o.count('\n') != n.count('\n'):
                    fail('newlines', f'{where}: {o.count(chr(10))} -> {n.count(chr(10))}')
                if o[:len(o) - len(o.lstrip())] != n[:len(n) - len(n.lstrip())] or \
                        o[len(o.rstrip()):] != n[len(n.rstrip()):]:
                    fail('edge', f'{where}: leading/trailing whitespace changed')
                if en:
                    for t, rx in terms.items():
                        oc, nc = len(rx.findall(o)), len(rx.findall(n))
                        if oc + adj['glossary'].get(t, 0) != nc:
                            fail('glossary', f'{where}: "{t}" {oc} -> {nc}')
                    ro, rn = restriction_counts(o), restriction_counts(n)
                    if ro != rn:
                        diff = ', '.join(f'{w} {ro[w]} -> {rn[w]}' for w in RESTRICTION_WORDS if ro[w] != rn[w])
                        warns.append(where)
                        if not listing:
                            print(f'WARN restriction: {where}: {diff}')
                else:
                    for t in terms:
                        if o.count(t) + adj['glossary'].get(t, 0) != n.count(t):
                            fail('glossary', f'{where}: 「{t}」 {o.count(t)} -> {n.count(t)}')
                lab = label_like_en if en else label_like
                if lab(o) or lab(n):
                    labels.append((where, o, n))
                    if not args.allow_labels:
                        fail('label', f'{where}: label {o!r} -> {n!r} (pass --allow-labels to reword labels)')
                found = set()
                for frag in (clauses_en if en else clauses)(o, n):
                    found.update((h, frag) for h in sources.find(frag))
                if lab(o):
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

    if args.triples:
        # The ja value is read from the working tree: ja is canonical and untouched by an en rewrite.
        ja_cache = {}
        print('key\tja\ten old\ten new')
        for path, key, o, n in triples:
            ja_path = JA_DIR + path[len(EN_DIR):]
            if ja_path not in ja_cache:
                try:
                    ja_cache[ja_path] = evaluate(ja_path) if os.path.exists(ja_path) else {}
                except Fail as e:
                    print(f'error: {ja_path}: {e}', file=sys.stderr)
                    return 2
            esc = lambda v: v.replace('\\', '\\\\').replace('\t', '\\t').replace('\n', '\\n')
            print('\t'.join([key, esc(ja_cache[ja_path].get(key, '')), esc(o), esc(n)]))
        print(f'{len(triples)} changed value(s)', file=sys.stderr)
        return 0
    if listing:
        for key, loc, text in pins:
            print(f'{loc}\t{key}\t{text}')
        print(f'{len(pins)} pinned location(s)', file=sys.stderr)
        return 0
    if labels and args.allow_labels:
        print('changed labels:')
        for where, o, n in labels:
            print(f'  LABEL {where}: {o} -> {n}')
    for ai, (_, _, _, _, spec) in enumerate(allowances):
        if ai not in allow_applied:
            why = '; '.join(allow_why[ai]) or 'the key was not among the changed values'
            fail('allow', f'--allow-term {spec} matched no change (stale, wrong direction or wrong count): {why}')
    for _, _, _, spec in exempt:
        if spec not in used:
            print(f'WARN: --exempt-pin {spec} matched nothing (stale exemption)')
    print(f'{len(files)} file(s), {checked} value(s) checked, {changed_n} changed, {exempted} pin(s) exempted')
    if allowances:
        print(f'{len(allow_applied)} of {len(allowances)} term allowance(s) applied')
        cats = [*cats, 'allow']
    print('failures: ' + ', '.join(f'{c}={fails[c]}' for c in cats))
    if en:
        print(f'warnings: restriction={len(warns)}')
    return 1 if fails else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
