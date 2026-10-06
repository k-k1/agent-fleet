#!/usr/bin/env python3
"""Deterministic, local-only notation normalizer for phase B1 of the Japanese i18n revision.

B1 unifies *notation* inside console/src/lib/i18n/locales/ja/<domain>.ts values.
It never chooses terms (B2: sign-in/login, deploy/haibi,Mobile terminal, frame,
fold) and never rewrites the catalogue by itself: the default is --dry-run, and
--apply touches one domain file only after a human reviewed the report.

Rules (all judgement lives here; every ambiguous case is SKIPPED and REPORTED):

  R1 (term decision 21) spacing: insert one half-width space at a Latin/Japanese
      or digit/Japanese boundary that has none ("Gitホスティング" -> "Git ホス
      ティング", "{n}人" -> "{n} 人", "30日" -> "30 日"). Never insert next to
      Japanese punctuation/brackets, at string ends, inside `code` spans,
      {placeholders}, <n>...</n> slot tags, URLs, quoted Latin
      identifiers/paths/env vars, ranges ("1〜10"), times ("12:30"), version
      numbers, multipliers ("3x", "×1.25"), or in single-token labels of 3
      characters or less. Existing spaces are never removed. Anything unsure is
      skipped with a reason. Changes inside 「...」 are always skipped: the
      catalog-diff-check kagi rule has no allowance mechanism, so such an edit
      could never pass the follow-up guard.
  R2 (term decision 29) kana: 既に -> すでに; 無い -> ない; 無く -> なく. The 無い/
      無く forms change ONLY as adjective/auxiliary: never inside 無料/無効/無制限/
      無視/無理/無事/無限/無駄/無数 (matched literally, so these can never fire),
      never the noun 無し, never the verbs 無くす/無くなる (skipped, no whitelist:
      the one real occurrence, err.branch_not_in_head "行き場が無くなる", was
      inspected and stays untouched). Past/conditional forms (無かった, 無ければ)
      are out of scope and reported, not changed.
  R3 (term decision 17) Workspace -> ワークスペース in running Japanese text when
      a plain Latin word. Skipped (reported): product names ("Google
      Workspace"), lowercase "workspace" (search syntax / identifiers), standalone
      "Workspace" labels, larger identifier runs, code/URL/kagi spans. Every
      replacement is emitted as an allowances line KEY<TAB>OLD<TAB>NEW[<TAB>N] in
      exactly the format catalog-diff-check --allow-terms-file reads
      (ワークスペース is a glossary term, so the guard needs it).

Decision 22 (spelling variants such as API トークン/APIトークン, X/Xする labels)
is judgement: never automated, only LISTED as candidates for a human.

Never touched, by key (reported as excluded, never proposed):
  plan.review_prompt_* (agent-facing prompts), clean.reason.* and chat.report.*
  (Go twins: workspace/agent has the same sentences), *.speech (TTS readings),
  *_ph keys and *badge* keys (decision C keeps placeholders/badges short).

Usage:
  python3 scripts/ja-notation-normalize.py --domain settings [--rules R1,R2,R3] [--dry-run|--apply]
      [--allow-terms-out PATH] [--report PATH] [--force]
  python3 scripts/ja-notation-normalize.py --all [--rules R1,R2,R3]

--all is a dry-run over every domain: one count line per domain plus totals and
runtime. --apply edits ONLY string-literal values of the named domain file(s)
(never keys, comments, order, en/ or other files), preserves quoting/escapes/
line breaks, is idempotent, and refuses a dirty file unless --force. The string
scanner mirrors skeleton() of scripts/catalog-diff-check.py: only literal
contents change, so the guard's skeleton/keys checks stay green by construction.

Follow-up after --apply (one domain at a time, reviewed):
  python3 scripts/catalog-diff-check.py origin/develop --allow-labels --allow-terms-file <emitted file>
then --list-citations / --rewrite-guide for labels, then the test suites.
Exit status: 0 clean, 2 usage/refusal error (bad domain, dirty file, bad rules).
Local use only; not wired into CI.
"""
import argparse
import os
import re
import subprocess
import sys
import time

CATALOGUE = 'console/src/lib/i18n/locales'
JA_DIR = CATALOGUE + '/ja'

