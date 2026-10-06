#!/usr/bin/env python3
"""Conservative, local-only B1 notation edits; no Node evaluation and no CI hook."""
import argparse
import collections
from dataclasses import dataclass, field
import importlib.util
import json
from pathlib import Path
import re
import shlex
import subprocess
import sys

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('catalog_guard', HERE / 'catalog-diff-check.py')
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)
JA = Path(guard.JA_DIR)
RULES = ('R1', 'R2', 'R3')
JP = r'[ぁ-ゖァ-ヺ一-鿿々〆ー]'
JAPANESE = re.compile(JP)
BOUNDARY = re.compile(r'(?<=[A-Za-z0-9])(?=' + JP + r')|(?<=' + JP + r')(?=[A-Za-z0-9])|(?<=\})(?=' + JP + r')|(?<=' + JP + r')(?=\{\w+\})')
NUMERIC_PLACEHOLDERS = {'n', 'count', 'days', 'profiles', 'hosts', 'bytes', 'applied'}
KANA = re.compile(r'既に|無い|無く|無し|無[料効制視理事限駄数]')
WORKSPACE = re.compile(r'Workspace')
# Exact inspected values keep adjective approvals from drifting into other grammar.
CONTEXT_FILE = HERE / 'ja_notation_contexts.json'


class Refusal(Exception):
    pass


@dataclass
class Token:
    text: str
    start: int
    end: int
    string: bool = False


def tokenize(text):
    """Mirror the guard's comment/string scanner, retaining source offsets."""
    out, i = [], 0
    while i < len(text):
        if text[i].isspace():
            i += 1
        elif text.startswith('//', i):
            end = text.find('\n', i)
            i = len(text) if end < 0 else end
        elif text.startswith('/*', i):
            end = text.find('*/', i + 2)
            if end < 0:
                raise Refusal('unterminated block comment')
            i = end + 2
        elif text[i] in '\'"`':
            quote, end = text[i], i + 1
            while end < len(text) and text[end] != quote:
                if text[end] == '\\':
                    end += 1
                elif text[end] in '\r\n' and quote != '`':
                    raise Refusal('unterminated string literal')
                elif quote == '`' and text.startswith('${', end):
                    raise Refusal('template substitution is unsupported')
                end += 1
            if end >= len(text):
                raise Refusal('unterminated string literal')
            out.append(Token(text[i:end + 1], i, end + 1, True))
            i = end + 1
        else:
            m = re.match(r'[A-Za-z_$][\w$]*', text[i:])
            end = i + len(m[0]) if m else i + 1
            out.append(Token(text[i:end], i, end))
            i = end
    return out


def decode(token):
    """Decoded characters map to original bytes-in-text; escapes never get re-encoded."""
    raw, chars, offsets = token.text[1:-1], [], []
    i = 0
    simple = {'n': '\n', 'r': '\r', 't': '\t', 'b': '\b', 'f': '\f', 'v': '\v',
              '0': '\0', '\\': '\\', '"': '"', "'": "'", '`': '`'}
    while i < len(raw):
        end, char = i + 1, raw[i]
        if char == '\\':
            if end >= len(raw):
                raise Refusal('incomplete escape')
            escape = raw[end]
            if escape in simple:
                char, end = simple[escape], end + 1
            elif escape in ('u', 'x'):
                count = 4 if escape == 'u' else 2
                digits = raw[end + 1:end + 1 + count]
                if len(digits) != count or not re.fullmatch(r'[0-9a-fA-F]+', digits):
                    raise Refusal('unsupported Unicode escape')
                char, end = chr(int(digits, 16)), end + 1 + count
            else:
                raise Refusal('unsupported escape; review manually')
        chars.append(char)
        offsets.append((token.start + 1 + i, token.start + 1 + end))
        i = end
    return ''.join(chars), offsets


@dataclass
class Value:
    key: str
    text: str
    offsets: list


