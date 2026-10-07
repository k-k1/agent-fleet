"""Plan safety, real-guard integration and verification-removal mutants for local B2 tools."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import ja_term_common as common

HERE = Path(__file__).resolve().parent
APPLY = HERE / 'ja-term-apply.py'
CANDIDATES = HERE / 'ja-term-candidates.py'


def entry(text, key='d.login', domain='d'):
    src = 'export const d = {' + json.dumps(key) + ':' + json.dumps(text, ensure_ascii=False) + '};'
    return domain, common.notation.values(src)[0]


def row(old='ログイン', new='サインイン', family='F-login', key='d.login'):
    return common.Row(key, old, new, family, 'Reviewed IdP control and neighbouring keys', 1)


class VerificationTests(unittest.TestCase):
    def test_each_family_accepts_only_declared_changes(self):
        cases = [('F-login', 'ログインします。', 'サインインします。'),
                 ('F-deploy', 'デプロイ既定', '配備の既定'),
                 ('F-deploy', 'デプロイ全体', '配備全体'),
                 ('F-device', 'この端末だけに保存します。', 'このブラウザだけに保存します。'),
                 ('F-device', 'ほかの端末にも表示されます。', 'ほかのブラウザにも表示されます。'),
                 ('F-slot', '週間枠', '週間利用枠'), ('F-slot', '枠', '子の上限'),
                 ('F-fold', '時計で畳まれます。', '時計で停止されます。'),
                 ('F-fold', '畳まれても続きます。', '終了されても続きます。'),
                 ('F-onoff', '既定 ON/OFF', '既定 オン/オフ'),
                 ('F-onoff', 'オンにします。', '有効にします。'),
                 ('F-default', 'デフォルト', '既定'),
                 ('F-variants', 'セッション削除', 'セッションを削除'),
                 ('F-variants', '保存中', '保存中…'),
                 ('F-variants', 'APIトークン', 'API トークン'),
                 ('F-variants', '7日', '7 日'),
                 ('F-variants', '変更ファイルを検索…', '変更ファイルを検索'),
                 ('F-variants', 'ファイルを検索…', 'ファイルを検索'),
                 ('F-variants', 'このセッションが直したファイルを検索…', 'このセッションが直したファイルを検索'),
                 ('F-variants', 'セッションを検索…', 'セッションを検索'),
                 ('F-variants', '過去のセッションの会話を検索…', '過去のセッションの会話を検索')]
        cases += [('F-buttons', n + 'する', n) for n in common.BUTTONS]
        for family, old, new in cases:
            with self.subTest(family=family, old=old):
                r = row(old, new, family)
                self.assertFalse(common.verify_plan([r], {r.key: entry(old)})[r.key][0])
                with self.assertRaises(common.Refusal):
                    common.verify_plan([row(old, new + '追加', family)], {r.key: entry(old)})

    def test_wrong_family_and_free_form_rewrites(self):
        for r in [row('ログイン', '既定'), row('ログインの設定', '設定のサインイン'),
                  row('端末', 'このパソコン', 'F-device'),
                  row('保存します。', '保存…します。', 'F-variants'),
                  row('JSON', 'JSオン', 'F-onoff'),
                  row('ON_FLAG', 'オン_FLAG', 'F-onoff'),
                  row('削除する前に確認。', '削除前に確認。', 'F-buttons'),
                  row('デプロイ既定', '配備既定ではあります', 'F-deploy')]:
            with self.subTest(r=r), self.assertRaises(common.Refusal):
                common.verify_plan([r], {r.key: entry(r.old)})

    def test_opposite_directions_in_one_value_are_rejected(self):
        for family, old, new in [
                ('F-login', 'ログインとサインイン。', 'サインインとログイン。'),
                ('F-deploy', '配備とデプロイ。', 'デプロイと配備。'),
                ('F-onoff', 'オンと有効。', '有効とオン。')]:
            with self.subTest(family=family), self.assertRaisesRegex(common.Refusal, 'opposite directions'):
                common.verify_plan([row(old, new, family)], {'d.login': entry(old)})
        for family, old, new in [
                ('F-login', 'ログインとログイン。', 'サインインとサインイン。'),
                ('F-onoff', 'ON/OFF', 'オン/オフ'),
                ('F-onoff', 'オンとオフ。', '有効と無効。')]:
            with self.subTest(family=family):
                common.verify_plan([row(old, new, family)], {'d.login': entry(old)})

    def test_discovery_only_and_manual_terms(self):
        for stem, old, new in [('畳み', '畳みます。', '停止します。'),
                               ('畳ま', '畳まない。', '停止しない。'),
                               ('畳ん', '畳んじゃいます。', '停止しちゃいます。'),
                               ('畳も', '畳もう。', '停止しよう。')]:
            with self.subTest(stem=stem):
                self.assertEqual(common.occurrences(old, 'F-fold'), [(0, len(stem), stem)])
                with self.assertRaisesRegex(common.Refusal, 'unapproved rewrite'):
                    common.substitution_edits(old, new, 'F-fold')
        self.assertFalse(common.occurrences('ブラウザに保存します。', 'F-device'))
        self.assertEqual(common.occurrences('このブラウザに保存します。', 'F-device')[0][2], 'このブラウザ')
        common.substitution_edits('ほかの端末。', 'ほかのブラウザ。', 'F-device')
        with self.assertRaisesRegex(common.Refusal, 'unapproved rewrite'):
            common.substitution_edits('ブラウザに保存します。', 'このブラウザに保存します。', 'F-device')

    def test_current_and_missing_key(self):
        with self.assertRaisesRegex(common.Refusal, 'byte for byte'):
            common.verify_plan([row()], {'d.login': entry('ログイン ')})
        with self.assertRaisesRegex(common.Refusal, 'absent'):
            common.verify_plan([row()], {})
        self.assertFalse(common.verify_plan([row()], {'d.login': entry('ログイン')})['d.login'][0])
        self.assertTrue(common.verify_plan([row()], {'d.login': entry('サインイン')})['d.login'][0])

    def test_protected_changes(self):
        for old, new in [('「ログイン」', '「サインイン」'), ('`ログイン`', '`サインイン`'),
                         ('{ログイン}', '{サインイン}'), ('<0>ログイン</0>', '<0>サインイン</0>')]:
            with self.subTest(old=old), self.assertRaisesRegex(common.Refusal, 'protected'):
                common.verify_plan([row(old, new)], {'d.login': entry(old)})
        good = row('{name} のログイン 12。\n<0/>と `code` と「固定」です。',
                   '{name} のサインイン 12。\n<0/>と `code` と「固定」です。')
        common.verify_plan([good], {'d.login': entry(good.old)})

    def test_invariants_have_independent_negative_controls(self):
        for old, new, reason in [('{n}', '{m}', 'placeholders'), ('<0/>', '<1/>', 'slots'),
                                 ('12', '13', 'digits'), ('`a`', '`b`', 'code'),
                                 ('「a」', '「b」', 'kagi'), ('a\nb', 'ab', 'newlines')]:
            with self.subTest(reason=reason), self.assertRaisesRegex(common.Refusal, reason):
                common.verify_protections(row(old, new), [])

    def test_excluded_keys(self):
        for key in ['plan.review_prompt_reply', 'wi.prompt_read', 'launch.first_prompt_note',
                    'notif.speech', 'notif.failed_speech_bare', 'notif.speech.future',
                    'err.auth', 'chat.report.lang', 'clean.reason.blocked', 'clean.reason_future']:
            with self.subTest(key=key), self.assertRaisesRegex(common.Refusal, 'excluded'):
                common.verify_plan([row(key=key)], {key: entry('ログイン', key)})

    def test_shared_labels(self):
        entries = {'d.login': entry('ログイン'), 'e.login': entry('ログイン', 'e.login', 'e')}
        with self.assertRaisesRegex(common.Refusal, 'SPLIT.*e.login'):
            common.verify_plan([row()], entries)
        with self.assertRaisesRegex(common.Refusal, 'SPLIT'):
            common.verify_plan([row('枠', '利用枠', 'F-slot'), row('枠', '子の上限', 'F-slot', 'e.login')],
                               {'d.login': entry('枠'), 'e.login': entry('枠', 'e.login', 'e')})
        common.verify_plan([row(), row(key='e.login')], entries)
        applied = {'d.login': entry('サインイン'), 'e.login': entry('サインイン', 'e.login', 'e')}
        self.assertTrue(all(a for a, _ in common.verify_plan([row(), row(key='e.login')], applied).values()))
        # A prose value is independent even if another key shares the same sentence.
        old, new = 'ログインしてください。', 'サインインしてください。'
        common.verify_plan([row(old, new)], {'d.login': entry(old), 'e.login': entry(old, 'e.login')})

    def test_duplicate_plan(self):
        with self.assertRaisesRegex(common.Refusal, 'duplicate'):
            common.verify_plan([row(), row()], {'d.login': entry('ログイン')})

    def test_reviewed_sense_approvals_are_complete_and_scoped(self):
        entries = {'d.login': entry('ログイン'), 'e.login': entry('ログイン', 'e.login', 'e')}
        common.verify_plan([row()], entries, split_approvals={'d.login', 'e.login'})
        self.assertTrue(common.verify_plan([row()], {'d.login': entry('サインイン'), 'e.login': entries['e.login']},
                                         split_approvals={'d.login', 'e.login'})['d.login'][0])
        for keys in [{'d.login'}, {'d.login', 'e.login', 'absent'}]:
            with self.subTest(keys=keys), self.assertRaises(common.Refusal):
                common.verify_plan([row()], entries, split_approvals=keys)
        r = row(key='err.auth')
        common.verify_plan([r], {r.key: entry(r.old, r.key)}, user_errors={r.key})
        for key in ['err.prompt', 'err.speech', 'd.login', 'absent']:
            with self.subTest(key=key), self.assertRaises(common.Refusal):
                common.verify_plan([row(key=key)], {key: entry('ログイン', key)}, user_errors={key})
        # Reviewed user-visible err.* values: F-login, F-deploy and F-onoff only, per key.
        for old, new, family in [('このデプロイで有効', 'この配備で有効', 'F-deploy'),
                                 ('AI 提案が無効です', 'AI 提案がオフです', 'F-onoff')]:
            r = row(old, new, family, 'err.reviewed')
            with self.subTest(old=old):
                self.assertFalse(common.verify_plan([r], {r.key: entry(old, r.key)}, user_errors={r.key})[r.key][0])
                with self.assertRaisesRegex(common.Refusal, 'excluded'):
                    common.verify_plan([r], {r.key: entry(old, r.key)})
                for key in ['err.prompt_x', 'err.speech', 'chat.report.x', 'clean.reason.x', 'd.deploy']:
                    other = row(old, new, family, key)
                    with self.assertRaises(common.Refusal):
                        common.verify_plan([other], {key: entry(old, key)}, user_errors={key})
                with self.assertRaisesRegex(common.Refusal, 'user-error approval requires'):
                    common.verify_plan([r], {r.key: entry(old, r.key)}, user_errors={r.key, 'err.not_in_plan'})
                # Only declared substitutions pass, even with the approval.
                bad = row(old, new + '追加', family, 'err.reviewed')
                with self.assertRaises(common.Refusal):
                    common.verify_plan([bad], {bad.key: entry(old, bad.key)}, user_errors={bad.key})
        for old, new, family in [('デフォルト', '既定', 'F-default'), ('端末', 'ブラウザ', 'F-device'),
                                 ('保存中', '保存中…', 'F-variants'), ('枠', '利用枠', 'F-slot')]:
            r = row(old, new, family, 'err.reviewed')
            with self.subTest(family=family), self.assertRaisesRegex(common.Refusal, 'user-error approval requires'):
                common.verify_plan([r], {r.key: entry(old, r.key)}, user_errors={r.key})
        # Quoted-term approval stays F-login only.
        r = row('「デプロイ」', '「配備」', 'F-deploy', 'd.quote')
        with self.assertRaisesRegex(common.Refusal, 'F-login'):
            common.verify_plan([r], {r.key: entry(r.old, r.key)}, quoted_terms={r.key})
        r = row('「ログインして承認」を押します。', '「サインインして承認」を押します。')
        common.verify_plan([r], {r.key: entry(r.old)}, quoted_terms={r.key})
        for old, new, family in [('`ログイン`', '`サインイン`', 'F-login'),
                                 ('<0>ログイン</0>', '<0>サインイン</0>', 'F-login'),
                                 ('「ログイン」 12', '「サインイン」 13', 'F-login'),
                                 ('「ログイン」', '「サインイン追加」', 'F-login'),
                                 ('「デフォルト」', '「既定」', 'F-default'),
                                 ('ログイン', 'サインイン', 'F-login')]:
            with self.subTest(old=old), self.assertRaises(common.Refusal):
                common.verify_plan([row(old, new, family)], {'d.login': entry(old)}, quoted_terms={'d.login'})

    def test_source_offsets_and_unsafe_expressions(self):
        src = 'export const d = {"d.login": "ログイン\\n" + "文字列"};\r\n'
        v = common.notation.values(src)[0]
        edits = common.source_edits(v, [(0, 4, 'サインイン')])
        a, b, text = edits[0]
        self.assertEqual(src[:a] + text + src[b:], src.replace('ログイン', 'サインイン'))
        for src in ['export const d = {"x": "a", "x": "b"};',
                    'export const d = {"x": fn()};', 'export const d = {"x": `x${n}`};']:
            with self.assertRaises(common.Refusal):
                common.notation.values(src)
        for src in ['export const d = {"x": "ロ" + "グイン"};',
                    'export const d = {"x": "\\u30edグイン"};']:
            with self.assertRaisesRegex(common.Refusal, 'escape|boundary'):
                common.source_edits(common.notation.values(src)[0], [(0, 4, 'サインイン')])

    def test_variant_end_insertion(self):
        v = entry('保存中')[1]
        self.assertEqual(common.source_edits(v, common.substitution_edits('保存中', '保存中…', 'F-variants'))[0][2], '…')

    def test_english_type_only_wrapper_and_latin_occurrences(self):
        source = 'import type { d as jaD } from "../ja/d.ts"; export const d: Record<keyof typeof jaD, string> = {"d.login": "Sign in"};'
        self.assertEqual(common.english_values(source)[0].text, 'Sign in')
        for bad in [source.replace('import type', 'import'), source.replace('Record', 'Unknown'),
                    source.replace('export const', 'unsafe const'), source[:source.index(' = {') + 2]]:
            with self.assertRaises(common.Refusal):
                common.english_values(bad)
        self.assertFalse(common.occurrences('JSON XON ON_FLAG', 'F-onoff'))
        self.assertEqual([m[2] for m in common.occurrences('ONにするか OFF/OFF', 'F-onoff')], ['ON', 'OFF', 'OFF'])


class MutantTests(unittest.TestCase):
    def test_each_verification_removal_breaks_its_negative_control(self):
        mutants = [('verify_edit_directions', lambda o, e, f: None, 'test_opposite_directions_in_one_value_are_rejected'),
                   ('verify_current', lambda r, e: False, 'test_current_and_missing_key'),
                   ('substitution_edits', lambda o, n, f: [], 'test_wrong_family_and_free_form_rewrites'),
                   ('verify_protections', lambda r, e: None, 'test_protected_changes'),
                   ('verify_excluded', lambda r: None, 'test_excluded_keys'),
                   ('verify_split', lambda r, e: None, 'test_shared_labels')]
        for name, replacement, test in mutants:
            with self.subTest(name=name), patch.object(common, name, replacement):
                result = unittest.TestResult()
                VerificationTests(test).run(result)
                self.assertTrue(result.failures, f'{name} mutant survived: {result.errors}')
        # Widening the user-error family list must break the scoped-approval negative control.
        with patch.object(common, 'USER_ERROR_FAMILIES', (*common.PAIRS,)):
            result = unittest.TestResult()
            VerificationTests('test_reviewed_sense_approvals_are_complete_and_scoped').run(result)
            self.assertTrue(result.failures, f'family-scope mutant survived: {result.errors}')
        source = Path(common.__file__).read_text()
        marker = 'if len({r.key for r in rows}) != len(rows):'
        self.assertEqual(source.count(marker), 1)
        mutated = source.replace(marker, 'if False:', 1)
        namespace = {'__file__': common.__file__, '__name__': 'term_mutant'}
        # Reuse the real imports/dataclass definitions; only the validator is replaced.
        start, end = mutated.index('def verify_plan('), mutated.index('\ndef source_edits(')
        exec(compile(mutated[start:end], common.__file__, 'exec'), common.__dict__, namespace)
        with patch.object(common, 'verify_plan', namespace['verify_plan']):
            result = unittest.TestResult()
            VerificationTests('test_duplicate_plan').run(result)
            self.assertTrue(result.failures, f'duplicate mutant survived: {result.errors}')


@unittest.skipUnless(shutil.which('git'), 'git required')
class CliTests(unittest.TestCase):
    def setUp(self):
        base = Path(os.environ.get('AF_WORK_DIR') or Path.home() / '.af-work' / HERE.parent.name)
        base.mkdir(parents=True, exist_ok=True)
        self.tmp = tempfile.TemporaryDirectory(dir=base)
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        self.ja = self.repo / common.JA
        self.ja.mkdir(parents=True)
        self.en = self.ja.parent / 'en'
        self.en.mkdir()
        self.write('d', {'d.title': '認証設定', 'd.login': 'ログイン', 'd.after': 'IdP を選ぶ。',
                         'd.device': 'この端末だけに保存します。', 'd.fold': '時計で畳まれます。',
                         'd.onoff': '既定 ON/OFF', 'd.default': 'デフォルト', 'd.variant': '保存中'})
        self.write('d', {'d.title': 'Auth settings', 'd.login': 'Sign in', 'd.after': 'Choose IdP.'}, self.en)
        glossary = self.repo / common.guard.GLOSSARY
        glossary.parent.mkdir(parents=True)
        glossary.write_text('| 画面 | 意味 |\n|---|---|\n| サインイン | IdP |\n| ログイン | External |\n'
                            '| このブラウザ | Local |\n| 既定 | Default |\n| デプロイ（する） | Verb |\n| 配備 | Noun |\n')
        (self.repo / 'guide/ref/example.ja.md').write_text('「ログイン」を選びます。\n')
        test = self.repo / 'console/sample.test.ts'
        test.write_text('expect(label).toBe("ログイン");\n')
        ui = self.repo / 'console/src/Auth.tsx'
        ui.parent.mkdir(parents=True, exist_ok=True)
        ui.write_text('const label = tr("d.login");\n')
        self.git('init', '-q')
        self.commit()
        self.plan = self.repo / 'plan.tsv'
        self.plan_rows([row('この端末だけに保存します。', 'このブラウザだけに保存します。', 'F-device', 'd.device')])

    def write(self, domain, values, directory=None):
        source = '// Preserve this comment\nexport const ' + domain + ' = {\n' + ''.join(
            '  ' + json.dumps(k) + ': ' + json.dumps(v, ensure_ascii=False) + ',\n' for k, v in values.items()) + '};\n'
        ((directory or self.ja) / (domain + '.ts')).write_text(source, encoding='utf-8')

    def git(self, *args):
        return subprocess.run(['git', *args], cwd=self.repo, check=True, capture_output=True)

    def commit(self):
        self.git('add', '-A')
        self.git('-c', 'core.hooksPath=/dev/null', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                 'commit', '-qm', 'fixture')

    def cli(self, *args, script=APPLY):
        return subprocess.run([sys.executable, str(script), *args], cwd=self.repo, capture_output=True, text=True)

    def apply(self, *args):
        return self.cli('--plan', str(self.plan), *args)

    def guard(self, *args):
        return self.cli('HEAD', *args, script=HERE / 'catalog-diff-check.py')

    def plan_rows(self, rows):
        self.plan.write_text('# Reviewed B2 decisions\n' + ''.join('\t'.join(common.escape(x) for x in
                             (r.key, r.old, r.new, r.family, r.reason)) + '\n' for r in rows))

    def test_dry_run_check_only_and_apply_idempotence(self):
        before = (self.ja / 'd.ts').read_bytes()
        for flag in ['--dry-run', '--check-only']:
            p = self.apply(flag)
            self.assertEqual(p.returncode, 0, p.stderr)
            self.assertIn('1 verified', p.stdout)
            self.assertEqual(before, (self.ja / 'd.ts').read_bytes())
        terms, report = self.repo / 'allow.tsv', self.repo / 'report.txt'
        p = self.apply('--apply', '--allow-terms-out', str(terms), '--report', str(report))
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(report.read_text(), p.stdout)
        self.assertIn('cd console && npm test -- --maxWorkers=2', p.stdout)
        self.assertIn('update flag', p.stdout)
        after = (self.ja / 'd.ts').read_bytes()
        self.assertNotEqual(before, after)
        self.assertEqual(common.guard.skeleton(before.decode()), common.guard.skeleton(after.decode()))
        second = self.apply('--apply')
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertIn('1 already applied; 0 pending', second.stdout)
        self.assertEqual(after, (self.ja / 'd.ts').read_bytes())
        self.assertTrue(terms.read_text())
        # After committing the edits, repeated plan verification remains a no-op.
        self.commit()
        self.assertEqual(self.apply('--apply').returncode, 0)

    def test_dirty_file_refusal_and_force(self):
        path = self.ja / 'd.ts'
        path.write_text(path.read_text() + '\n')
        before = path.read_bytes()
        terms = self.repo / 'allow.tsv'
        p = self.apply('--apply', '--allow-terms-out', str(terms))
        self.assertEqual(p.returncode, 2)
        self.assertIn('dirty catalogue', p.stderr)
        self.assertFalse(terms.exists())
        self.assertEqual(before, path.read_bytes())
        self.assertEqual(self.apply('--apply', '--force', '--allow-terms-out', str(terms)).returncode, 0)

    @unittest.skipUnless(shutil.which('node'), 'real guard requires node')
    def test_reviewed_quotes_preserve_guard_negative_controls(self):
        self.write('d', {'d.quote': '「サインインして承認」を押してください。',
                         'err.auth': 'サインインを開始できませんでした'})
        self.commit()
        self.plan_rows([row('「サインインして承認」を押してください。', '「ログインして承認」を押してください。',
                            key='d.quote'),
                        row('サインインを開始できませんでした', 'ログインを開始できませんでした', key='err.auth')])
        before = (self.ja / 'd.ts').read_bytes()
        terms = self.repo / 'allow.tsv'
        self.assertEqual(self.apply('--check-only').returncode, 2)
        self.assertEqual(before, (self.ja / 'd.ts').read_bytes())
        flags = ['--allow-user-error', 'err.auth', '--allow-quoted-terms', 'd.quote']
        p = self.apply('--apply', '--allow-terms-out', str(terms), *flags)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.apply('--check-only', *flags).returncode, 0)
        negative = self.guard('--allow-labels', '--allow-terms-file', str(terms))
        self.assertEqual(negative.returncode, 1, negative.stdout)
        self.assertIn('FAIL kagi', negative.stdout)
        positive = self.guard('--allow-labels', '--allow-terms-file', str(terms), '--allow-quoted-terms', 'd.quote')
        self.assertEqual(positive.returncode, 0, positive.stdout + positive.stderr)
        for extra in [['--allow-quoted-terms', 'absent'], ['--allow-quoted-terms', 'err.auth']]:
            bad = self.guard('--allow-labels', '--allow-terms-file', str(terms), '--allow-quoted-terms', 'd.quote', *extra)
            self.assertEqual(bad.returncode, 1, bad.stdout)
            self.assertIn('stale quoted-term approval', bad.stdout)
        path = self.ja / 'd.ts'
        path.write_text(path.read_text().replace('ログインして承認', 'ログインして削除'))
        bad = self.guard('--allow-labels', '--allow-terms-file', str(terms), '--allow-quoted-terms', 'd.quote')
        self.assertEqual(bad.returncode, 1, bad.stdout)
        self.assertIn('FAIL kagi', bad.stdout)

    @unittest.skipUnless(shutil.which('node'), 'real guard requires node')
    def test_quoted_guard_rejects_excluded_keys(self):
        old = '「サインインして承認」を押してください。'
        new = '「ログインして承認」を押してください。'
        keys = ['d.quote', 'd.prompt', 'd.Prompt', 'd.speech', 'd_speech', 'notif.speech_bare',
                'chat.report.auth', 'clean.reason.auth']
        self.write('d', dict.fromkeys(keys, old))
        self.commit()
        for key in keys:
            with self.subTest(key=key):
                values = dict.fromkeys(keys, old)
                values[key] = new
                self.write('d', values)
                p = self.guard('--allow-labels', '--allow-term', f'{key}:サインイン>ログイン',
                               '--allow-quoted-terms', key)
                self.assertEqual(p.returncode, 0 if key == 'd.quote' else 2, p.stdout + p.stderr)
                if key != 'd.quote':
                    self.assertIn('excluded prompt/speech/report/reason key', p.stderr)

    def test_reviewed_split_cli_rejects_incomplete_and_stale_approvals(self):
        self.write('e', {'e.login': 'ログイン'})
        self.commit()
        self.plan_rows([row()])
        for spec in ['d.login', 'd.login,e.login,absent', 'd.login,']:
            self.assertEqual(self.apply('--check-only', '--allow-split', spec).returncode, 2)
        p = self.apply('--check-only', '--allow-split', 'd.login,e.login')
        self.assertEqual(p.returncode, 0, p.stderr)

    @unittest.skipUnless(shutil.which('node'), 'real guard requires node')
    def test_real_guard_rejects_without_and_accepts_emitted_allowances(self):
        rows = [row('この端末だけに保存します。', 'このブラウザだけに保存します。', 'F-device', 'd.device'),
                row('既定 ON/OFF', '既定 オン/オフ', 'F-onoff', 'd.onoff'),
                row('デフォルト', '既定', 'F-default', 'd.default'),
                row('保存中', '保存中…', 'F-variants', 'd.variant')]
        self.plan_rows(rows)
        terms = self.repo / 'allow.tsv'
        p = self.apply('--apply', '--allow-terms-out', str(terms))
        self.assertEqual(p.returncode, 0, p.stderr)
        negative = self.guard('--allow-labels')
        self.assertEqual(negative.returncode, 1, negative.stdout + negative.stderr)
        self.assertIn('FAIL latin:', negative.stdout)
        self.assertIn('FAIL glossary:', negative.stdout)
        positive = self.guard('--allow-labels', '--allow-terms-file', str(terms))
        self.assertEqual(positive.returncode, 0, positive.stdout + positive.stderr)
        self.assertIn('4 changed', positive.stdout)
        self.assertIn('failures:', positive.stdout)
        self.assertIn('ALLOWED term:', positive.stdout)

    @unittest.skipUnless(shutil.which('node'), 'real guard requires node')
    def test_slot_allowance_embedded_old_term_and_exact_count(self):
        glossary = self.repo / common.guard.GLOSSARY
        glossary.write_text(glossary.read_text() + '| 利用枠 | Quota |\n| 子の上限 | Child cap |\n')
        self.write('d', {'d.slot': '枠', 'd.slot_sentence': '利用枠と週間枠です。'})
        self.commit()
        self.plan_rows([row('枠', '利用枠', 'F-slot', 'd.slot'),
                        row('利用枠と週間枠です。', '利用枠と週間利用枠です。', 'F-slot', 'd.slot_sentence')])
        terms = self.repo / 'slot-allow.tsv'
        p = self.apply('--apply', '--allow-terms-out', str(terms))
        self.assertEqual(p.returncode, 0, p.stderr)
        negative = self.guard('--allow-labels')
        self.assertEqual(negative.returncode, 1, negative.stdout + negative.stderr)
        self.assertIn('FAIL glossary:', negative.stdout)
        positive = self.guard('--allow-labels', '--allow-terms-file', str(terms))
        self.assertEqual(positive.returncode, 0, positive.stdout + positive.stderr)
        self.assertIn('2 of 2 term allowance(s) applied', positive.stdout)
        terms.write_text(terms.read_text().replace('\t1\n', '\t2\n'))
        wrong = self.guard('--allow-labels', '--allow-terms-file', str(terms))
        self.assertEqual(wrong.returncode, 1, wrong.stdout + wrong.stderr)
        self.assertIn('FAIL allow:', wrong.stdout)

    def test_all_or_nothing_multiple_domains_and_invalid_late_row(self):
        self.write('e', {'e.login': 'ログインします。'})
        self.commit()
        original = {p: p.read_bytes() for p in self.ja.glob('*.ts')}
        rows = [row('この端末だけに保存します。', 'このブラウザだけに保存します。', 'F-device', 'd.device'),
                row('ログインします。', 'サインインしてください。', 'F-login', 'e.login')]
        self.plan_rows(rows)
        terms = self.repo / 'allow.tsv'
        p = self.apply('--apply', '--allow-terms-out', str(terms))
        self.assertEqual(p.returncode, 2)
        self.assertIn('unapproved rewrite', p.stderr)
        self.assertFalse(terms.exists())
        self.assertEqual(original, {p: p.read_bytes() for p in original})

    def test_duplicate_keys_in_files_or_plan_refused(self):
        self.plan_rows([row(), row()])
        self.assertIn('duplicate/conflicting plan key', self.apply('--check-only').stderr)
        self.plan_rows([row()])
        self.write('e', {'d.login': 'ログイン'})
        p = self.apply('--check-only')
        self.assertEqual(p.returncode, 2)
        self.assertIn('duplicate key across domains', p.stderr)

    def test_split_in_other_domain_and_incomplete_reapply(self):
        self.write('e', {'e.login': 'ログイン'})
        self.commit()
        self.plan_rows([row()])
        self.assertIn('SPLIT', self.apply('--check-only').stderr)
        self.plan_rows([row(), row(key='e.login')])
        terms = self.repo / 'allow.tsv'
        self.assertEqual(self.apply('--apply', '--allow-terms-out', str(terms)).returncode, 0)
        self.assertEqual(self.apply('--apply').returncode, 0)
        # A newly introduced old label cannot be omitted on reapply.
        self.write('f', {'f.login': 'ログイン'})
        self.assertIn('SPLIT', self.apply('--apply').stderr)

    def test_swap_and_bom_refusals_before_any_write(self):
        self.write('d', {'d.swap': 'ログインとサインイン。'})
        self.commit()
        self.plan_rows([row('ログインとサインイン。', 'サインインとログイン。', 'F-login', 'd.swap')])
        before = (self.ja / 'd.ts').read_bytes()
        terms, report = self.repo / 'allow.tsv', self.repo / 'report.txt'
        for flag, extra in [('--check-only', []),
                            ('--apply', ['--allow-terms-out', str(terms), '--report', str(report)])]:
            with self.subTest(flag=flag):
                p = self.apply(flag, *extra)
                self.assertEqual(p.returncode, 2, p.stdout)
                self.assertIn('d.swap', p.stderr)
                self.assertIn('opposite directions', p.stderr)
                self.assertEqual((self.ja / 'd.ts').read_bytes(), before)
                self.assertFalse(terms.exists())
                self.assertFalse(report.exists())
        self.plan.write_text('\ufeff' + self.plan.read_text(), encoding='utf-8')
        p = self.apply('--check-only')
        self.assertEqual(p.returncode, 2)
        self.assertIn('UTF-8 BOM', p.stderr)
        self.assertNotIn('key is absent', p.stderr)
        self.assertEqual((self.ja / 'd.ts').read_bytes(), before)

    def test_newlines_backslashes_and_tsv_errors(self):
        old, new = 'ログイン\n`C:\\dir`\tします。', 'サインイン\n`C:\\dir`\tします。'
        self.write('d', {'d.login': old})
        self.commit()
        self.plan_rows([row(old, new)])
        self.assertEqual(common.read_plan(self.plan)[0].old, old)
        self.assertEqual(self.apply('--check-only').returncode, 0)
        for text in ['a\tb\tc\tF-login\n', 'a\tb\tc\tF-unknown\tr\n',
                     'a\tb\\q\tc\tF-login\tr\n', '# empty\n']:
            self.plan.write_text(text)
            self.assertEqual(self.apply('--check-only').returncode, 2)

    def test_artifact_and_symlink_safety(self):
        before = (self.ja / 'd.ts').read_bytes()
        bad = [self.plan, self.ja / 'd.ts', self.repo / '.git/config', self.repo / 'guide/ref/example.ja.md']
        link = self.repo / 'out-link.tsv'
        link.symlink_to(self.repo / 'new.tsv')
        bad.append(link)
        for target in bad:
            with self.subTest(target=target):
                p = self.apply('--apply', '--allow-terms-out', str(target))
                self.assertEqual(p.returncode, 2)
                self.assertEqual(before, (self.ja / 'd.ts').read_bytes())
        target = self.repo / 'same.txt'
        self.assertEqual(self.apply('--apply', '--allow-terms-out', str(target), '--report', str(target)).returncode, 2)
        self.assertFalse(target.exists())
        self.assertEqual(self.apply('--apply').returncode, 2)
        self.assertEqual(self.apply('--check-only', '--report', str(target)).returncode, 2)
        path = self.ja / 'd.ts'
        real = self.repo / 'catalogue.ts'
        path.rename(real)
        path.symlink_to(real)
        self.assertIn('symlink catalogue', self.apply('--check-only').stderr)

    def test_candidates_shape_context_determinism_and_stats(self):
        a = self.cli('--family', 'F-login', script=CANDIDATES)
        b = self.cli('--family', 'F-login', script=CANDIDATES)
        self.assertEqual(a.returncode, 0, a.stderr)
        self.assertEqual(a.stdout, b.stdout)
        rows = [line.split('\t') for line in a.stdout.splitlines()]
        header, data = rows[0], rows[1]
        self.assertEqual(len(header), len(data))
        record = dict(zip(header, data))
        self.assertEqual(record['matched_term'], 'ログイン')
        self.assertEqual((record['start'], record['end']), ('0', '4'))
        self.assertEqual(record['previous_key'], 'd.title')
        self.assertEqual(record['next_key'], 'd.after')
        self.assertEqual(record['en_value'], 'Sign in')
        self.assertEqual(record['guide_count'], '2')
        self.assertIn('guide/ref/example.ja.md:1:', record['guide_first_two'])
        self.assertIn('console/sample.test.ts:1', record['tests_hits'])
        self.assertIn('console/src/Auth.tsx:1', record['ui_hits'])
        self.assertEqual(self.cli('--family', 'F-login', '--limit', '0', script=CANDIDATES).stdout.count('\n'), 1)
        md = self.cli('--family', 'F-login', '--format', 'md', script=CANDIDATES)
        self.assertIn('| family | key |', md.stdout)
        stats = self.cli('--family', 'F-login', '--stats', script=CANDIDATES)
        self.assertEqual(stats.returncode, 0, stats.stderr)
        self.assertIn('F-login\td\t1\t1\t1\t0', stats.stdout)
        self.assertIn('runtime_seconds', stats.stderr)
        self.assertEqual(self.cli('--family', 'F-bogus', script=CANDIDATES).returncode, 2)
        self.assertEqual(self.cli('--domain', 'missing', script=CANDIDATES).returncode, 2)


if __name__ == '__main__':
    unittest.main()