HIRAGANA = r'\u3041-\u3096'
KATAKANA = r'\u30A1-\u30FA\u30FC'
CJK = r'\u3400-\u4DBF\u4E00-\u9FFF'
JP = HIRAGANA + KATAKANA + CJK
JP_RE = '[%s]' % JP
LATIN_RE = '[A-Za-z]'
DIGIT_RE = '[0-9]'

# Japanese punctuation/brackets: anything in here next to a boundary vetoes R1.
JP_PUNCT = set('、。！？「」（）『』【】・：；…〜＝→←／＼〈〉《》［］｛｝×‥・「」')
# ASCII punctuation next to a boundary vetoes R1, except { and } (placeholder
# edges such as {n}人 are the decision-21 example and must stay eligible).
ASCII_PUNCT = set(' \t\u3000/:.,="\'()<>_+-*#@%&|\\$;!?`~^[]')
IDENT_RUN = set(
    'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:@#+=~$-')

ESCAPE = re.compile(r'\\u[0-9a-fA-F]{4}|\\.')
CODE_SPAN = re.compile(r'`[^`\\]*(?:\\.[^`\\]*)*`')
PLACEHOLDER = re.compile(r'\{\w+\}')
SLOT_TAG = re.compile(r'</?\d+/?>')
URL = re.compile('https?://[^\\s"\'\\\\）」<>%s]+' % JP)
KAGI = re.compile(r'「[^」\\]*(?:\\.[^」\\]*)*」')

R2_SCAN = re.compile('既に|無い|無く|無し|無かっ|無けれ')
R3_WORD = re.compile('Workspace')
VERB_TAIL = set('なすしさぜそ')

# Exclusion switches (mutant tests toggle these to prove each guard matters).
EXCL_CODE = 'code'
EXCL_PLACEHOLDER = 'placeholder'
EXCL_SLOT = 'slot'
EXCL_URL = 'url'
EXCL_KAGI = 'kagi'
EXCL_QUOTED_IDENT = 'quoted-ident'
EXCL_VERSION_TIME = 'version-time'
EXCL_SINGLE_LETTER = 'single-letter'
EXCL_SHORT_LABEL = 'short-label'
ALL_EXCLUSIONS = frozenset({
    EXCL_CODE, EXCL_PLACEHOLDER, EXCL_SLOT, EXCL_URL, EXCL_KAGI,
    EXCL_QUOTED_IDENT, EXCL_VERSION_TIME, EXCL_SINGLE_LETTER, EXCL_SHORT_LABEL,
})


class Fail(Exception):
    pass


def escape_spans(raw):
    return [(m.start(), m.end()) for m in ESCAPE.finditer(raw)]


def protected_spans(raw):
    """(start, end, kind) for spans no rule may edit inside."""
    out = []
    for rx, kind in ((CODE_SPAN, EXCL_CODE), (PLACEHOLDER, EXCL_PLACEHOLDER),
                     (SLOT_TAG, EXCL_SLOT), (URL, EXCL_URL),
                     (KAGI, EXCL_KAGI)):
        out += [(m.start(), m.end(), kind) for m in rx.finditer(raw)]
    return out


def inside(pos, spans, kinds=None):
    for s, e, k in spans:
        if kinds is not None and k not in kinds:
            continue
        if s < pos < e or s == pos and pos < e:
            return k
    return None


def tokenize(text):
    """Split into (kind, start, end); string contents are never interpreted.

    Mirrors skeleton() of scripts/catalog-diff-check.py: comments and key
    literals are identified so an edit can be confined to value literals.
    """
    toks, i, n = [], 0, len(text)
    while i < n:
        c = text[i]
        if text.startswith('//', i):
            j = text.find('\n', i)
            j = n if j < 0 else j
            toks.append(('comment', i, j))
            i = j
        elif text.startswith('/*', i):
            j = text.find('*/', i + 2)
            if j < 0:
                raise Fail('unterminated block comment')
            toks.append(('comment', i, j + 2))
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
            toks.append(('key' if k < n and text[k] == ':' else 'str', i, j + 1))
            i = j + 1
        else:
            toks.append(('other', i, i + 1))
            i += 1
    return toks


