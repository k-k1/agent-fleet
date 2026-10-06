#!/usr/bin/env python3
"""List attributed B2 term occurrences for a model/human judge; never choose a sense."""
import argparse
import collections
from pathlib import Path
import re
import sys
import time

import ja_term_common as common

FIELDS = ('family', 'key', 'domain', 'kind', 'value', 'matched_term', 'start', 'end',
          'sentence', 'previous_key', 'previous_value', 'next_key', 'next_value', 'en_value',
          'guide_count', 'guide_first_two', 'tests_hits', 'go_hits', 'af_usage_hits',
          'split_keys', 'excluded', 'sense', 'ui_hits', 'source')


def sentence(text, start, end):
    separators = '。！？\n'
    a = max((text.rfind(c, 0, start) for c in separators), default=-1) + 1
    ends = [text.find(c, end) for c in separators if text.find(c, end) >= 0]
    b = min(ends) + 1 if ends else len(text)
    return text[a:b]


def references(entries):
    """Use exact label sync plus the guard's hard-wrap-aware sentence PINNED scanner."""
    label_changes = [(k, str(common.JA / (d + '.ts')), v.text, v.text)
                     for k, (d, v) in entries.items() if common.guard.label_like(v.text)]
    hits, texts = common.guard.citations(label_changes, 'ja')
    result = collections.defaultdict(lambda: collections.defaultdict(set))
    for section, path, line, _, _, _, key, _, _ in hits:
        result[key][section].add((path, line))
    sources = common.guard.Sources()
    for key, (_, value) in entries.items():
        if common.guard.label_like(value.text):
            continue
        for fragment in common.guard.clauses(value.text, ''):
            for path, line in sources.find(fragment):
                section = ('guide' if path.startswith('guide/') else
                           'console tests' if path.startswith(('console/', 'console-e2e/')) else
                           'Go sources' if path.endswith('.go') else 'af-usage.md')
                result[key][section].add((path, line))
                if path not in texts:
                    texts[path] = common.notation.read_source(Path(path))
    return result, texts


def ui_locations(entries):
    result = collections.defaultdict(set)
    if not entries:
        return result
    rx = re.compile(r'(["\x27\x60])(' + '|'.join(re.escape(k) for k in sorted(entries)) + r')\1')
    for path in sorted(set(common.guard.git('ls-files', '-co', '--exclude-standard', '-z').split('\0'))):
        if not path.startswith('console/src/') or path.startswith(common.guard.CATALOGUE + '/') or \
                '.test.' in path or not path.endswith(common.guard.CODE_SUFFIXES):
            continue
        try:
            text = common.notation.read_source(Path(path))
        except (OSError, UnicodeError):
            continue
        for line, content in enumerate(text.splitlines(), 1):
            for match in rx.finditer(content):
                result[match[2]].add((path, line))
    return result


def render_rows(entries, all_entries, neighbours, en, selected):
    refs, texts = references(entries)
    uses = ui_locations(entries)
    shared = collections.defaultdict(list)
    for key, (_, v) in all_entries.items():
        shared[v.text].append(key)
    for key, (domain, value) in entries.items():
        for family in selected:
            for start, end, term in common.occurrences(value.text, family):
                adjacent = neighbours[key]
                # Neighbours retain file order, even across cards; no card boundary is inferred.
                all_keys = list(k for k, (d, _) in all_entries.items() if d == domain)
                index = all_keys.index(key)
                previous = adjacent[0] if index else None
                following = adjacent[-1] if index < len(all_keys) - 1 else None
                def locations(section):
                    return '; '.join(f'{p}:{n}' for p, n in sorted(refs[key][section]))
                guide = sorted(refs[key]['guide'])
                preview = '; '.join(f'{p}:{n}: {texts[p].splitlines()[n - 1]}' for p, n in guide[:2])
                yield (family, key, domain, 'label' if common.guard.label_like(value.text) else 'sentence',
                       value.text, term, str(start), str(end), sentence(value.text, start, end),
                       previous.key if previous else '', previous.text if previous else '',
                       following.key if following else '', following.text if following else '',
                       en[key][1].text if key in en else '', str(len(guide)), preview,
                       locations('console tests'), locations('Go sources'),
                       '; '.join(filter(None, (locations('af-usage.md'), locations('af-usage.coverage.tsv')))),
                       ','.join(sorted(shared[value.text])) if len(shared[value.text]) > 1 else '',
                       'yes' if common.excluded(key) else 'no', common.SENSES[family],
                       '; '.join(f'{p}:{n}' for p, n in sorted(uses[key])),
                       str(common.JA / (domain + '.ts')))


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--family', default=','.join(common.PAIRS))
    ap.add_argument('--domain', help='comma-separated domain stems')
    ap.add_argument('--format', choices=('tsv', 'md'), default='tsv')
    ap.add_argument('--limit', type=int)
    ap.add_argument('--stats', action='store_true', help='per-family/domain occurrence, value and label counts plus runtime')
    args = ap.parse_args(argv)
    if args.limit is not None and args.limit < 0:
        ap.error('--limit must be nonnegative')
    try:
        started = time.monotonic()
        selected = common.families(args.family)
        root = common.root_dir()
        all_entries, _, neighbours = common.load_catalogue(root)
        available = {d for d, _ in all_entries.values()}
        domains = args.domain.split(',') if args.domain else sorted(available)
        if any(d not in available for d in domains):
            raise common.Refusal('unknown domain')
        entries = {k: v for k, v in all_entries.items() if v[0] in domains and
                   any(common.occurrences(v[1].text, f) for f in selected)}
        if args.stats:
            print('family\tdomain\toccurrences\tvalues\tlabels\texcluded_occurrences')
            for family in selected:
                for domain in sorted(domains):
                    vals = [(v, common.occurrences(v.text, family)) for d, v in entries.values() if d == domain]
                    vals = [(v, matches) for v, matches in vals if matches]
                    print('\t'.join(map(str, (family, domain, sum(len(m) for _, m in vals), len(vals),
                                             sum(common.guard.label_like(v.text) for v, _ in vals),
                                             sum(len(m) for v, m in vals if common.excluded(v.key))))))
            print(f'runtime_seconds\t{time.monotonic() - started:.3f}', file=sys.stderr)
            return 0
        if args.limit is not None:
            limited, count = {}, 0
            for key, item in entries.items():
                if count >= args.limit:
                    break
                limited[key] = item
                count += sum(len(common.occurrences(item[1].text, f)) for f in selected)
            entries = limited
        en, _, _ = common.load_catalogue(root, 'en')
        if args.format == 'tsv':
            print('\t'.join(FIELDS))
        else:
            print('| ' + ' | '.join(FIELDS) + ' |')
            print('| ' + ' | '.join('---' for _ in FIELDS) + ' |')
        count = 0
        for row in render_rows(entries, all_entries, neighbours, en, selected):
            if args.limit is not None and count >= args.limit:
                break
            cells = [common.escape(x) for x in row]
            print('\t'.join(cells) if args.format == 'tsv' else '| ' + ' | '.join(c.replace('|', '&#124;') for c in cells) + ' |')
            count += 1
        print(f'candidate_occurrences\t{count}; offsets are decoded Unicode code points [start,end); '
              'empty references mean no hits in the guard scanner, not proof of no dependencies', file=sys.stderr)
        return 0
    except (common.Refusal, common.guard.Fail, OSError, ValueError) as e:
        print(f'error: {e}', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
