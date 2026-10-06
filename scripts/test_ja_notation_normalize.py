"""Fixtures and exclusion-removal mutants for the local notation normalizer.

Run: python3 -m unittest discover -s scripts -p test_ja_notation_normalize.py
"""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
SCRIPT = HERE / 'ja-notation-normalize.py'
spec = importlib.util.spec_from_file_location('notation', SCRIPT)
mod = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = mod
spec.loader.exec_module(mod)


def proposal(text, key='dom.value', rules=mod.RULES, approved=None):
    src = 'export const dom = {' + json.dumps(key) + ':' + json.dumps(text, ensure_ascii=False) + '};'
    return mod.normalize(mod.values(src)[0], rules, approved, ['ワークスペース'])


# Expected skip reasons are part of the safety contract, even when the guard backs it up.
EXCLUSIONS = {
    'code': ('`Git表示`を読む。', 'code'),
    'placeholder': ('{Git表示}を見る。', 'placeholder'),
    'slot': ('<0>Git表示</0>を見る。', 'slot'),
    'quote': ('"Git表示"を見る。', 'quote'),
    'url': ('https://example.invalid/Git表示を開く。', 'url'),
    'identifier': ('AF_WORKSPACE表示を開く。', 'identifier'),
    'range': ('1〜10日を指定。', 'range'),
    'time': ('12:30時に開く。', 'time'),
    'version': ('1.2版を使う。', 'version'),
    'multiplier': ('×2倍にする。', 'multiplier'),
    'unit': ('30 GB容量です。', 'unit'),
}