def parse_entries(text):
    """[(key, [(quote, start, end, raw_inner)...])] for every "k": value entry.

    A value is one string literal or several joined with +; only the literal
    contents (start+1 .. end-1) are ever edited.
    """
    toks = tokenize(text)
    entries = []
    i = 0
    while i < len(toks):
        kind, s, e = toks[i]
        if kind == 'key':
            key = text[s + 1:e - 1]
            j = i + 1
            lits = []
            while j < len(toks):
                k2, s2, e2 = toks[j]
                chunk = text[s2:e2]
                if k2 == 'str':
                    lits.append((text[s2], s2 + 1, e2 - 1))
                    j += 1
                elif k2 == 'other' and chunk.strip() in ('', ':', '+'):
                    j += 1
                else:
                    break
            if lits:
                entries.append((key, lits))
            i = j
        else:
            i += 1
    return entries


def is_jp(ch):
    return bool(re.fullmatch(JP_RE, ch))


def is_latin(ch):
    return bool(re.fullmatch(LATIN_RE, ch))


def is_digit(ch):
    return bool(re.fullmatch(DIGIT_RE, ch))


def ident_run(raw, i):
    """Maximal IDENT_RUN span containing raw[i] (raw[i] must be in it)."""
    s = i
    while s > 0 and raw[s - 1] in IDENT_RUN:
        s -= 1
    e = i
    while e < len(raw) and raw[e] in IDENT_RUN:
        e += 1
    return raw[s:e]


def boundary_kind(a, b, ph_left, ph_right):
    """(left_class, right_class) or None when this boundary is out of scope."""
    if a == '}' and ph_left:
        left = 'PH'
    elif is_latin(a):
        left = 'L'
    elif is_digit(a):
        left = 'D'
    elif is_jp(a):
        left = 'J'
    else:
        return None
    if b == '{' and ph_right:
        right = 'PH'
    elif is_latin(b):
        right = 'L'
    elif is_digit(b):
        right = 'D'
    elif is_jp(b):
        right = 'J'
    else:
        return None
    if (left == 'J') == (right == 'J'):
        return None
    if left == 'PH' and right == 'PH':
        return None
    return left, right


