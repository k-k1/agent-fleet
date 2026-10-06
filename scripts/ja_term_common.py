"""Shared, local-only B2 term registry and conservative catalogue operations."""
from dataclasses import dataclass
import importlib.util
from pathlib import Path
import re
import sys

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('term_notation', HERE / 'ja-notation-normalize.py')
notation = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = notation
spec.loader.exec_module(notation)
guard = notation.guard
Refusal = notation.Refusal
JA = Path(guard.JA_DIR)


def both(pairs):
    return tuple(pair for a, b in pairs for pair in ((a, b), (b, a)))


BUTTONS = ('コピー', 'リセット', '停止', '共有', '再認証', '再開', '削除', '失効',
           '完全に削除', '待機', '掃除', '接続', '登録', '破棄', '表示', '起動')
# Whole-label pairs cannot authorize inserting arbitrary spaces or ellipses in prose.
VARIANTS = (
    ('APIトークン', 'API トークン'), (' いいね', 'いいね'), ('起動中', '起動中…'),
    ('30日', '30 日'), ('停止中', '停止中…'), ('保存中', '保存中…'),
    ('{n}人', '{n} 人'), (' ほか{n}件', ' ほか {n} 件'), ('確認中', '確認中…'),
    ('カスタム', 'カスタム…'), ('承認待ち', '承認待ち…'),
    ('ブランチ削除', 'ブランチを削除'), ('セッション削除', 'セッションを削除'),
    ('進行中', '進行中…'), ('送信', '送信…'), ('実行中', '実行中…'),
    ('予算を上げて再開', '予算を上げて再開…'), ('プロファイルを選択', 'プロファイルを選択…'),
    ('絞り込み', '絞り込み…'), ('共有する', '共有する…'),
    ('…ほか {count} 件', 'ほか {count} 件'), ('Git Flow を初期化', 'Git Flow を初期化…'),
    ('コマンド・セッションを検索', 'コマンド・セッションを検索…'),
    ('Gitホスティング', 'Git ホスティング'), ('{n}ファイル', '{n} ファイル'),
)
PAIRS = {
    'F-login': both((('サインイン', 'ログイン'),)),
    'F-deploy': (('デプロイ既定', '配備の既定'), *both((('デプロイ', '配備'),))),
    'F-device': (('この端末', 'このブラウザ'), ('端末', 'ブラウザ'), ('端末', 'このブラウザ')),
    'F-slot': (('枠', '利用枠'), ('枠', '子の上限'), ('利用枠', '子の上限')),
    'F-fold': tuple((a, b) for a, bs in (
        ('畳む', ('停止', '終了', '停止する', '終了する')),
        ('畳まれ', ('停止され', '終了され')),
        ('畳んだ', ('停止した', '終了した')),
        ('畳んで', ('停止して', '終了して')),
        ('畳め', ('停止でき', '終了でき')),
    ) for b in bs),
    'F-onoff': (('ON', 'オン'), ('OFF', 'オフ'), *both((('オン', '有効'), ('オフ', '無効')))),
    'F-buttons': both(tuple((noun, noun + 'する') for noun in BUTTONS)),
    'F-default': (('デフォルト', '既定'),),
    'F-variants': both(VARIANTS),
}
SENSES = {
    'F-login': 'Agent Fleet IdP: サインイン; external CLI/service: ログイン. Review tenant group citations.',
    'F-deploy': 'Noun: 配備 (配備の既定, 配備全体); verb デプロイする stays.',
    'F-device': 'Browser-local/device settings: ブラウザ; terminal emulator: 端末 stays.',
    'F-slot': 'Usage quota: 利用枠; child count: 子の上限; EC2 only: スロット. Review each period/free quota.',
    'F-fold': 'UI collapse: 畳む stays; ending a session: 停止/終了. Data roll-up is a separate sense; defer it. Agent prompts are excluded.',
    'F-onoff': 'Prose: オン/オフ; follow a control named 有効/無効; never Latin ON/OFF in prose.',
    'F-buttons': 'Noun on buttons; する only on a confirmation dialog execute button. Read the component.',
    'F-default': 'Initial setting: 既定.',
    'F-variants': '… only for in-progress displays/dialog-opening buttons; otherwise majority spelling. Read the component.',
}
WHOLE = {'F-buttons', 'F-variants'}


