#!/usr/bin/env python3
"""Check that a wording rewrite of the Japanese Console catalogue kept its structure.

Compares console/src/lib/i18n/locales/ja/<domain>.ts with its content at a git ref and
fails when a rewrite changed anything but the wording of values. Local use only; it is
not wired into CI (the same stance as scripts/guide-diff-check.py).

    python3 scripts/catalog-diff-check.py <ref> [--list-citations | --rewrite-guide] [--lang en] [--allow-labels] [--allow-term KEY:OLD>NEW] [--list-pinned] [files...]

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

LABEL SYNC: shared old labels must move to the same new text in every domain, or
FAIL split. --allow-split KEY,KEY explicitly approves every divergent participant;
stale approvals fail. --list-citations lists exact label citations as TSV and exits 0.
--rewrite-guide changes exact bracket-quote, bold and menu segments (and the settings
reference's first-column cells) in the selected language's guide only. Headings,
prose, code, tests, knowledge and Go citations remain manual and fail until updated
or reviewed with --exempt-pin. Dirty guide targets require --force. Chained/swapped
labels require manual guide edits to preserve idempotence. See §10.6 for matching rules.

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


def catalogue_snapshot(ref, target):
    """Evaluate all domains in one Node process; shared labels cross domain boundaries."""
    names = [p for p in git('ls-tree', '-r', '--name-only', ref, '--', target).splitlines()
             if p.endswith('.ts')]
    with tempfile.TemporaryDirectory(dir=os.environ.get('AF_WORK_DIR') or None) as tmp:
        paths = []
        for i, name in enumerate(names):
            before = os.path.join(tmp, f'{i}.ts')
            with open(before, 'w', encoding='utf-8') as fh:
                fh.write(git('show', f'{ref}:{name}'))
            paths.append(before)
            paths.append(os.path.abspath(name) if os.path.exists(name) else before)
        program = ('const out=[]; for(const p of JSON.parse(process.argv[1])) '
                   '{const m=await import(p); out.push(Object.values(m)[0]);} '
                   'process.stdout.write(JSON.stringify(out));')
        p = subprocess.run(['node', '--experimental-strip-types', '--no-warnings',
                            '--input-type=module', '-e', program, json.dumps(paths)],
                           capture_output=True, text=True)
        if p.returncode:
            raise Fail(f'node could not evaluate catalogue: {p.stderr.strip()[:300]}')
        values = json.loads(p.stdout)
    old, new = {}, {}
    for i, name in enumerate(names):
        old.update({k: (name, v) for k, v in values[2 * i].items()})
        if os.path.exists(name):
            new.update(values[2 * i + 1])
    return old, new


def split_labels(changes, old, new, approvals):
    problems, used = {}, set()
    for key, _, o, n in changes:
        others = sorted(k for k, (_, v) in old.items()
                        if v == o and k != key and new.get(k) != n)
        if others:
            problems[key] = others
            used.update(k for k in [key, *others] if k in approvals)
    return problems, used


# The offsets are in the original line, so swaps and chains never cascade within a run.
KAGI_SPAN = re.compile(r'「([^」\n]*)」')
BOLD_SPAN = re.compile(r'\*\*([^*\n]*)\*\*')
HEADING = re.compile(r'^ {0,3}#{1,6}(?:\s|$)')
BARE_EDGE = r'\w'
QUOTED = re.compile(r'"([^"\n]*)"|\'([^\'\n]*)\'|`([^`\n]*)`|“([^”\n]*)”')
SYNC_SECTIONS = ('guide', 'console tests', 'af-usage.md', 'af-usage.coverage.tsv', 'Go sources')


def citation_files(lang):
    sections = {}
    for name in sorted(set(git('ls-files', '-co', '--exclude-standard', '-z').split('\0'))):
        if name.startswith('guide/') and name.endswith('.md'):
            if name.endswith('.ja.md') == (lang == 'ja'):
                sections[name] = 'guide'
        elif (name.startswith('console/') and '.test.' in name or
              name.startswith('console-e2e/') and name.endswith(CODE_SUFFIXES)):
            sections[name] = 'console tests'
        elif name in ('workspace/agent/knowledge/af-usage.md',
                      'workspace/agent/knowledge/af-usage.coverage.tsv'):
            sections[name] = name.rsplit('/', 1)[-1]
        elif name.endswith('.go'):
            sections[name] = 'Go sources'
    return sections


def line_citations(line, label, settings=False):
    """Exact spans win over menu segments, which win over bounded bare text."""
    spans = []
    for pattern, form in ((KAGI_SPAN, 'bracket-quote'), (BOLD_SPAN, 'bold')):
        for m in pattern.finditer(line):
            if m.group(1) == label:
                spans.append((m.start(1), m.end(1), form))
    if settings and line.lstrip().startswith('|'):
        cell = re.search(r'\|\s*([^|]*?)\s*\|', line)
        if cell and cell.group(1) == label:
            spans.append((cell.start(1), cell.end(1), 'table-cell'))
    # A menu segment must fill its delimiter-bounded cell. Wrappers are retained.
    for m in re.finditer(r'[^\n|、。,.!?;:()（）「」\[\]*`]+', line):
        chunk = m.group()
        if not re.search(r'\s>\s|\s*→\s*', chunk):
            continue
        for segment in re.finditer(r'(?:^|[>→])([^>→]+)', chunk):
            raw = segment.group(1)
            if raw.strip() == label:
                start = m.start() + segment.start(1) + len(raw) - len(raw.lstrip())
                if not any(a <= start < b for a, b, _ in spans):
                    spans.append((start, start + len(label), 'menu-segment'))
    for m in QUOTED.finditer(line):
        for group in range(1, 5):
            if m.group(group) == label and not any(a <= m.start(group) < b for a, b, _ in spans):
                spans.append((m.start(group), m.end(group), 'quoted'))
    for m in re.finditer(r'(?<![' + BARE_EDGE + '])' + re.escape(label) +
                         r'(?![' + BARE_EDGE + '])', line):
        if not any(a <= m.start() < b for a, b, _ in spans):
            # Do not treat an exact word inside a longer quoted/bold span as a label.
            if any(s.start() <= m.start() < s.end() for s in [*KAGI_SPAN.finditer(line), *BOLD_SPAN.finditer(line), *QUOTED.finditer(line)]):
                continue
            spans.append((m.start(), m.end(), 'bare-in-prose-exact-match'))
    return sorted(set(spans))


def markdown_content(line):
    """Remove container markers before interpreting headings or fences."""
    while True:
        stripped = re.sub(r'^ {0,3}(?:>[ \t]?|(?:[-+*]|\d+[.)])[ \t]+)', '', line)
        if stripped == line:
            return line
        line = stripped


def settings_table_rows(lines, lang):
    """Only Tab/target-language headers identify tables governed by docs-check."""
    rows = set()
    header = 'タブ' if lang == 'ja' else 'Tab'
    for i, line in enumerate(lines[:-1]):
        cells = markdown_content(line).strip().strip('|').split('|')
        if not line.lstrip().startswith('|') or cells[0].strip() != header:
            continue
        separator = lines[i + 1].strip().strip('|').split('|')
        if not separator or not all(re.fullmatch(r'\s*:?-{3,}:?\s*', c) for c in separator):
            continue
        j = i + 2
        while j < len(lines) and lines[j].lstrip().startswith('|'):
            rows.add(j)
            j += 1
    return rows


class MarkdownProtection:
    """Conservative source ranges: changing code or link metadata can break navigation."""

    def __init__(self, text):
        self.ranges = []
        lines = text.splitlines(keepends=True)
        starts, offset = [], 0
        fence = None
        front = bool(lines and lines[0].lstrip('\ufeff').strip() == '---')
        for i, line in enumerate(lines):
            starts.append(offset)
            content = markdown_content(line)
            if front:
                self.ranges.append((offset, offset + len(line), 'metadata'))
                if i and line.strip() in ('---', '...'):
                    front = False
            elif fence:
                self.ranges.append((offset, offset + len(line), 'code'))
                char, length = fence
                if re.fullmatch(r' {0,3}' + re.escape(char) + '{' + str(length) + r',}[ \t]*\r?\n?', content):
                    fence = None
            else:
                opener = re.match(r'^ {0,3}(`{3,}|~{3,})([^\r\n]*)', content)
                if opener and (opener[1][0] != '`' or '`' not in opener[2]):
                    fence = (opener[1][0], len(opener[1]))
                    self.ranges.append((offset, offset + len(line), 'code'))
                elif re.match(r'^(?: {4}| {0,3}\t)', content):
                    self.ranges.append((offset, offset + len(line), 'code'))
            offset += len(line)
        # Code spans may cross lines; a closing run must have exactly the opener's length.
        ticks = list(re.finditer(r'(?<!`)`+(?!`)', text))
        i = 0
        while i < len(ticks):
            opening = ticks[i]
            prefix = text[:opening.start()]
            escaped = (len(prefix) - len(prefix.rstrip('\\'))) % 2
            if escaped or self.at(opening.start(), opening.end()):
                i += 1
                continue
            closing = next((j for j in range(i + 1, len(ticks))
                            if len(ticks[j][0]) == len(opening[0])
                            and not self.at(ticks[j].start(), ticks[j].end())), None)
            if closing is None:
                i += 1
            else:
                self.ranges.append((opening.start(), ticks[closing].end(), 'code'))
                i = closing + 1
        for match in re.finditer(r'<!--.*?(?:-->|\Z)|<(?:/?[A-Za-z][\w:-]*\b|!|\?)(?:"[^"]*"|\'[^\']*\'|[^\'">])*>', text, re.S):
            self.ranges.append((match.start(), match.end(), 'metadata'))
        # Protect balanced destinations and optional titles, retaining visible link text.
        for match in re.finditer(r'\]\s*\(', text):
            if self.at(match.start(), match.end()):
                continue
            start, pos, depth = match.start() + 1, match.end(), 1
            quote, angle = None, False
            while pos < len(text) and depth:
                if text[pos] == '\\':
                    pos += 2
                    continue
                char = text[pos]
                if quote:
                    if char == quote:
                        quote = None
                elif char in '"\'':
                    quote = char
                elif char == '<':
                    angle = True
                elif char == '>':
                    angle = False
                elif not angle and char == '(':
                    depth += 1
                elif not angle and char == ')':
                    depth -= 1
                pos += 1
            self.ranges.append((start, pos, 'metadata'))
        for match in re.finditer(r'^ {0,3}\[[^\]\n]+\]:[^\n]*(?:\n[ \t]+[^\n]+)*', text, re.M):
            self.ranges.append((match.start(), match.end(), 'metadata'))
        for match in re.finditer(r'(?:https?://|mailto:)[^\s<>]+', text):
            self.ranges.append((match.start(), match.end(), 'metadata'))
        self.starts = starts

    def at(self, start, end):
        forms = {form for a, b, form in self.ranges if start < b and end > a}
        return 'code' if 'code' in forms else 'metadata' if forms else None

    def form(self, line, start, end):
        offset = self.starts[line]
        return self.at(offset + start, offset + end)


def citations(changes, lang):
    hits, texts = [], {}
    if any(not o for _, _, o, _ in changes):
        changes = [c for c in changes if c[2]]
    for path, section in citation_files(lang).items():
        try:
            with open(path, encoding='utf-8', newline='') as fh:
                text = fh.read()
        except (OSError, UnicodeDecodeError):
            continue
        texts[path] = text
        lines = text.splitlines(keepends=True)
        protection = MarkdownProtection(text) if section == 'guide' else None
        tab_rows = settings_table_rows(lines, lang) if path == (
            'guide/ref/settings' + ('.ja' if lang == 'ja' else '') + '.md') else set()
        for i, line in enumerate(lines):
            content = markdown_content(line)
            heading = bool(HEADING.match(content) or
                           i + 1 < len(lines) and re.fullmatch(r'\s*(?:=+|-+)\s*', markdown_content(lines[i + 1]))
                           and content.strip())
            for key, _, o, n in changes:
                if o not in line:
                    continue
                matches = line_citations(line, o, i in tab_rows and key.startswith(('set.tab_', 'tenant.tab_')))
                if section in ('console tests', 'Go sources'):
                    matches = [m for m in matches if m[2] in ('quoted', 'bracket-quote', 'bold')]
                if section == 'af-usage.coverage.tsv':
                    matches = [m for m in matches if m[2] != 'bare-in-prose-exact-match']
                    offset = 0
                    for cell in line.rstrip('\r\n').split('\t'):
                        if cell == o:
                            matches.append((offset, offset + len(o), 'tsv-cell'))
                        offset += len(cell) + 1
                for a, b, form in matches:
                    protected = protection.form(i, a, b) if protection else None
                    if protected:
                        form = protected
                    elif heading:
                        form = 'heading'
                    hits.append((section, path, i + 1, form, o, n, key, a, b))
    return hits, texts


def print_citations(hits):
    esc = lambda s: s.replace('\\', '\\\\').replace('\t', '\\t').replace('\n', '\\n').replace('\r', '\\r')
    for section in SYNC_SECTIONS:
        print(f'# {section}')
        for sec, path, line, form, o, n, key, _, _ in hits:
            if sec == section:
                print('\t'.join([f'{path}:{line}', form, esc(o), esc(n), key]))


def rewrite_guide(hits, texts, blocked, force, changes):
    edits = collections.defaultdict(lambda: collections.defaultdict(dict))
    mapping = {o: n for _, _, o, n in changes}
    if any(n in mapping for n in mapping.values()):
        raise Fail('chained or swapped labels cannot be rewritten idempotently; update guide manually')
    for section, path, line, form, o, n, key, a, b in hits:
        if section != 'guide' or form not in ('bracket-quote', 'bold', 'menu-segment', 'table-cell') or key in blocked:
            continue
        real = os.path.realpath(path)
        if not real.startswith(os.path.abspath('guide') + os.sep):
            raise Fail(f'refusing guide path outside guide/: {path}')
        if real != os.path.abspath(path):
            raise Fail(f'refusing symlink guide path: {path}')
        if any(a < end and b > start and (a, b) != (start, end)
               for start, end in edits[path][line]):
            raise Fail(f'overlapping label rewrites at {path}:{line}; update manually')
        existing = edits[path][line].get((a, b))
        if existing and existing != (o, n):
            raise Fail(f'ambiguous label rewrite at {path}:{line}')
        edits[path][line][a, b] = (o, n)
    # Preflight every target before the first write; a refusal leaves the guide intact.
    for path in edits:
        if not force and git('status', '--porcelain', '--', path).strip():
            raise Fail(f'dirty guide file: {path} (review it, then use --force)')
    for path, by_line in edits.items():
        lines = texts[path].splitlines(keepends=True)
        for line, spans in by_line.items():
            for (a, b), (o, n) in sorted(spans.items(), reverse=True):
                lines[line - 1] = lines[line - 1][:a] + n + lines[line - 1][b:]
                print(f'{path}:{line} {o} -> {n}')
        with open(path, 'w', encoding='utf-8', newline='') as fh:
            fh.write(''.join(lines))


def main(argv):
    ap = argparse.ArgumentParser(prog='catalog-diff-check.py', usage='%(prog)s <ref> [--lang en] [--allow-labels] [--allow-term KEY:OLD>NEW] [--list-pinned | --list-citations | --rewrite-guide | --triples] [files...]')
    ap.add_argument('ref')
    ap.add_argument('files', nargs='*')
    ap.add_argument('--lang', choices=['ja', 'en'], default='ja',
                    help='catalogue to check (default ja)')
    ap.add_argument('--triples', action='store_true',
                    help='en only: print key, ja, en old, en new for every changed value, exit 0')
    ap.add_argument('--allow-labels', action='store_true')
    ap.add_argument('--list-pinned', action='store_true')
    ap.add_argument('--list-citations', action='store_true', help='list exact label citations as TSV, exit 0')
    ap.add_argument('--rewrite-guide', action='store_true', help='rewrite exact guide spans and list manual citations')
    ap.add_argument('--force', action='store_true', help='allow rewriting reviewed dirty guide files')
    ap.add_argument('--allow-split', action='append', default=[], metavar='KEY,KEY',
                    help='approve listed keys participating in a reviewed label split')
    ap.add_argument('--exempt-pin', action='append', default=[], metavar='KEY@PATH[:LINE]',
                    help='accept one reviewed PINNED hit that is not a citation of the value')
    ap.add_argument('--allow-term', action='append', default=[], metavar='KEY:OLD>NEW[*N]',
                    help='approve one reviewed term change in one key: OLD count -N, NEW count +N')
    ap.add_argument('--allow-terms-file', action='append', default=[], metavar='PATH',
                    help='the same, one KEY<TAB>OLD<TAB>NEW[<TAB>N] per line')
    args = ap.parse_args(argv)
    if (args.list_citations or args.rewrite_guide) and sum((args.list_pinned, args.list_citations, args.triples, args.rewrite_guide)) > 1:
        ap.error('listing and rewrite modes are mutually exclusive')
    if args.force and not args.rewrite_guide:
        ap.error('--force needs --rewrite-guide')
    split_approvals = set()
    for spec in args.allow_split:
        keys = spec.split(',')
        if any(not re.fullmatch(r'[\w.-]+', k) for k in keys):
            ap.error('--allow-split needs KEY,KEY (no empty keys or spaces)')
        split_approvals.update(keys)
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
    listing = args.list_pinned or args.triples or args.list_citations

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
        sync_changes = []
        split_problems = {}
        split_used = set()
        if args.list_citations or args.rewrite_guide:
            base_files = set(git('ls-tree', '-r', '--name-only', args.ref, '--', target).splitlines())
            for path in files:
                if path not in base_files or not os.path.isfile(path):
                    raise Fail(f'{path}: cannot compare old and new catalogue values '
                               '(added, deleted or moved file); label sync requires existing files')
        if files and not args.triples:
            old_catalogue, new_catalogue = catalogue_snapshot(args.ref, target)
            lab = label_like_en if en else label_like
            sync_changes = [(k, p, o, new_catalogue[k]) for k, (p, o) in old_catalogue.items()
                            if p in files and k in new_catalogue and o != new_catalogue[k]
                            and (lab(o) or lab(new_catalogue[k]))]
            split_problems, split_used = split_labels(sync_changes, old_catalogue, new_catalogue, split_approvals)
        for key, others in split_problems.items():
            missing = sorted(set([key, *others]) - split_approvals)
            if missing:
                fail('split', f'{key}: old label shared with keys not changed to the same new text: '
                     + ', '.join(others) + '; unapproved: ' + ', '.join(missing))
            elif not listing:
                print(f'ALLOWED split: {key}: ' + ', '.join(others))
        for key in sorted(split_approvals - split_used):
            fail('split', f'--allow-split {key} matched no split (stale approval)')
        if args.list_citations or args.rewrite_guide:
            print(f'label sync: {len(files)} compared file(s), {len(sync_changes)} changed label(s)'
                  + ('; no catalogue diff against ref' if not changed else
                     '; no changed labels in compared values' if not sync_changes else ''), file=sys.stderr)
            for key, _, _, _ in sync_changes:
                if key.startswith(('set.tab_', 'tenant.tab_')):
                    print(f'WARN SETTINGS TAB: {key}: update the first-column cell in guide/ref/settings'
                          + ('.md' if en else '.ja.md') + '; scripts/docs-check.py requires exact labels and '
                          'Japanese personal-tab headings (manual; update anchors/links too)', file=sys.stderr)
            sync_hits, sync_texts = citations(sync_changes, args.lang)
            if args.list_citations:
                print_citations(sync_hits)
                return 0
            # Even an approved split is unsafe for automatic citation rewriting.
            rewritable_hits = [h for h in sync_hits if not any(
                e[0] == h[6] and e[1] == h[1] and e[2] in ('', str(h[2])) for e in exempt)]
            rewrite_guide(rewritable_hits, sync_texts, set(split_problems), args.force, sync_changes)
            sync_hits, _ = citations(sync_changes, args.lang)
            remaining = []
            for hit in sync_hits:
                _, name, line, form, _, _, key, _, _ = hit
                approval = next((e for e in exempt if e[0] == key and e[1] == name
                                 and e[2] in ('', str(line))), None)
                if approval:
                    used.add(approval[3])
                    exempted += 1
                    print(f'EXEMPT: {key}: reviewed, not a citation: {name}:{line}')
                else:
                    remaining.append(hit)
                    fail('pinned', f'{key}: remaining {form} citation at {name}:{line}')
            print('remaining manual citations:')
            print_citations(remaining)
        sources = Sources(args.lang)
    except (Fail, OSError) as e:
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
                    # exactly the stated amount. Amounts sum per (category, counted unit), where
                    # the unit is what that category counts: en glossary terms are
                    # case-insensitive (Default and default are one unit), caps stay exact.
                    def units(item):
                        out = []
                        for d in item_domains(en, item, terms) or [None]:
                            out.append((d, item.lower() if en and d == 'glossary' else item))
                        return out
                    want = collections.defaultdict(int)
                    for _, (_, old_t, new_t, cnt, _) in mine:
                        for t, sign in ((old_t, -1), (new_t, 1)):
                            for u in units(t):
                                want[u] += sign * cnt
                    seen = {}
                    bad = set()
                    for (d, k), w in want.items():
                        seen[(d, k)] = (item_count(en, d, k, o, terms), item_count(en, d, k, n, terms))
                        if seen[(d, k)][1] - seen[(d, k)][0] != w:
                            bad.add((d, k))
                    for ai, a in mine:
                        spec = ai
                        us = units(a[1]) + units(a[2])
                        if any(u in bad for u in us):
                            allow_why[spec].append(f'{where}: ' + ', '.join(
                                f'{u[1]} [{u[0] or "direct"}] {seen[u][0]} -> {seen[u][1]} (allowed {want[u]:+d})'
                                for u in us if u in bad))
                            continue
                        allow_applied.setdefault(spec, []).append(where)
                        for item, sign in ((a[1], -1), (a[2], 1)):
                            for d, k in units(item):
                                if d:
                                    k = next(t for t in terms if t.lower() == item.lower()) if en and d == 'glossary' else k
                                    adj[d][k] += sign * a[3]
                        if not listing:
                            def shown(item):
                                us = units(item)
                                return ', '.join(f'{item}{f" [{d or "direct"}]" if len(us) > 1 else ""} {seen[(d, k)][0]} -> {seen[(d, k)][1]}'
                                                 for d, k in us)
                            print(f'ALLOWED term: {where}: {a[1]} -> {a[2]} x{a[3]} ({shown(a[1])}, {shown(a[2])})')
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
                for frag in ([] if args.rewrite_guide and lab(o) else (clauses_en if en else clauses)(o, n)):
                    found.update((h, frag) for h in sources.find(frag))
                if lab(o) and not args.rewrite_guide:
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
    if fails['split'] or args.allow_split or args.rewrite_guide:
        cats = [*cats, 'split']
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