def r1_spots(raw, exclusions=ALL_EXCLUSIONS):
    """Return (new_raw, inserts, skips) for rule R1.

    inserts: [pos]; skips: [(pos, reason)]. Idempotent: rerunning finds none.
    """
    spans = protected_spans(raw)
    escs = escape_spans(raw)
    short = len(raw) <= 3 and EXCL_SHORT_LABEL in exclusions
    inserts, skips = [], []
    for i in range(1, len(raw)):
        a, b = raw[i - 1], raw[i]
        if a in ' \t\u3000' or b in ' \t\u3000':
            continue
        if any(s <= i - 1 < e or s < i <= e for s, e in escs):
            continue
        ph_left = raw[:i].endswith('}') and any(
            e == i and k == EXCL_PLACEHOLDER for _, e, k in spans)
        ph_right = raw[i:].startswith('{') and any(
            s == i and k == EXCL_PLACEHOLDER for s, _, k in spans)
        bk = boundary_kind(a, b, ph_left, ph_right)
        if bk is None:
            if EXCL_SLOT in exclusions and any(
                    (s == i or e == i) and k == EXCL_SLOT for s, e, k in spans) and (
                    is_latin(a) or is_digit(a) or is_jp(a)
                    or is_latin(b) or is_digit(b) or is_jp(b)):
                skips.append((i, raw[max(0, i - 15):i + 15],
                              'adjacent to <n> slot tag'))
            continue
        ctx = raw[max(0, i - 15):i + 15]
        if short:
            skips.append((i, ctx, 'single-token label (<=3 chars)'))
            continue
        inner = next((k for s, e, k in spans if s < i < e), None)
        if inner is not None and inner != EXCL_PLACEHOLDER:
            if inner not in exclusions:
                pass  # mutant path: the exclusion is off, propose below
            elif inner == EXCL_KAGI:
                skips.append((i, ctx, 'inside 「...」 (guard kagi rule has no allowance)'))
                continue
            elif inner == EXCL_CODE:
                skips.append((i, ctx, 'inside `code` span'))
                continue
            elif inner == EXCL_SLOT:
                skips.append((i, ctx, 'inside <n> slot tag'))
                continue
            elif inner == EXCL_URL:
                skips.append((i, ctx, 'inside URL'))
                continue
            else:
                continue
        if a in JP_PUNCT or b in JP_PUNCT:
            skips.append((i, ctx, 'next to Japanese punctuation/brackets'))
            continue
        if (a in ASCII_PUNCT or b in ASCII_PUNCT) and not (
                (a == '}' and ph_left) or (b == '{' and ph_right)):
            if EXCL_QUOTED_IDENT in exclusions or a not in '"\'' or b not in '"\'':
                skips.append((i, ctx, 'next to ASCII punctuation (identifier/path/flag syntax)'))
                continue
        lat = a if is_latin(a) else (b if is_latin(b) else None)
        if lat is not None:
            run = ident_run(raw, i - 1 if is_latin(a) else i)
            if EXCL_QUOTED_IDENT in exclusions and \
                    any(ch in run for ch in '_./:@#+=~$-'):
                skips.append((i, ctx, 'identifier-like run (%s)' % run))
                continue
            letters = re.search(r'[A-Za-z]+', run)
            if EXCL_SINGLE_LETTER in exclusions and letters and len(letters.group(0)) == 1 \
                    and not ph_left and not ph_right:
                skips.append((i, ctx, 'single Latin letter (possible variable)'))
                continue
            if EXCL_QUOTED_IDENT in exclusions:
                before = raw[i - len(run) - 1] if is_latin(a) and i - len(run) - 1 >= 0 else ''
                after = raw[i + len(run)] if is_latin(b) and i + len(run) < len(raw) else ''
                if before != '' and before in '`"\'\'' or \
                        after != '' and after in '`"\'\'':
                    skips.append((i, ctx, 'quoted identifier/path'))
                    continue
        if EXCL_VERSION_TIME in exclusions:
            if is_digit(a) or is_digit(b):
                run = ident_run(raw, i - 1 if (is_digit(a) or a == '}') else i) \
                    if (is_digit(a) or is_digit(b)) else ''
                if '.' in run or ':' in run:
                    skips.append((i, ctx, 'version number / time'))
                    continue
        inserts.append(i)
    new_raw = ''.join(
        ch + (' ' if i + 1 in inserts else '') for i, ch in enumerate(raw))
    return new_raw, inserts, skips


def r2_spots(raw, exclusions=ALL_EXCLUSIONS):
    """Return (new_raw, events); events: [(pos, matched, action, context)].

    action is 'CHANGE <new>' or 'SKIP <reason>'. Every occurrence on the real
    catalogue is reported so the dry-run shows all R2 contexts.
    """
    spans = protected_spans(raw)
    out, events, last = [], [], 0
    for m in R2_SCAN.finditer(raw):
        word = m.group(0)
        ctx = raw[max(0, m.start() - 20):m.end() + 20]
        k = inside(m.start() + 1, spans)
        if k is not None and k in exclusions:
            events.append((m.start(), word, 'SKIP inside %s' % k, ctx))
            continue
        if word == '既に':
            out.append((m.start(), m.end(), 'すでに'))
            events.append((m.start(), word, 'CHANGE すでに', ctx))
        elif word == '無い':
            out.append((m.start(), m.end(), 'ない'))
            events.append((m.start(), word, 'CHANGE ない', ctx))
        elif word == '無く':
            nxt = raw[m.end():m.end() + 1]
            if nxt in VERB_TAIL:
                events.append((m.start(), word + nxt,
                               'SKIP verb family 無くす/無くなる (not whitelisted)', ctx))
            else:
                out.append((m.start(), m.end(), 'なく'))
                events.append((m.start(), word, 'CHANGE なく', ctx))
        elif word == '無し':
            events.append((m.start(), word, 'SKIP noun 無し (decision 29 keeps it)', ctx))
        else:
            events.append((m.start(), word,
                           'SKIP past/conditional form (only 無い/無く in scope)', ctx))
    new_raw = raw
    for s, e, rep in sorted(out, reverse=True):
        new_raw = new_raw[:s] + rep + new_raw[e:]
    return new_raw, events