def excluded(key):
    return 'prompt' in key.lower() or re.search(r'(?:^|[._])speech(?:[._]|$)', key) is not None or \
        key.startswith(('err.', 'chat.report.', 'clean.reason'))


def escape(text):
    return text.replace('\\', '\\\\').replace('\t', '\\t').replace('\n', '\\n').replace('\r', '\\r')


def unescape(text):
    out, i = [], 0
    escapes = {'\\': '\\', 't': '\t', 'n': '\n', 'r': '\r'}
    while i < len(text):
        if text[i] == '\\':
            i += 1
            if i == len(text) or text[i] not in escapes:
                raise Refusal('TSV fields allow only \\\\, \\t, \\n, \\r escapes')
            out.append(escapes[text[i]])
        else:
            out.append(text[i])
        i += 1
    return ''.join(out)


def root_dir():
    return Path(notation.git(Path.cwd(), 'rev-parse', '--show-toplevel').strip())


def load_catalogue(root, lang='ja'):
    directory = root / JA.parent / lang
    if directory.is_symlink() or directory.resolve() != directory.absolute():
        raise Refusal(f'{lang} catalogue directory must not contain symlinks')
    entries, sources, neighbours = {}, {}, {}
    for path in sorted(directory.glob('*.ts')):
        if path.is_symlink():
            raise Refusal(f'symlink catalogue: {path.relative_to(root)}')
        source = notation.read_source(path)
        vals = english_values(source) if lang == 'en' else notation.values(source)
        sources[path.stem] = source
        for i, value in enumerate(vals):
            if value.key in entries:
                raise Refusal(f'duplicate key across domains: {value.key}')
            entries[value.key] = (path.stem, value)
            neighbours[value.key] = tuple(vals[j] for j in (i - 1, i + 1) if 0 <= j < len(vals))
    if not sources:
        raise Refusal(f'no {lang} catalogue domains found')
    return entries, sources, neighbours


def english_values(source):
    """Read the en type-only wrapper without executing imports or TypeScript."""
    tokens = notation.tokenize(source)
    if tokens and tokens[0].text == 'import':
        prefix = [t.text for t in tokens[:11]]
        if len(prefix) != 11 or prefix[:3] != ['import', 'type', '{'] or \
                prefix[4] != 'as' or prefix[6:8] != ['}', 'from'] or not tokens[8].string or \
                prefix[9] != ';' or prefix[10] != 'export':
            raise Refusal('unsupported en type-only import wrapper')
        tokens = tokens[10:]
    if len(tokens) > 3 and tokens[3].text == ':':
        if len(tokens) < 14 or [t.text for t in tokens[:2]] != ['export', 'const'] or \
                [t.text for t in tokens[3:8]] != [':', 'Record', '<', 'keyof', 'typeof'] or \
                [t.text for t in tokens[9:14]] != [',', 'string', '>', '=', '{']:
            raise Refusal('unsupported en type annotation')
        return notation.values('export const en = ' + source[tokens[13].start:])
    return notation.values(source[tokens[0].start:] if tokens else source)


def families(text):
    selected = text.split(',')
    if not selected or len(set(selected)) != len(selected) or any(f not in PAIRS for f in selected):
        raise Refusal('families must be a comma-separated, unique subset of ' + ','.join(PAIRS))
    return tuple(f for f in PAIRS if f in selected)


def occurrences(text, family):
    if family in WHOLE:
        return [(0, len(text), text)] if any(text == a for a, _ in PAIRS[family]) else []
    terms = {s for pair in PAIRS[family] for s in pair}
    if family == 'F-device':
        terms = {'端末', 'このブラウザ'}
    if family == 'F-fold':
        terms = {'畳む', '畳まれ', '畳んだ', '畳んで', '畳め', '畳み', '畳ま', '畳ん', '畳も'}
    if family == 'F-default':
        terms.add('既定')
    rx = '|'.join(('(?<![A-Za-z0-9_])' + re.escape(t) + '(?![A-Za-z0-9_])')
                  if t in ('ON', 'OFF') else re.escape(t)
                  for t in sorted(terms, key=lambda t: (-len(t), t)))
    return [(m.start(), m.end(), m[0]) for m in re.finditer(rx, text)]


