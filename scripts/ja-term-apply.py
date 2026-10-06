#!/usr/bin/env python3
"""Validate a reviewed B2 TSV plan before mechanically editing Japanese literals."""
import argparse
import collections
from pathlib import Path
import shlex
import sys

import ja_term_common as common


def allowances(row, edits, terms):
    """Emit net guard units, rather than compounds the guard does not count."""
    known = set(terms) | set(common.guard.INVARIANTS['latin'].findall(row.old + ' ' + row.new))
    removed = {t: common.guard.item_count(False, (common.guard.item_domains(False, t, terms) or [None])[0], t, row.old, terms) -
               common.guard.item_count(False, (common.guard.item_domains(False, t, terms) or [None])[0], t, row.new, terms)
               for t in known}
    removed = {t: n for t, n in removed.items() if n}
    rows = collections.Counter()
    for start, end, replacement in edits:
        before = row.old[start:end]
        olds = sorted(t for t, n in removed.items() if n > 0 and t in before)
        news = sorted(t for t, n in removed.items() if n < 0 and t in replacement)
        if not olds and not news:
            continue
        if len(olds) > 1 or len(news) > 1:
            raise common.Refusal(f'{row.key}: overlapping guard units require a separately reviewed family pair')
        a, b = olds[0] if olds else before, news[0] if news else replacement
        if not a or not b:
            raise common.Refusal(f'{row.key}: cannot express guard drift with a nonempty allowance pair')
        rows[a, b] += 1
    out = [(row.key, a, b, n) for (a, b), n in sorted(rows.items())]
    # Validate with the guard's category/count semantics before publishing approvals.
    old_terms, new_terms = {a for _, a, _, _ in out}, {b for _, _, b, _ in out}
    if old_terms & new_terms:
        raise common.Refusal(f'{row.key}: reversed/chained allowances; separate the term decisions')
    wants = collections.Counter()
    adj = collections.defaultdict(collections.Counter)
    for _, a, b, n in out:
        for term, sign in ((a, -1), (b, 1)):
            for category in common.guard.item_domains(False, term, terms) or [None]:
                wants[category, term] += sign * n
                if category:
                    adj[category][term] += sign * n
    for (category, term), n in wants.items():
        counted_units = {t for d, t in wants if d is not None}
        if common.guard.allowance_count(False, category, term, row.new, terms, counted_units) - \
                common.guard.allowance_count(False, category, term, row.old, terms, counted_units) != n:
            raise common.Refusal(f'{row.key}: allowance cannot explain net {category or "direct"} count of {term!r}')
    for category, counted in (
        ('glossary', lambda text: collections.Counter({t: text.count(t) for t in terms})),
        ('latin', lambda text: collections.Counter(common.guard.INVARIANTS['latin'].findall(text))),
    ):
        a, b = counted(row.old), counted(row.new)
        if any(b[t] - a[t] != adj[category][t] for t in set(a) | set(b) | set(adj[category])):
            raise common.Refusal(f'{row.key}: unexplained guard {category} drift')
    return out


def outputs_check(root, paths, plan):
    directories = {(root / '.git').resolve(),
                   Path(common.notation.git(root, 'rev-parse', '--absolute-git-dir').strip()).resolve(),
                   (root / common.notation.git(root, 'rev-parse', '--git-common-dir').strip()).resolve()}
    resolved = [p.resolve() for p in paths]
    if len(set(resolved)) != len(resolved):
        raise common.Refusal('output paths must be distinct')
    tracked = {root / name for name in common.notation.git(root, 'ls-files', '-z').split('\0') if name}
    for path, target in zip(paths, resolved):
        if path.is_symlink() or target.exists() or not target.parent.is_dir() or target == plan.resolve() or \
                target in tracked or any(target.is_relative_to(d) for d in directories) or \
                target.is_relative_to(root / common.JA.parent) or target.is_relative_to(root / 'scripts'):
            raise common.Refusal(f'unsafe/existing output: {path}; use a fresh artifact path outside source and git metadata')