def r3_spots(raw, exclusions=ALL_EXCLUSIONS):
    """Return (new_raw, n, skips); n counts replacements for the allowance."""
    spans = protected_spans(raw)
    out, skips = [], []
    for m in R3_WORD.finditer(raw):
        ctx = raw[max(0, m.start() - 20):m.end() + 20]
        k = inside(m.start() + 1, spans)
        if k is not None and k in exclusions:
            continue
        before = raw[m.start() - 1] if m.start() > 0 else ''
        after = raw[m.end():m.end() + 1]
        # ASCII glue only: Japanese particles attach directly (空のWorkspace
        # is a plain word, not an identifier); isalnum() would misread kana.
        glue = (set('abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_')
                | set('/._:@#-'))
        if (before in glue) or (after in glue):
            skips.append((m.start(), ctx, 'part of a larger identifier run'))
            continue
        if before != '' and before in '<>"\'`' or \
                after != '' and after in '<>"\'`':
            skips.append((m.start(), ctx, 'quoted identifier/path'))
            continue
        if re.search(r'Google\s*$', raw[:m.start()]):
            skips.append((m.start(), ctx, 'product name (Google Workspace)'))
            continue
        s, e = m.start(), m.end()
        # Re-attach Japanese particles: この Workspace では becomes
        # このワークスペースでは. Only when the space touches Japanese on its
        # far side; placeholder/latin edges (同時 {n} Workspace) keep theirs.
        if s >= 2 and raw[s - 1] == ' ' and is_jp(raw[s - 2]):
            s -= 1
        if e + 1 < len(raw) and raw[e] == ' ' and is_jp(raw[e + 1]):
            e += 1
        out.append((s, e))
    if out and raw.strip() == 'Workspace':
        skips.append((out[0][0], raw, 'standalone label (ambiguous)'))
        return raw, 0, skips
    new_raw = raw
    for s, e in sorted(out, reverse=True):
        new_raw = new_raw[:s] + 'ワークスペース' + new_raw[e:]
    return new_raw, len(out), skips


def never_touch(key):
    if key.startswith('plan.review_prompt'):
        return 'agent-facing prompt (plan.review_prompt_*)'
    if key.startswith('clean.reason.'):
        return 'Go twin (clean.reason.* has the same sentence in Go)'
    if key.startswith('chat.report.'):
        return 'Go twin (chat.report.* has the same sentence in Go)'
    if key.endswith('.speech'):
        return 'TTS reading (*.speech)'
    if key.endswith('_ph') or 'badge' in key:
        return 'short-form placeholder/badge (decision C)'
    return None


def variant_candidates(values):
    """Decision-22 candidates (informational only, never changed)."""
    ellipsis = [(k, v) for k, v in values if '…' in v]
    labels = {}
    for k, v in values:
        if len(v) <= 15 and '。' not in v:
            labels.setdefault(v, []).append(k)
    pairs = []
    seen = set()
    for v, keys in sorted(labels.items()):
        if v.endswith('する') and v[:-2] in labels:
            cand = (v[:-2], v)
        elif v + 'する' in labels:
            cand = (v, v + 'する')
        else:
            continue
        if cand not in seen:
            seen.add(cand)
            pairs.append(cand)
    dups = []
    by_norm = {}
    for k, v in values:
        by_norm.setdefault(v.replace(' ', ''), []).append((k, v))
    adj = re.compile('[A-Za-z0-9][%s]|[%s][A-Za-z0-9]' % (JP, JP))
    for norm, occs in sorted(by_norm.items()):
        forms = sorted({v for _, v in occs})
        if len(forms) > 1 and adj.search(norm):
            dups.append((norm, occs))
    return ellipsis, pairs, dups


def esc(s):
    return s.replace('\\', '\\\\').replace('\n', '\\n').replace('\t', '\\t')