@dataclass(frozen=True)
class Row:
    key: str
    old: str
    new: str
    family: str
    reason: str
    line: int = 0


def read_plan(path):
    rows, seen = [], set()
    source = notation.read_source(path)
    if source.startswith('\ufeff'):
        raise Refusal(f'{path}: UTF-8 BOM is unsupported; save UTF-8 without a BOM')
    for no, line in enumerate(source.split('\n'), 1):
        line = line.removesuffix('\r')
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        fields = line.split('\t')
        if len(fields) != 5 or not all(fields):
            raise Refusal(f'{path}:{no}: want key<TAB>old_value<TAB>new_value<TAB>family<TAB>reason')
        row = Row(*(unescape(f) for f in fields), line=no)
        if row.key in seen:
            raise Refusal(f'{path}:{no}: duplicate/conflicting plan key: {row.key}')
        seen.add(row.key)
        if row.family not in PAIRS:
            raise Refusal(f'{path}:{no}: unknown family: {row.family}')
        if row.old == row.new or not row.reason.strip():
            raise Refusal(f'{path}:{no}: plan requires a change and a review reason')
        rows.append(row)
    if not rows:
        raise Refusal('empty plan')
    return rows


def verify_edit_directions(old, edits, family):
    # Opposite edits can swap senses while hiding all glossary-count changes.
    directions = set()
    for start, end, replacement in edits:
        original = old[start:end]
        if (replacement, original) in directions:
            raise Refusal(f'{family}: opposite directions of the same pair in one value: '
                          f'{original!r} <-> {replacement!r} at old offset {start}')
        directions.add((original, replacement))


def substitution_edits(old, new, family):
    """Find an exact alignment made only of unchanged characters and approved pairs."""
    if family in WHOLE:
        if (old, new) not in PAIRS[family]:
            raise Refusal(f'{family}: whole value must be an explicit label pair')
        # Minimal edits preserve escaped placeholders inside a whole-label pair.
        prefix = 0
        while prefix < min(len(old), len(new)) and old[prefix] == new[prefix]:
            prefix += 1
        suffix = 0
        while suffix < min(len(old), len(new)) - prefix and old[-1 - suffix] == new[-1 - suffix]:
            suffix += 1
        return [(prefix, len(old) - suffix, new[prefix:len(new) - suffix if suffix else len(new)])]
    pairs = sorted(PAIRS[family], key=lambda pair: (-len(pair[0]), pair))
    # Iterative reachability avoids recursion limits on long catalogue paragraphs.
    predecessors = {(0, 0): None}
    pending = [(0, 0)]
    for i, j in pending:
        if (i, j) == (len(old), len(new)):
            break
        moves = []
        if i < len(old) and j < len(new) and old[i] == new[j]:
            moves.append((i + 1, j + 1, None))
        for a, b in pairs:
            if old.startswith(a, i) and new.startswith(b, j):
                if a in ('ON', 'OFF') and (i and re.fullmatch(r'[A-Za-z0-9_]', old[i - 1]) or
                                          i + len(a) < len(old) and re.fullmatch(r'[A-Za-z0-9_]', old[i + len(a)])):
                    continue
                moves.append((i + len(a), j + len(b), (i, i + len(a), b)))
        for ni, nj, edit in moves:
            if (ni, nj) not in predecessors:
                predecessors[ni, nj] = ((i, j), edit)
                pending.append((ni, nj))
    state = (len(old), len(new))
    if state not in predecessors:
        furthest = max(predecessors, key=lambda p: (sum(p), p))
        raise Refusal(f'{family}: unapproved rewrite near old offset {furthest[0]}, new offset {furthest[1]}: '
                      f'{old[furthest[0]:furthest[0]+24]!r} -> {new[furthest[1]:furthest[1]+24]!r}')
    edits = []
    while predecessors[state] is not None:
        state, edit = predecessors[state]
        if edit:
            edits.append(edit)
    edits.sort()
    verify_edit_directions(old, edits, family)
    return edits