def follow_up(path):
    command = 'python3 scripts/catalog-diff-check.py origin/develop --allow-labels --allow-terms-file ' + \
        (shlex.quote(str(path)) if path else '<emitted-allowances.tsv>')
    return ['FOLLOW-UP ' + command, 'FOLLOW-UP ' + command + ' --list-citations',
            'FOLLOW-UP ' + command + ' --rewrite-guide',
            'Resolve remaining citations and rerun the guard to exit 0.',
            'FOLLOW-UP (cd console && npm test -- --maxWorkers=2)',
            'FOLLOW-UP python3 scripts/docs-check.py',
            'WARNING: Regenerate goldens only with the project\'s update flag (inspect the owning test).',
            'Full npm test is mandatory: assembled strings, goldens, console-e2e and Go twins can be invisible to PINNED.',
            'Review console-e2e and Go references; run their relevant tests when citations change.',
            'Read the real test summary line and exit status before claiming a pass.']


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--plan', type=Path, required=True)
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument('--dry-run', action='store_true')
    mode.add_argument('--apply', action='store_true')
    mode.add_argument('--check-only', action='store_true', help='review verification only; no artifact or catalogue writes')
    ap.add_argument('--force', action='store_true', help='apply to reviewed dirty catalogue files')
    ap.add_argument('--allow-terms-out', type=Path)
    ap.add_argument('--report', type=Path)
    args = ap.parse_args(argv)
    if args.force and not args.apply:
        ap.error('--force requires --apply')
    if args.check_only and (args.allow_terms_out or args.report):
        ap.error('--check-only does not write artifacts')
    try:
        root = common.root_dir()
        rows = common.read_plan(args.plan)
        entries, sources, _ = common.load_catalogue(root)
        verified = common.verify_plan(rows, entries)
        terms = common.guard.glossary_terms(common.notation.read_source(root / common.guard.GLOSSARY))
        allowed = []
        replacements = collections.defaultdict(list)
        for row in rows:
            applied, edits = verified[row.key]
            allowed.extend(allowances(row, edits, terms))
            if not applied:
                domain, value = entries[row.key]
                replacements[domain].extend(common.source_edits(value, edits))
        rewritten = {}
        for domain, edits in replacements.items():
            source = sources[domain]
            for a, b, replacement in sorted(edits, reverse=True):
                source = source[:a] + replacement + source[b:]
            expected = {k: v.text for k, (d, v) in entries.items() if d == domain}
            expected.update({r.key: r.new for r in rows if entries[r.key][0] == domain})
            if {v.key: v.text for v in common.notation.values(source)} != expected or \
                    common.guard.skeleton(source) != common.guard.skeleton(sources[domain]):
                raise common.Refusal(f'{domain}: edited source does not match plan or changed skeleton')
            rewritten[domain] = source
        output_paths = [p for p in (args.allow_terms_out, args.report) if p is not None]
        outputs_check(root, output_paths, args.plan)
        if args.apply and rewritten and args.allow_terms_out is None:
            raise common.Refusal('--apply requires --allow-terms-out PATH (retain allowances for review)')
        if args.apply:
            for domain in rewritten:
                if not args.force and common.notation.git(root, 'status', '--porcelain', '--', str(common.JA / (domain + '.ts'))).strip():
                    raise common.Refusal(f'dirty catalogue file: {domain}.ts; review before --force')
        for domain, source in sources.items():
            if common.notation.read_source(root / common.JA / (domain + '.ts')) != source:
                raise common.Refusal('catalogue changed during verification; rerun')
        lines = ['CHECK ONLY' if args.check_only else 'APPLY' if args.apply else 'DRY RUN',
                 f'plan: {len(rows)} verified; {sum(v[0] for v in verified.values())} already applied; '
                 f'{sum(not v[0] for v in verified.values())} pending; allowances={len(allowed)}']
        lines.extend('\t'.join(common.escape(x) for x in (r.key, r.old, r.new, r.family, r.reason)) for r in rows)
        lines.extend(follow_up(args.allow_terms_out))
        report = '\n'.join(lines) + '\n'
        # Exclusive artifacts are created before edits so artifact failures cannot mutate catalogues.
        if args.allow_terms_out:
            with args.allow_terms_out.open('x', encoding='utf-8', newline='') as fh:
                fh.write(''.join('\t'.join(map(str, row)) + '\n' for row in allowed))
        if args.report:
            with args.report.open('x', encoding='utf-8', newline='') as fh:
                fh.write(report)
        if args.apply:
            for domain, source in rewritten.items():
                (root / common.JA / (domain + '.ts')).write_bytes(source.encode('utf-8'))
        print(report, end='')
        return 0
    except (common.Refusal, common.guard.Fail, OSError, ValueError) as e:
        print(f'error: {e}', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