def analyze_domain(domain, rules, exclusions=ALL_EXCLUSIONS):
    """Full dry-run analysis of one domain file. No side effects."""
    path = os.path.join(JA_DIR, domain + '.ts')
    with open(path, encoding='utf-8') as fh:
        text = fh.read()
    result = {'domain': domain, 'path': path, 'changes': [], 'r1_skips': [],
              'r2_events': [], 'r3_skips': [], 'allowances': [],
              'excluded_keys': [], 'variants': ([], [], []), 'values': 0,
              'r1_reasons': {}, 'r3_reasons': {}}
    values = []
    for key, lits in parse_entries(text):
        why = never_touch(key)
        if why:
            result['excluded_keys'].append((key, why))
            continue
        result['values'] += 1
        old_raw = ''.join(text[s:e] for _, s, e in lits)
        values.append((key, old_raw))
        new_raw = old_raw
        changed_by = set()
        if 'R3' in rules:
            # R3 runs before R1 so 空のWorkspace becomes 空のワークスペース
            # (one allowance), never 空の Workspace.
            new_raw, n, skips = r3_spots(new_raw, exclusions)
            for _, ctx, reason in skips:
                result['r3_skips'].append((key, ctx, reason))
                result['r3_reasons'][reason] = result['r3_reasons'].get(reason, 0) + 1
            if n:
                changed_by.add('R3')
                result['allowances'].append((key, 'Workspace', 'ワークスペース', n))
        if 'R1' in rules:
            new_raw, inserts, skips = r1_spots(new_raw, exclusions)
            if inserts:
                changed_by.add('R1')
            for _, ctx, reason in skips:
                result['r1_skips'].append((key, ctx, reason))
                result['r1_reasons'][reason] = result['r1_reasons'].get(reason, 0) + 1
        if 'R2' in rules:
            new_raw, events = r2_spots(new_raw, exclusions)
            for _, word, action, ctx in events:
                result['r2_events'].append((key, word, action, ctx))
            if any(a.startswith('CHANGE') for _, _, a, _ in events):
                changed_by.add('R2')
        if new_raw != old_raw:
            if 'R1' in rules:
                check, _, _ = r1_spots(new_raw, exclusions)
                assert check == new_raw, 'R1 not idempotent on %s' % key
            if 'R2' in rules:
                check2, _ = r2_spots(new_raw, exclusions)
                assert check2 == new_raw, 'R2 not idempotent on %s' % key
            if 'R3' in rules:
                check3, _, _ = r3_spots(new_raw, exclusions)
                assert check3 == new_raw, 'R3 not idempotent on %s' % key
            result['changes'].append((key, old_raw, new_raw, sorted(changed_by)))
    return result


def report_domain(res, rules):
    L = []
    L.append('== domain: %s (%s)  rules=%s ==' % (
        res['domain'], res['path'], ','.join(rules)))
    L.append('values checked: %d  excluded keys: %d  changed values: %d' % (
        res['values'], len(res['excluded_keys']), len(res['changes'])))
    for key, why in res['excluded_keys']:
        L.append('EXCLUDED %s | %s' % (key, why))
    L.append('-- R1 spacing: %d proposed, %d skipped --' %
             (sum(1 for _, _, _, by in res['changes'] if 'R1' in by),
              len(res['r1_skips'])))
    for key, old, new, by in res['changes']:
        if 'R1' in by or 'R3' in by or 'R2' in by:
            L.append('%s | %s | %s' % (key, esc(old), esc(new)))
    for key, ctx, reason in res['r1_skips']:
        L.append('SKIPPED R1 %s | %s | %s' % (key, esc(ctx), reason))
    for reason, n in sorted(res['r1_reasons'].items()):
        L.append('r1 skip reason: %s x%d' % (reason, n))
    if 'R2' in rules:
        n_change = sum(1 for _, _, a, _ in res['r2_events'] if a.startswith('CHANGE'))
        L.append('-- R2 kana: %d occurrences, %d proposed --' % (
            len(res['r2_events']), n_change))
        for key, word, action, ctx in res['r2_events']:
            L.append('R2 %s | %s | %s | %s' % (key, word, action, esc(ctx)))
    if 'R3' in rules:
        L.append('-- R3 Workspace: %d keys, %d skipped --' % (
            len(res['allowances']), len(res['r3_skips'])))
        for key, old, new, n in res['allowances']:
            L.append('ALLOW %s\t%s\t%s\t%d' % (key, old, new, n))
        for key, ctx, reason in res['r3_skips']:
            L.append('SKIPPED R3 %s | %s | %s' % (key, esc(ctx), reason))
    ell, pairs, dups = res['variants']
    L.append('-- decision-22 variants (NOT automated, human judgement) --')
    for key, v in ell:
        L.append('ELLIPSIS %s | %s' % (key, esc(v[:80])))
    for a, b in pairs:
        L.append('PAIR %s / %s' % (a, b))
    for norm, occs in dups:
        forms = sorted({'%s:%s' % (k, v) for k, v in occs})
        L.append('DUP %s' % ' | '.join(forms))
    return '\n'.join(L)