def verify_protections(row, edits):
    spans = notation.protected_spans(row.old)
    spans += [(m.start(), m.end(), 'digits/newlines') for m in re.finditer(r'\d+|[\r\n]', row.old)]
    for start, end, _ in edits:
        for a, b, why in spans:
            if row.family == 'F-onoff' and why == 'identifier' and re.fullmatch(r'(?:ON|OFF)/(?:ON|OFF)', row.old[a:b]):
                continue
            if a < end and start < b or start == end and a < start < b:
                raise Refusal(f'{row.key}: protected {why} at old offsets {a}:{b}')
    for name, rx in guard.INVARIANTS.items():
        if name != 'latin' and rx.findall(row.old) != rx.findall(row.new):
            raise Refusal(f'{row.key}: {name} changed')
    for char in ('\n', '\r'):
        if row.old.count(char) != row.new.count(char):
            raise Refusal(f'{row.key}: newlines changed')
    if row.old[:len(row.old) - len(row.old.lstrip())] != row.new[:len(row.new) - len(row.new.lstrip())] or \
            row.old[len(row.old.rstrip()):] != row.new[len(row.new.rstrip()):]:
        raise Refusal(f'{row.key}: edge whitespace changed')


def verify_current(row, entries):
    if row.key not in entries:
        raise Refusal(f'{row.key}: key is absent from ja/')
    current = entries[row.key][1].text
    if current not in (row.old, row.new):
        raise Refusal(f'{row.key}: current value does not equal old_value (or already-applied new_value) byte for byte: '
                      f'expected {row.old!r}, got {current!r}')
    return current == row.new


def verify_excluded(row):
    if excluded(row.key):
        raise Refusal(f'{row.key}: excluded prompt/speech/error/Go-twin key')


def verify_split(rows, entries):
    by_key = {r.key: r for r in rows}
    for row in rows:
        if not (guard.label_like(row.old) or guard.label_like(row.new)):
            continue
        # Reconstruct the pre-plan label group, including already-applied keys.
        others = sorted(k for k, (_, v) in entries.items()
                        if (by_key[k].old if k in by_key else v.text) == row.old)
        missing = [k for k in others if k not in by_key or by_key[k].new != row.new]
        if missing:
            raise Refusal(f'{row.key}: shared label would SPLIT; all keys need the same new value: ' + ', '.join(missing))


def verify_plan(rows, entries):
    if len({r.key for r in rows}) != len(rows):
        raise Refusal('duplicate/conflicting plan key')
    checked = {}
    for row in rows:
        applied = verify_current(row, entries)
        verify_excluded(row)
        try:
            edits = substitution_edits(row.old, row.new, row.family)
            verify_protections(row, edits)
        except Refusal as e:
            raise Refusal(f'plan line {row.line}, {row.key}: {e}') from e
        checked[row.key] = (applied, edits)
    verify_split(rows, entries)
    return checked


def source_edits(value, edits):
    out = []
    for start, end, replacement in edits:
        if start == end:
            if not value.offsets or 0 < start < len(value.offsets) and value.offsets[start - 1][1] != value.offsets[start][0]:
                raise Refusal(f'{value.key}: insertion at a literal edge/boundary requires manual review')
            a = b = value.offsets[start][0] if start < len(value.offsets) else value.offsets[-1][1]
        else:
            offsets = value.offsets[start:end]
            if any(b - a != 1 for a, b in offsets) or any(offsets[i][1] != offsets[i + 1][0] for i in range(len(offsets) - 1)):
                raise Refusal(f'{value.key}: substitution crosses an escape or concatenated literal boundary')
            a, b = offsets[0][0], offsets[-1][1]
        # Replacement syntax must remain inside the existing string literal.
        if any(c in replacement for c in ('\\', '"', "'", '`', '\n', '\r', '\t')):
            raise Refusal(f'{value.key}: replacement contains string-literal syntax')
        out.append((a, b, replacement))
    return out