class RuleTests(unittest.TestCase):
    def test_spacing_positive(self):
        cases = [('Gitホスティング', 'Git ホスティング'), ('30日後です', '30 日後です'),
                 ('{n}人が参加', '{n} 人が参加'), ('日本語Gitです', '日本語 Git です'),
                 ('プロファイル{profiles}件', 'プロファイル {profiles} 件')]
        for old, new in cases:
            with self.subTest(old=old):
                self.assertEqual(proposal(old).new, new)

    def test_workspace_conversion_removes_new_spacing_boundaries(self):
        for old, new in [('このWorkspaceを開く。', 'このワークスペースを開く。'),
                         ('Workspaceを開く。', 'ワークスペースを開く。')]:
            p = proposal(old)
            self.assertEqual(p.new, new)
            self.assertEqual([e[3] for e in p.edits], ['R3'])

    def test_kana_positive_and_contexts(self):
        text = '既に接続が無くても権限が無い。'
        p = proposal(text, approved={'dom.value': [text]})
        self.assertEqual(p.new, 'すでに接続がなくても権限がない。')
        self.assertEqual([c[1] for c in p.contexts], ['既に', '無く', '無い'])
        self.assertTrue(all(c[2] == text for c in p.contexts))

    def test_workspace_positive_and_allowance(self):
        p = proposal('この Workspace と別の Workspace を開く。')
        self.assertEqual(p.new, 'この ワークスペース と別の ワークスペース を開く。')
        self.assertEqual(p.allowances, [('dom.value', 'Workspace', 'ワークスペース', 2)])

    def check_exclusion(self, name):
        text, reason = EXCLUSIONS[name]
        p = proposal(text)
        self.assertEqual(p.new, text)
        self.assertIn(reason, [s[2] for s in p.skipped])

    def test_exclusions(self):
        for name in EXCLUSIONS:
            with self.subTest(name=name):
                self.check_exclusion(name)

    def test_punctuation_ends_and_existing_spaces(self):
        for text in ['Git、日', 'Git。日', 'Git「日」', 'Git（日）', 'Git・日', 'Git：日',
                     'Git（日）Git', 'Git', '30', ' Git 表示 ', '30 日後', '{n} 人',
                     '12:30', '1〜10', 'v1.2.3', '3x', '×1.25', '30 GB', '30GB', 'Git、。「」（）・：']:
            with self.subTest(text=text):
                self.assertEqual(proposal(text).new, text)

    def test_short_label(self):
        for text in ['A日', '3日', '30日', '日A', '日3', 'AI日']:
            self.assertEqual(proposal(text).new, text)
            self.assertIn('single-token label <=3 characters', [s[2] for s in proposal(text).skipped])

    def test_unknown_placeholder(self):
        for text in ['失敗しました{msg}', '{name}さんが参加。', '{what}項目']:
            self.assertEqual(proposal(text).new, text)
            self.assertIn('untyped placeholder', ' '.join(s[2] for s in proposal(text).skipped))

    def test_identifiers_paths_envs_and_slots(self):
        for text in ['「Git表示」', '『Workspace を起動』', '“Workspace を起動”',
                     'src/Workspace.tsを読む。', 'AF_Workspace設定です。', 'myWorkspace設定です。',
                     'Workspace/pathを開く。', 'workspaceAgent日本語です。', '--Workspaceを指定。',
                     '<0/>を読む。', '<0>既に Workspace を起動</0>',
                     '「30日後」', '10〜30日後', '1h後に起動。', '1 h後に起動。', '2 GiB以内。',
                     '12:30時', 'v1.2.3版', '3x表示', '×1.25倍']:
            with self.subTest(text=text):
                self.assertEqual(proposal(text).new, text)

    def test_unapproved_adjective(self):
        text = '設定が無いときは接続が無くても動く。'
        self.assertEqual(proposal(text).new, text)
        self.assertEqual(proposal(text, approved={'other.key': [text]}).new, text)
        self.assertEqual(proposal(text, approved={'dom.value': [text + '追記']}).new, text)

    def test_nouns_and_verbs(self):
        for text in ['無料', '無効', '無制限', '無視', '無理', '無事', '無限', '無駄', '無数',
                     '無し', '設定を無くす。', '設定が無くなる。']:
            self.assertEqual(proposal(text, approved={'dom.value': [text]}).new, text)

    def test_product_names(self):
        for text in ['Google Workspace の設定です。', 'Workspace agent が更新された。',
                     'Workspace Agent を使う。', 'Workspace ONE の設定です。', 'Workspace',
                     'Workspace settings', 'MyWorkspace を開く。']:
            self.assertEqual(proposal(text).new, text)

    def test_excluded_keys(self):
        for key in ['err.test', 'plan.review_prompt_x', 'chat.report.title', 'clean.reason.x',
                    'clean.reason_hint.x', 'notif.ready.speech', 'notif.result.failed_speech']:
            p = proposal('既に Git表示が30日で終わる。Workspace を起動。', key=key)
            self.assertEqual(p.new, p.value.text)
            self.assertTrue(p.skipped)

    def test_idempotence(self):
        for text in ['Gitホスティング', '30日後', '{n}人が参加', '既に保存済みです。', 'Workspace を起動します。']:
            first = proposal(text)
            second = proposal(first.new)
            self.assertEqual(second.new, first.new)
            self.assertFalse(second.edits)

    def test_rules_subset(self):
        self.assertEqual(proposal('既に Git表示', rules=('R1',)).new, '既に Git 表示')
        self.assertEqual(proposal('既に Git表示', rules=('R2',)).new, 'すでに Git表示')

    def test_literal_offsets_preserve_escapes_and_crlf(self):
        source = 'export const dom = {\r\n  "Gitキー": \'Git表示\\n既に保存\\t\\"quoted\\"\', // Git表示\r\n};\r\n'
        v = mod.values(source)[0]
        p = mod.normalize(v)
        changed = source
        for *_, a, b in sorted(p.edits, key=lambda e: e[4], reverse=True):
            e = next(e for e in p.edits if e[4] == a)
            changed = changed[:a] + e[2] + changed[b:]
        self.assertEqual(changed, source.replace('Git表示\\n既に', 'Git 表示\\nすでに', 1))
        self.assertEqual(mod.guard.skeleton(changed), mod.guard.skeleton(source))

    def test_escaped_and_concatenated_contents_are_skipped(self):
        source = 'export const dom = {"dom.value": "Git" + "表示と\\u65e2に保存。"};'
        p = mod.normalize(mod.values(source)[0])
        self.assertEqual(p.new, p.value.text)
        self.assertEqual(len(p.skipped), 2)

    def test_unbalanced_markup_is_preserved(self):
        for text in ['``Git表示', '<0>Git表示', '</0>Git表示', '「Git表示', '"Git表示', '{Git表示']:
            p = proposal(text)
            self.assertEqual(p.new, text)
            self.assertTrue(p.skipped)

    def test_rejects_unsupported_syntax(self):
        for text in ['export const d = {"a": `x${name}`};', 'export const d = {"a": fn()};',
                     'export const d = {"a": "x", "a": "y"};', 'export const d = {"a": "x"',
                     'export const d = { /* unclosed', 'export const d = {...x};']:
            with self.assertRaises(mod.Refusal):
                mod.values(text)