def fill_variants(res, rules):
    with open(res['path'], encoding='utf-8') as fh:
        text = fh.read()
    vals = []
    for key, lits in parse_entries(text):
        if never_touch(key):
            continue
        vals.append((key, ''.join(text[s:e] for _, s, e in lits)))
    res['variants'] = variant_candidates(vals)


FOLLOWUP = """\
# follow-up (one domain at a time, after reviewing the report above):
python3 scripts/catalog-diff-check.py origin/develop --allow-labels --allow-terms-file {allowfile}
# then sync label citations and rewrite the guide:
python3 scripts/catalog-diff-check.py origin/develop --list-citations
python3 scripts/catalog-diff-check.py origin/develop --allow-labels --rewrite-guide
# then the suites:
python3 -m unittest discover -s scripts -p 'test_*.py'
(cd console && npm test)"""


def apply_domain(domain, rules, force, allow_out, exclusions=ALL_EXCLUSIONS):
    path = os.path.join(JA_DIR, domain + '.ts')
    st = subprocess.run(['git', 'status', '--porcelain', '--', path],
                        capture_output=True, text=True)
    if st.stdout.strip() and not force:
        raise Fail('%s is dirty (staged/unstaged/untracked change); '
                   'review it or pass --force' % path)
    with open(path, encoding='utf-8') as fh:
        text = fh.read()
    res = analyze_domain(domain, rules, exclusions)
    if not res['changes']:
        return res, False
    edits = {}
    for key, lits in parse_entries(text):
        old_raw = ''.join(text[s:e] for _, s, e in lits)
        for ck, old, new, _ in res['changes']:
            if ck == key and old == old_raw:
                # Recompute per literal (same rule order as analyze_domain)
                # to place edits exactly; the join must equal new.
                cands = []
                for _, s, e in lits:
                    cands.append((s, e))
                acc = []
                for q, s, e in lits:
                    lit_old = text[s:e]
                    lit_new = lit_old
                    if 'R3' in rules:
                        lit_new, _, _ = r3_spots(lit_new, exclusions)
                    if 'R1' in rules:
                        lit_new, _, _ = r1_spots(lit_new, exclusions)
                    if 'R2' in rules:
                        lit_new, _ = r2_spots(lit_new, exclusions)
                    acc.append(lit_new)
                assert ''.join(acc) == new, 'literal split mismatch on %s' % key
                for (s, e), part in zip(cands, acc):
                    edits[s, e] = part
                break
    out = []
    last = 0
    for s, e in sorted(edits):
        out.append(text[last:s])
        out.append(edits[s, e])
        last = e
    out.append(text[last:])
    new_text = ''.join(out)
    # The edit must change literal contents only: skeleton must be identical.
    with open(path, 'w', encoding='utf-8') as fh:
        fh.write(new_text)
    if allow_out and res['allowances']:
        with open(allow_out, 'w', encoding='utf-8') as fh:
            for key, old, new, n in res['allowances']:
                fh.write('%s\t%s\t%s\t%d\n' % (key, old, new, n))
    return res, True


def all_domains():
    import glob
    return sorted(
        os.path.basename(p)[:-3]
        for p in glob.glob(os.path.join(JA_DIR, '*.ts')))