def values(text):
    tokens = tokenize(text)
    if len(tokens) < 7 or [t.text for t in tokens[:2]] != ['export', 'const'] or \
            [t.text for t in tokens[3:5]] != ['=', '{']:
        raise Refusal('expected export const NAME = { string keys and literal values }')
    out, seen, i = [], set(), 5
    while i < len(tokens) and tokens[i].text != '}':
        if not tokens[i].string or i + 2 >= len(tokens) or tokens[i + 1].text != ':':
            raise Refusal('unsupported catalogue property')
        key, _ = decode(tokens[i])
        if key in seen:
            raise Refusal(f'duplicate key: {key}')
        seen.add(key)
        i += 2
        parts, offsets = [], []
        while i < len(tokens):
            if not tokens[i].string:
                raise Refusal(f'{key}: only string literals and + are supported')
            part, pos = decode(tokens[i])
            parts.append(part)
            offsets.extend(pos)
            i += 1
            if i < len(tokens) and tokens[i].text == '+':
                i += 1
                continue
            break
        out.append(Value(key, ''.join(parts), offsets))
        if i < len(tokens) and tokens[i].text == ',':
            i += 1
        elif i >= len(tokens) or tokens[i].text != '}':
            raise Refusal(f'{key}: unsupported value expression')
    if i >= len(tokens) or [t.text for t in tokens[i:]] not in (['}', ';'], ['}'], ['}', 'as', 'const', ';']):
        raise Refusal('unsupported catalogue suffix')
    return out


# Each recognizer has its own negative control and exclusion-removal mutant test.
PROTECTIONS = {
    'code': r'(`+)[\s\S]*?\1',
    'placeholder': r'\{[^{}]*\}',
    'slot': r'<(\d+)>[\s\S]*?</\1>|</?\d+/?>',
    'quote': r'「[^」]*」|『[^』]*』|"[^"\n]*"|\'[^\'\n]*\'|“[^”]*”|‘[^’]*’',
    'url': r'(?:https?://|www\.)[^\s「」『』"\'（）<>]+',
    'identifier': r'[A-Za-z0-9_./~@$:-]*[A-Za-z][A-Za-z0-9_./~@$:-]*',
    'range': r'\d+(?:\.\d+)?\s*[〜～~–-]\s*\d+(?:\.\d+)?',
    'time': r'\d{1,2}:\d{2}(?::\d{2})?',
    'version': r'v?\d+(?:\.\d+)+(?:[A-Za-z0-9-]+)?',
    'multiplier': r'\d+(?:\.\d+)?[xX×]|[xX×]\d+(?:\.\d+)?',
    'unit': r'\d+(?:\.\d+)?\s*[A-Za-z]+',
}


def protected_spans(text):
    spans = []
    for reason, pattern in PROTECTIONS.items():
        for m in re.finditer(pattern, text):
            if reason == 'identifier':
                word = m[0]
                # Plain ASCII words may be spaced; mixed digits, syntax and camelCase may not.
                if re.fullmatch(r'[A-Za-z]+', word) and not re.search(r'[a-z][A-Z]', word):
                    continue
            spans.append((m.start(), m.end(), reason))
    # Unbalanced markup can hide identifiers, so preserve the whole value.
    unbalanced = any(text.count(a) != text.count(b) for a, b in [('「', '」'), ('『', '』'), ('{', '}')])
    ticks = list(re.finditer(r'`+', text))
    unbalanced |= len(ticks) % 2 != 0 or any(ticks[i][0] != ticks[i + 1][0] for i in range(0, len(ticks) - 1, 2))
    unbalanced |= any(text.count(q) % 2 for q in ('"', "'"))
    stack = []
    for slot in re.finditer(r'<(/?)(\d+)(/?)>', text):
        close, number, single = slot.groups()
        if close:
            if not stack or stack.pop() != number:
                unbalanced = True
        elif not single:
            stack.append(number)
    if stack or unbalanced:
        spans.append((0, len(text), 'unbalanced markup'))
    return spans


@dataclass
class Proposal:
    value: Value
    new: str
    edits: list = field(default_factory=list)
    skipped: list = field(default_factory=list)
    contexts: list = field(default_factory=list)
    allowances: list = field(default_factory=list)


def forbidden_key(key):
    return key.startswith(('plan.review_prompt_', 'err.', 'chat.report.', 'clean.reason')) or \
        (key.startswith('notif.') and key.endswith(('speech', 'failed_speech')))