class MutantTests(unittest.TestCase):
    def test_every_span_exclusion_is_detected_when_disabled(self):
        caught = []
        for name in EXCLUSIONS:
            with self.subTest(name=name), patch.dict(mod.PROTECTIONS):
                del mod.PROTECTIONS[name]
                suite = RuleTests('test_exclusions')
                with self.assertRaises(AssertionError):
                    suite.check_exclusion(name)
                caught.append(name)
        self.assertEqual(caught, list(EXCLUSIONS))

    def test_nonspan_exclusion_mutants(self):
        source = SCRIPT.read_text(encoding='utf-8')
        mutants = [
            ("len(old) <= 3", "len(old) <= 0", 'test_short_label'),
            ("valid[0][1:-1] not in NUMERIC_PLACEHOLDERS", "False", 'test_unknown_placeholder'),
            ("if old not in (approved or {}).get(value.key, []):", "if False:", 'test_unapproved_adjective'),
            ("if word == '無く' and re.match", "if False and re.match", 'test_nouns_and_verbs'),
            ("elif re.search(r'[A-Za-z]+\\s+$', before) or re.match(r'\\s+[A-Za-z]+', after):", "elif False:", 'test_product_names'),
            ("if forbidden_key(value.key):", "if False:", 'test_excluded_keys'),
            ("JP = r'[ぁ-ゖァ-ヺ一-鿿々〆ー]'", "JP = r'[ぁ-ゖァ-ヺ一-鿿々〆ー、。：「」（）・]'", 'test_punctuation_ends_and_existing_spaces'),
        ]
        original = globals()['mod']
        for old, new, name in mutants:
            with self.subTest(name=name):
                self.assertIn(old, source)
                mutant = types.ModuleType('notation_mutant')
                mutant.__file__ = str(SCRIPT)
                sys.modules[mutant.__name__] = mutant
                exec(compile(source.replace(old, new, 1), str(SCRIPT), 'exec'), mutant.__dict__)
                globals()['mod'] = mutant
                try:
                    result = unittest.TestResult()
                    RuleTests(name).run(result)
                    self.assertTrue(result.failures, f'mutant survived: {name}; errors={result.errors}')
                finally:
                    globals()['mod'] = original
                    del sys.modules[mutant.__name__]