def main(argv=None):
    ap = argparse.ArgumentParser(description='B1 notation normalizer (dry-run by default)')
    ap.add_argument('--domain', metavar='NAME[,..]',
                    help='one domain (file stem) or comma list')
    ap.add_argument('--all', action='store_true', help='dry-run every domain, count lines only')
    ap.add_argument('--rules', default='R1,R2,R3',
                    help='subset of R1,R2,R3 (default all)')
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument('--dry-run', dest='apply', action='store_false', default=None)
    mode.add_argument('--apply', dest='apply', action='store_true', default=None)
    ap.add_argument('--allow-terms-out', metavar='PATH', default=None)
    ap.add_argument('--report', metavar='PATH', default=None)
    ap.add_argument('--force', action='store_true',
                    help='apply even when the domain file is dirty')
    args = ap.parse_args(argv)
    rules = [r.strip().upper() for r in args.rules.split(',') if r.strip()]
    if not rules or any(r not in ('R1', 'R2', 'R3') for r in rules):
        print('error: --rules wants a subset of R1,R2,R3', file=sys.stderr)
        return 2
    if args.all and args.domain:
        print('error: --all and --domain exclude each other', file=sys.stderr)
        return 2
    if args.all and args.apply:
        print('error: --all is dry-run only', file=sys.stderr)
        return 2
    if args.apply and not args.domain:
        print('error: --apply needs --domain', file=sys.stderr)
        return 2
    domains = all_domains()
    if args.all:
        t0 = time.time()
        tot_v = tot_c = 0
        lines = []
        for d in domains:
            res = analyze_domain(d, rules)
            fill_variants(res, rules)
            r1 = sum(1 for _, _, _, by in res['changes'] if 'R1' in by)
            r2 = sum(1 for _, _, a, _ in res['r2_events'] if a.startswith('CHANGE'))
            r3 = len(res['allowances'])
            ell, pairs, dups = res['variants']
            tot_v += res['values']
            tot_c += len(res['changes'])
            lines.append('%s values=%d changed=%d r1=%d r2=%d r3=%d skipped_r1=%d skipped_r3=%d variants=%d+%d+%d' % (
                d, res['values'], len(res['changes']), r1, r2, r3,
                len(res['r1_skips']), len(res['r3_skips']),
                len(ell), len(pairs), len(dups)))
        print('\n'.join(lines))
        print('total values=%d changed=%d domains=%d time=%.1fs' % (
            tot_v, tot_c, len(domains), time.time() - t0))
        cross = []
        for d in domains:
            with open(os.path.join(JA_DIR, d + '.ts'), encoding='utf-8') as fh:
                text = fh.read()
            for key, lits in parse_entries(text):
                if never_touch(key):
                    continue
                cross.append(('%s:%s' % (d, key),
                              ''.join(text[s:e] for _, s, e in lits)))
        _, _, dups = variant_candidates([(k, v) for k, v in cross])
        for norm, occs in dups:
            print('DUP %s' % ' | '.join(
                sorted({'%s=%s' % (k, v) for k, v in occs})))
        return 0
    if not args.domain:
        print('error: need --domain or --all', file=sys.stderr)
        return 2
    want = [d.strip() for d in args.domain.split(',') if d.strip()]
    bad = [d for d in want if d not in domains]
    if bad:
        print('error: unknown domain(s) %s (see %s/)' % (','.join(bad), JA_DIR),
              file=sys.stderr)
        return 2
    reports = []
    for d in want:
        if args.apply:
            try:
                res, wrote = apply_domain(d, rules, args.force, args.allow_terms_out)
            except Fail as ex:
                print('error: %s' % ex, file=sys.stderr)
                return 2
            fill_variants(res, rules)
            rep = report_domain(res, rules)
            rep += '\n%s: %s' % (d, 'wrote values' if wrote else 'no changes')
        else:
            res = analyze_domain(d, rules)
            fill_variants(res, rules)
            rep = report_domain(res, rules)
            if args.allow_terms_out and res['allowances']:
                with open(args.allow_terms_out, 'w', encoding='utf-8') as fh:
                    for key, old, new, n in res['allowances']:
                        fh.write('%s\t%s\t%s\t%d\n' % (key, old, new, n))
                rep += '\nwrote allowances: %s' % args.allow_terms_out
            elif res['allowances']:
                rep += '\nallowances (pass --allow-terms-out PATH to save):'
                for key, old, new, n in res['allowances']:
                    rep += '\n%s\t%s\t%s\t%d' % (key, old, new, n)
        reports.append(rep)
    full = '\n\n'.join(reports)
    allowfile = args.allow_terms_out or '<emitted file>'
    full += '\n\n' + FOLLOWUP.format(allowfile=allowfile)
    print(full)
    if args.report:
        with open(args.report, 'w', encoding='utf-8') as fh:
            fh.write(full + '\n')
    return 0


if __name__ == '__main__':
    sys.exit(main())