def normalize(value, rules=RULES, approved=None, terms=()):
    old = value.text
    proposal = Proposal(value, old)
    spans = protected_spans(old)
    edits = []

    def offer(rule, start, end, replacement, extra=None):
        reason = extra
        for a, b, why in spans:
            overlaps = ((a < start < b) if why == 'placeholder' else (a <= start <= b)) if start == end else (a < end and start < b)
            if overlaps:
                reason = why
                break
        if forbidden_key(value.key):
            reason = 'excluded agent-facing/Go-twin/speech key'
        if reason:
            proposal.skipped.append((rule, start, reason, old))
        else:
            edits.append((start, end, replacement, rule))

    if 'R1' in rules:
        for m in BOUNDARY.finditer(old):
            pos = m.start()
            reason = 'single-token label <=3 characters' if len(old) <= 3 and not re.search(r'\s', old) else None
            if old[pos - 1] == '}' or old[pos] == '{':
                # Only a complete placeholder boundary is safe; its contents remain protected.
                valid = re.search(r'\{\w+\}$', old[:pos]) if old[pos - 1] == '}' else re.match(r'\{\w+\}', old[pos:])
                if not valid or valid[0][1:-1] not in NUMERIC_PLACEHOLDERS:
                    reason = 'untyped placeholder; numeric boundary is not approved'
            offer('R1', pos, pos, ' ', reason)
    if 'R2' in rules:
        for m in KANA.finditer(old):
            proposal.contexts.append((m.start(), m[0], old))
            word = m[0]
            if word not in ('既に', '無い', '無く'):
                proposal.skipped.append(('R2', m.start(), 'noun/compound; never a kana rewrite', old))
                continue
            reason = None
            if word in ('無い', '無く'):
                if old not in (approved or {}).get(value.key, []):
                    reason = 'adjective context is not in the exact-value whitelist'
                if word == '無く' and re.match(r'(す|な)', old[m.end():]):
                    reason = 'verb form; no explicit whitelist for this use'
            offer('R2', m.start(), m.end(), {'既に': 'すでに', '無い': 'ない', '無く': 'なく'}[word], reason)
    if 'R3' in rules:
        for m in WORKSPACE.finditer(old):
            before, after = old[:m.start()], old[m.end():]
            reason = None
            if re.search(r'[A-Za-z0-9_./:-]$', before) or re.match(r'[A-Za-z0-9_./:-]', after):
                reason = 'Workspace is part of an identifier'
            elif re.search(r'[A-Za-z]+\s+$', before) or re.match(r'\s+[A-Za-z]+', after):
                reason = 'adjacent Latin word; possible product name'
            elif not JAPANESE.search(old):
                reason = 'no running Japanese text; standalone/English label'
            offer('R3', m.start(), m.end(), 'ワークスペース', reason)
    # Ranges and escapes must map to a single literal, without touching its syntax.
    safe = []
    for start, end, replacement, rule in edits:
        offsets = value.offsets
        if start == end:
            okay = start > 0 and start < len(offsets) and offsets[start - 1][1] == offsets[start][0]
            a = b = offsets[start][0] if okay else 0
        else:
            okay = all(offsets[i][1] - offsets[i][0] == 1 for i in range(start, end)) and \
                all(offsets[i][1] == offsets[i + 1][0] for i in range(start, end - 1))
            a, b = offsets[start][0], offsets[end - 1][1]
        if not okay:
            proposal.skipped.append((rule, start, 'escape or concatenated-literal boundary', old))
        else:
            safe.append((start, end, replacement, rule, a, b))
    # A successful Latin-to-kana conversion removes its own spacing boundaries.
    converted_edges = {edge for start, end, _, rule, *_ in safe if rule == 'R3' for edge in (start, end)}
    retained = []
    for edit in safe:
        if edit[3] == 'R1' and edit[0] in converted_edges:
            proposal.skipped.append(('R1', edit[0], 'boundary removed by Workspace conversion', old))
        else:
            retained.append(edit)
    safe = retained
    new = old
    for start, end, replacement, *_ in sorted(safe, reverse=True):
        new = new[:start] + replacement + new[end:]
    n = old.count('Workspace') - new.count('Workspace')
    allowances = [(value.key, 'Workspace', 'ワークスペース', n)] if n else []
    # Changes must satisfy the guard's multisets, with only the approved Workspace drift.
    reason = None
    for name, rx in guard.INVARIANTS.items():
        a, b = collections.Counter(rx.findall(old)), collections.Counter(rx.findall(new))
        if name == 'latin' and n:
            a['Workspace'] -= n
            a = +a
        if a != b:
            reason = f'guard {name} invariant would change'
    for term in terms:
        adjustment = n if term == 'ワークスペース' else -n if term == 'Workspace' else 0
        if old.count(term) + adjustment != new.count(term):
            reason = f'guard glossary invariant would change: {term}'
    if reason:
        proposal.skipped.extend((rule, start, reason, old) for start, _, _, rule, *_ in safe)
    else:
        proposal.new, proposal.edits, proposal.allowances = new, safe, allowances
    return proposal


def read_source(path):
    return path.read_bytes().decode('utf-8')


def escaped(text):
    return text.replace('\\', '\\\\').replace('\r', '\\r').replace('\n', '\\n').replace('\t', '\\t').replace('|', '\\|')