@unittest.skipUnless(shutil.which('git') and shutil.which('node'), 'git and node required')
class CliTests(unittest.TestCase):
    def setUp(self):
        base = Path(os.environ.get('AF_WORK_DIR') or Path.home() / '.af-work/ja-notation-tests')
        base.mkdir(parents=True, exist_ok=True)
        self.tmp = tempfile.TemporaryDirectory(dir=base)
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        self.ja = self.repo / mod.JA
        self.ja.mkdir(parents=True)
        self.en = self.repo / mod.JA.parent / 'en'
        self.en.mkdir()
        self.write('dom', {'dom.git': 'Git表示', 'dom.ws': 'Workspace を開きます。',
                           'dom.save': '保存', 'dom.saving': '保存…', 'dom.code': '`Git表示`'})
        (self.en / 'dom.ts').write_text('export const dom = {"dom.git": "Git display"};\n')
        glossary = self.repo / mod.guard.GLOSSARY
        glossary.parent.mkdir(parents=True)
        glossary.write_text('| 画面 | 意味 |\n|---|---|\n| ワークスペース | 環境 |\n')
        self.run_git('init', '-q')
        self.run_git('add', '-A')
        self.commit()

    def write(self, domain, values):
        source = '// Git表示 must stay unchanged\nexport const ' + domain + ' = {\n' + ''.join(
            '  ' + json.dumps(k) + ': ' + json.dumps(v, ensure_ascii=False) + ',\n' for k, v in values.items()) + '};\n'
        (self.ja / (domain + '.ts')).write_text(source, encoding='utf-8')

    def run_git(self, *args):
        return subprocess.run(['git', *args], cwd=self.repo, check=True, capture_output=True)

    def commit(self):
        self.run_git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture')

    def run_cli(self, *args):
        return subprocess.run([sys.executable, str(SCRIPT), *args], cwd=self.repo, capture_output=True, text=True)

    def run_guard(self, *args):
        return subprocess.run([sys.executable, str(HERE / 'catalog-diff-check.py'), 'HEAD', *args],
                              cwd=self.repo, capture_output=True, text=True)

    def test_dry_run_and_report_are_deterministic(self):
        before = (self.ja / 'dom.ts').read_bytes()
        args = ['--domain', 'dom', '--report', str(self.repo / 'report.txt'), '--allow-terms-out', str(self.repo / 'terms.tsv')]
        first = self.run_cli(*args)
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual((self.repo / 'report.txt').read_text(), first.stdout)
        self.assertIn('CANDIDATE (manual only) 保存 / 保存…', first.stdout)
        self.assertIn('SKIPPED R1 dom.code', first.stdout)
        self.assertEqual((self.ja / 'dom.ts').read_bytes(), before)
        (self.repo / 'report.txt').unlink()
        (self.repo / 'terms.tsv').unlink()
        self.assertEqual(self.run_cli(*args).stdout, first.stdout)

    def test_apply_guard_allowances_and_idempotence(self):
        before = (self.ja / 'dom.ts').read_text()
        en = (self.en / 'dom.ts').read_bytes()
        terms = self.repo / 'terms.tsv'
        p = self.run_cli('--domain', 'dom', '--apply', '--allow-terms-out', str(terms))
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('--allow-terms-file ' + str(terms), p.stdout)
        after = (self.ja / 'dom.ts').read_text()
        self.assertEqual(mod.guard.skeleton(before), mod.guard.skeleton(after))
        self.assertEqual((self.en / 'dom.ts').read_bytes(), en)
        self.assertIn('// Git表示 must stay unchanged', after)
        self.assertEqual(mod.guard.read_allowance_file(str(terms))[0][:4], ('dom.ws', 'Workspace', 'ワークスペース', 1))
        negative = self.run_guard('--allow-labels')
        self.assertEqual(negative.returncode, 1, negative.stdout + negative.stderr)
        self.assertIn('FAIL latin:', negative.stdout)
        self.assertIn('FAIL glossary:', negative.stdout)
        guarded = self.run_guard('--allow-labels', '--allow-terms-file', str(terms))
        self.assertEqual(guarded.returncode, 0, guarded.stdout + guarded.stderr)
        self.assertIn('2 changed', guarded.stdout)
        second = self.run_cli('--domain', 'dom', '--apply', '--force')
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertIn('changed=0', second.stdout)
        self.assertEqual((self.ja / 'dom.ts').read_text(), after)
        self.assertTrue(terms.read_text())

    def test_apply_adjacent_workspace_and_spacing_plan(self):
        self.write('dom', {'dom.ws': 'このWorkspaceを開く。', 'dom.git': '日本語Gitです。'})
        self.run_git('add', '-A')
        self.commit()
        p = self.run_cli('--domain', 'dom', '--apply', '--allow-terms-out', str(self.repo / 'terms.tsv'))
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        current = {v.key: v.text for v in mod.values((self.ja / 'dom.ts').read_text())}
        self.assertEqual(current, {'dom.ws': 'このワークスペースを開く。', 'dom.git': '日本語 Git です。'})
        p = self.run_guard('--allow-labels', '--allow-terms-file', str(self.repo / 'terms.tsv'))
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_dirty_refusal_preflights_every_domain(self):
        self.write('ext', {'ext.git': 'Git表示'})
        self.run_git('add', '-A')
        self.commit()
        original = (self.ja / 'dom.ts').read_bytes()
        with (self.ja / 'ext.ts').open('a') as f:
            f.write('// reviewed edit\n')
        p = self.run_cli('--domain', 'dom,ext', '--apply', '--allow-terms-out', str(self.repo / 't.tsv'))
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn('dirty catalogue file', p.stderr)
        self.assertEqual((self.ja / 'dom.ts').read_bytes(), original)
        self.assertFalse((self.repo / 't.tsv').exists())
        self.run_git('add', '-A')
        p = self.run_cli('--domain', 'ext', '--apply')
        self.assertEqual(p.returncode, 2, p.stderr)
        p = self.run_cli('--domain', 'ext', '--apply', '--force')
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_refuses_untracked_catalogue(self):
        self.write('ext', {'ext.git': 'Git表示'})
        p = self.run_cli('--domain', 'ext', '--apply')
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn('dirty catalogue file', p.stderr)

    def test_shared_labels_skip_unselected_participants(self):
        self.write('ext', {'ext.git': 'Git表示'})
        self.run_git('add', '-A')
        self.commit()
        p = self.run_cli('--domain', 'dom')
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('shared label would SPLIT: ext.git', p.stdout)
        self.assertNotIn('dom.git | Git表示 | Git 表示', p.stdout)
        p = self.run_cli('--domain', 'dom,ext')
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('dom.git | Git表示 | Git 表示', p.stdout)
        self.assertIn('ext.git | Git表示 | Git 表示', p.stdout)

    def test_candidate_pairs_cross_domains_without_edits(self):
        self.write('ext', {'ext.saving': '保存…', 'ext.session': 'セッションを削除'})
        self.write('dom', {'dom.save': '保存', 'dom.session': 'セッション削除'})
        p = self.run_cli('--domain', 'dom')
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('CANDIDATE (manual only) 保存 / 保存…', p.stdout)
        self.assertIn('CANDIDATE (manual only) セッションを削除 / セッション削除', p.stdout)
        self.assertIn('changed=0', p.stdout)

    def test_actual_whitelist_used_and_drift_skipped(self):
        key = 'agents.oc_usage_note_free'
        old = json.loads(mod.CONTEXT_FILE.read_text())[key][0]
        self.write('dom', {key: old})
        p = self.run_cli('--domain', 'dom', '--rules', 'R2')
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('接続がなくても', p.stdout)
        self.assertIn('CONTEXT R2', p.stdout)
        self.write('dom', {key: old + '追記'})
        p = self.run_cli('--domain', 'dom', '--rules', 'R2')
        self.assertIn('exact-value whitelist', p.stdout)
        self.assertIn('changed=0', p.stdout)

    def test_all_is_read_only_and_domain_names_are_validated(self):
        p = self.run_cli('--all')
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('DOMAIN dom: R1=', p.stdout)
        for args in [('--all', '--apply'), ('--domain', '../en'), ('--domain', 'missing'),
                     ('--domain', 'dom,') , ('--domain', 'dom', '--rules', 'R4')]:
            self.assertEqual(self.run_cli(*args).returncode, 2)

    def test_requires_allowance_artifact_and_safe_fresh_outputs(self):
        before = (self.ja / 'dom.ts').read_bytes()
        self.assertEqual(self.run_cli('--domain', 'dom', '--apply').returncode, 2)
        for path in [self.en / 'new.ts', self.ja / 'dom.ts', self.repo / '.git/extra']:
            self.assertEqual(self.run_cli('--domain', 'dom', '--report', str(path)).returncode, 2)
        existing = self.repo / 'existing.txt'
        existing.write_text('keep')
        self.assertEqual(self.run_cli('--domain', 'dom', '--report', str(existing)).returncode, 2)
        self.assertEqual(existing.read_text(), 'keep')
        path = self.repo / 'same.txt'
        self.assertEqual(self.run_cli('--domain', 'dom', '--report', str(path), '--allow-terms-out', str(path)).returncode, 2)
        self.assertEqual((self.ja / 'dom.ts').read_bytes(), before)

    def test_symlink_refusal(self):
        target = self.repo / 'outside.ts'
        target.write_bytes((self.ja / 'dom.ts').read_bytes())
        (self.ja / 'dom.ts').unlink()
        (self.ja / 'dom.ts').symlink_to(target)
        self.assertEqual(self.run_cli('--domain', 'dom', '--apply', '--force').returncode, 2)


if __name__ == '__main__':
    unittest.main()