def git(root, *args):
    p = subprocess.run(['git', '-C', str(root), *args], capture_output=True, text=True)
    if p.returncode:
        raise Refusal(p.stderr.strip())
    return p.stdout


def candidate_pairs(all_values, domains):
    groups = collections.defaultdict(lambda: collections.defaultdict(list))
    for key, (domain, value) in all_values.items():
        if guard.label_like(value.text):
            base = re.sub(r'[\s…]+', '', value.text).replace('を', '')
            groups[base][value.text].append((domain, key))
    for labels in groups.values():
        names = sorted(labels)
        for i, label in enumerate(names):
            for other in names[i + 1:]:
                keys = labels[label] + labels[other]
                if any(d in domains for d, _ in keys):
                    yield label, other, sorted(k for _, k in keys)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__)
    selection = ap.add_mutually_exclusive_group(required=True)
    selection.add_argument('--domain', help='comma-separated domain names')
    selection.add_argument('--all', action='store_true', help='counts for every domain; dry-run only')
    ap.add_argument('--rules', default=','.join(RULES))
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument('--dry-run', action='store_true')
    mode.add_argument('--apply', action='store_true')
    ap.add_argument('--force', action='store_true', help='apply to reviewed dirty catalogue files')
    ap.add_argument('--allow-terms-out', type=Path)
    ap.add_argument('--report', type=Path)
    args = ap.parse_args(argv)
    rules = tuple(dict.fromkeys(args.rules.split(',')))
    if not rules or any(r not in RULES for r in rules):
        ap.error('--rules needs R1,R2,R3 (any nonempty subset)')
    if args.all and args.apply:
        ap.error('--all is dry-run only')
    if args.force and not args.apply:
        ap.error('--force needs --apply')
    try:
        root = Path(git(Path.cwd(), 'rev-parse', '--show-toplevel').strip())
        ja = root / JA
        if ja.is_symlink() or ja.resolve() != ja.absolute():
            raise Refusal('catalogue directory must not contain symlinks')
        available = sorted(p.stem for p in ja.glob('*.ts'))
        if not available:
            raise Refusal('no catalogue domains found')
        domains = available if args.all else sorted(set(args.domain.split(',')))
        if any(not re.fullmatch(r'[A-Za-z0-9_-]+', d) or d not in available for d in domains):
            raise Refusal('unknown or unsafe domain name')
        approved = json.loads(CONTEXT_FILE.read_text(encoding='utf-8'))
        terms = guard.glossary_terms(read_source(root / guard.GLOSSARY))
        all_values, sources = {}, {}
        for domain in available:
            path = ja / (domain + '.ts')
            if path.is_symlink():
                raise Refusal(f'symlink catalogue: {path.relative_to(root)}')
            source = read_source(path)
            sources[domain] = source
            for value in values(source):
                if value.key in all_values:
                    raise Refusal(f'duplicate key across domains: {value.key}')
                all_values[value.key] = (domain, value)
        proposals = {key: normalize(v, rules, approved, terms) for key, (d, v) in all_values.items() if d in domains}
        # Fixed point: rejecting one participant can make another shared-label edit unsafe.
        while True:
            rejected = []
            for key, proposal in proposals.items():
                old, new = proposal.value.text, proposal.new
                if old == new or not (guard.label_like(old) or guard.label_like(new)):
                    continue
                others = [k for k, (_, v) in all_values.items() if v.text == old and
                          (proposals[k].new if k in proposals else v.text) != new]
                if others:
                    rejected.append((key, others))
            if not rejected:
                break
            for key, others in rejected:
                proposal = proposals[key]
                proposal.skipped.extend((r, s, 'shared label would SPLIT: ' + ', '.join(sorted(others)), proposal.value.text)
                                        for s, _, _, r, *_ in proposal.edits)
                proposal.new, proposal.edits, proposal.allowances = proposal.value.text, [], []
        outputs = [p for p in (args.report, args.allow_terms_out) if p is not None]
        if len({p.absolute().resolve() for p in outputs}) != len(outputs):
            raise Refusal('output paths must be distinct')
        for path in outputs:
            target = path.absolute().resolve()
            if target.is_relative_to((root / '.git').resolve()) or target.is_relative_to(root / JA.parent) or \
                    target.is_relative_to(root / 'scripts') or (target.exists() and not target.is_file()):
                raise Refusal('output paths must not overwrite catalogue, scripts, or directories')
            if path.is_symlink() or target in [p.absolute().resolve() for p in outputs if p != path]:
                raise Refusal('output paths must be distinct regular files')
            if target.exists():
                raise Refusal(f'output already exists: {path}; use a fresh artifact path')
            if not target.parent.is_dir():
                raise Refusal(f'output parent does not exist: {path}')
        rows = [row for p in proposals.values() for row in p.allowances]
        if args.apply and rows and args.allow_terms_out is None:
            raise Refusal('Workspace changes require --allow-terms-out PATH before applying')
        if args.apply:
            for d in domains:
                path = JA / (d + '.ts')
                if not args.force and git(root, 'status', '--porcelain', '--', str(path)).strip():
                    raise Refusal(f'dirty catalogue file: {path}; review before --force')
        lines = ['APPLY' if args.apply else 'DRY RUN (no catalogue writes)']
        for domain in domains:
            selected = [p for k, p in proposals.items() if all_values[k][0] == domain]
            count = collections.Counter(e[3] for p in selected for e in p.edits)
            lines.append(f'DOMAIN {domain}: ' + ', '.join(f'{r}={count[r]}' for r in RULES) +
                         f'; changed={sum(p.new != p.value.text for p in selected)}; skipped={sum(len(p.skipped) for p in selected)}; ' +
                         ', '.join(f'skipped_{r}={sum(s[0] == r for p in selected for s in p.skipped)}' for r in RULES))
            for p in selected:
                for pos, word, text in p.contexts:
                    lines.append(f'CONTEXT R2 {p.value.key}@{pos} {word} | {escaped(text)}')
                if p.new != p.value.text:
                    rs = ','.join(r for r in RULES if any(e[3] == r for e in p.edits))
                    lines.append(f'{p.value.key} | {escaped(p.value.text)} | {escaped(p.new)} [{rs}]')
                for rule, pos, reason, text in p.skipped:
                    lines.append(f'SKIPPED {rule} {p.value.key}@{pos}: {reason} | {escaped(text)}')
        for label, other, keys in candidate_pairs(all_values, domains):
            lines.append(f'CANDIDATE (manual only) {escaped(label)} / {escaped(other)} | ' + ','.join(keys))
        for key, old, new, n in rows:
            lines.append(f'ALLOW {key}\t{old}\t{new}\t{n}')
        allowance_arg = str(args.allow_terms_out) if args.allow_terms_out else '<emitted-file>'
        command = 'python3 scripts/catalog-diff-check.py origin/develop --allow-labels'
        if rows or args.allow_terms_out:
            command += ' --allow-terms-file ' + (shlex.quote(allowance_arg) if args.allow_terms_out else allowance_arg)
        lines.extend(['FOLLOW-UP ' + command, 'FOLLOW-UP ' + command + ' --list-citations',
                      'FOLLOW-UP ' + command + ' --rewrite-guide',
                      'Then resolve remaining citations, rerun the guard, and run tests (see development §10.6).'])
        report = '\n'.join(lines) + '\n'
        rewritten = {}
        if args.apply:
            replacements = collections.defaultdict(list)
            for key, p in proposals.items():
                replacements[all_values[key][0]].extend(p.edits)
            for domain, edits in replacements.items():
                source = sources[domain]
                for _, _, replacement, _, a, b in sorted(edits, key=lambda e: e[4], reverse=True):
                    source = source[:a] + replacement + source[b:]
                planned = {k: p.new for k, p in proposals.items() if all_values[k][0] == domain}
                if {v.key: v.text for v in values(source)} != planned:
                    raise Refusal(f'{domain}: source edits did not match the planned values')
                if guard.skeleton(source) != guard.skeleton(sources[domain]):
                    raise Refusal(f'{domain}: skeleton changed')
                if source != sources[domain]:
                    rewritten[domain] = source
            # Recheck all inputs after planning, before the first write.
            for domain in domains:
                if read_source(ja / (domain + '.ts')) != sources[domain]:
                    raise Refusal('catalogue changed during planning; rerun')
        # Write approvals before catalogues; exclusive creation prevents racing artifact overwrites.
        if args.allow_terms_out:
            with args.allow_terms_out.open('x', encoding='utf-8') as fh:
                fh.write(''.join('\t'.join(map(str, row)) + '\n' for row in rows))
        if args.report:
            with args.report.open('x', encoding='utf-8') as fh:
                fh.write(report)
        for domain, source in rewritten.items():
            (ja / (domain + '.ts')).write_bytes(source.encode('utf-8'))
        print(report, end='')
        return 0
    except (Refusal, guard.Fail, OSError, ValueError) as e:
        print(f'error: {e}', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
