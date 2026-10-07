"""Fixtures and exclusion-removal mutants for the local notation normalizer.

Run: python3 -m unittest discover -s scripts -p test_ja_notation_normalize.py
"""
import contextlib
import importlib.util
import io
import json
import os
import re
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
        self.assertEqual(p.new, 'このワークスペースと別のワークスペースを開く。')
        self.assertEqual(p.allowances, [('dom.value', 'Workspace', 'ワークスペース', 2)])

    def test_workspace_reglue_neighbour_types(self):
        cases = [('Workspace を破棄', 'ワークスペースを破棄'),
                 ('この Workspace が動く。', 'このワークスペースが動く。'),
                 ('Workspace 、次の操作。', 'ワークスペース、次の操作。'),
                 ('Workspace 。次の操作。', 'ワークスペース。次の操作。'),
                 ('Workspace 「起動」', 'ワークスペース「起動」'),
                 ('「起動」 Workspace', '「起動」ワークスペース'),
                 ('Workspace （起動中）', 'ワークスペース（起動中）'),
                 ('（起動中） Workspace', '（起動中）ワークスペース'),
                 ('{key} の Workspace を破棄しますか？', '{key} のワークスペースを破棄しますか？'),
                 ('{name} Workspace を起動。', '{name} ワークスペースを起動。'),
                 ('起動した Workspace {name}', '起動したワークスペース {name}'),
                 ('`home` Workspace を起動。', '`home` ワークスペースを起動。'),
                 ('起動した Workspace `home`', '起動したワークスペース `home`'),
                 ('30 Workspace が動く。', '30 ワークスペースが動く。'),
                 ('起動した Workspace 30', '起動したワークスペース 30'),
                 ('起動した Workspace', '起動したワークスペース'),
                 ('この  Workspace  を破棄。', 'このワークスペースを破棄。')]
        for old, new in cases:
            with self.subTest(old=old):
                p = proposal(old)
                self.assertEqual(p.new, new)
                self.assertEqual(p.allowances, [('dom.value', 'Workspace', 'ワークスペース', 1)])
                self.assertEqual(proposal(new).new, new)

    def test_workspace_reglue_preserves_non_japanese_neighbour_spaces(self):
        cases = [('home Workspace を起動。', 'home ワークスペースを起動。'),
                 ('起動した Workspace home', '起動したワークスペース home'),
                 ('{key} Workspace を起動。', '{key} ワークスペースを起動。'),
                 ('30 Workspace を起動。', '30 ワークスペースを起動。'),
                 ('`home` Workspace を起動。', '`home` ワークスペースを起動。')]
        for old, new in cases:
            with self.subTest(old=old):
                m = mod.WORKSPACE.search(old)
                start, end = mod.workspace_replacement_span(old, m.start(), m.end())
                self.assertEqual(old[:start] + 'ワークスペース' + old[end:], new)

    def test_workspace_reglue_preserves_edges_and_line_breaks(self):
        for old, new in [(' Workspace を起動。 ', ' ワークスペースを起動。 '),
                         ('この Workspace ', 'このワークスペース '),
                         ('この\nWorkspace を起動。', 'この\nワークスペースを起動。'),
                         ('この\tWorkspace を起動。', 'この\tワークスペースを起動。')]:
            self.assertEqual(proposal(old).new, new)

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

    def check_excluded_variants(self, keys):
        text = '既に Git表示が30日で終わる。Workspace を起動。'
        for key in keys:
            for rule in mod.RULES:
                with self.subTest(key=key, rule=rule):
                    p = proposal(text, key=key, rules=(rule,))
                    self.assertEqual(p.new, text)
                    self.assertFalse(p.edits)
                    self.assertFalse(p.allowances)
                    self.assertTrue(p.skipped)
                    self.assertTrue(all('excluded agent-facing' in skip[2] for skip in p.skipped))

    def test_agent_prompt_variants_excluded(self):
        self.check_excluded_variants(['wi.prompt_review', 'wi.prompt_read_generic', 'wi.prompt_future'])

    def test_speech_variants_excluded(self):
        self.check_excluded_variants(['notif.terminal.speech_bare', 'notif.terminal.speech_short',
                                      'notif.result.failed_speech_bare', 'notif.terminal.speech.future'])

    def test_prompt_ui_description_remains_eligible(self):
        p = proposal('既に Git表示を確認しました。Workspace を起動。', key='launch.first_prompt_note')
        self.assertNotEqual(p.new, p.value.text)
        self.assertEqual({e[3] for e in p.edits}, set(mod.RULES))

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
            ("'wi.prompt_', ", "", 'test_agent_prompt_variants_excluded'),
            ("speech(?:[._]|$)", "speech$", 'test_speech_variants_excluded'),
            ("start, end = workspace_replacement_span(old, m.start(), m.end())",
             "start, end = m.start(), m.end()", 'test_workspace_reglue_neighbour_types'),
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
        base = Path(os.environ.get('AF_WORK_DIR') or Path.home() / '.af-work' / HERE.parent.name)
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

    def test_workspace_reglue_apply_matches_plan_and_guard(self):
        old = '{key} の Workspace を破棄しますか？ 別の Workspace も破棄します。'
        new = '{key} のワークスペースを破棄しますか？ 別のワークスペースも破棄します。'
        self.write('dom', {'dom.ws': old})
        self.run_git('add', '-A')
        self.commit()
        terms = self.repo / 'reglue.tsv'
        p = self.run_cli('--domain', 'dom', '--apply', '--allow-terms-out', str(terms))
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn(new, p.stdout)
        self.assertEqual(mod.values((self.ja / 'dom.ts').read_text())[0].text, new)
        self.assertEqual(mod.guard.read_allowance_file(str(terms))[0][:4], ('dom.ws', 'Workspace', 'ワークスペース', 2))
        guard = self.run_guard('--allow-labels', '--allow-terms-file', str(terms))
        self.assertEqual(guard.returncode, 0, guard.stdout + guard.stderr)
        second = self.run_cli('--domain', 'dom', '--apply', '--force')
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertIn('changed=0', second.stdout)

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

    def test_artifact_write_failure_leaves_catalogue_intact(self):
        before = (self.ja / 'dom.ts').read_bytes()
        original_open = Path.open

        def fail_artifact(path, mode='r', *args, **kwargs):
            if mode == 'x':
                raise PermissionError('fixture: artifact output is not writable')
            return original_open(path, mode, *args, **kwargs)

        with contextlib.chdir(self.repo), patch.object(Path, 'open', fail_artifact), \
                contextlib.redirect_stderr(io.StringIO()) as error:
            code = mod.main(['--domain', 'dom', '--apply', '--allow-terms-out', str(self.repo / 'terms.tsv')])
        self.assertEqual(code, 2)
        self.assertIn('not writable', error.getvalue())
        self.assertEqual((self.ja / 'dom.ts').read_bytes(), before)

    def test_nonbmp_escape_pair_applies_without_changing_escape_bytes(self):
        source = 'export const dom = {"probe.surrogate": "\\uD83D\\uDE00 Git表示です。"};\n'
        path = self.ja / 'dom.ts'
        for with_report in (False, True):
            with self.subTest(with_report=with_report):
                path.write_bytes(source.encode('utf-8'))
                args = ['--domain', 'dom', '--apply', '--force']
                report = self.repo / 'pair-report.txt'
                if with_report:
                    args += ['--report', str(report)]
                p = self.run_cli(*args)
                self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
                self.assertEqual(path.read_bytes(), source.replace('Git表示', 'Git 表示').encode('utf-8'))
                self.assertIn('😀 Git 表示です。', p.stdout)
                if with_report:
                    self.assertEqual(report.read_text(encoding='utf-8'), p.stdout)

    def test_unpaired_surrogates_refuse_before_catalogue_or_artifact_writes(self):
        path = self.ja / 'dom.ts'
        for suffix, escape in enumerate(['\\uD83D', '\\uDE00', '\\uD83D\\uD83D']):
            for with_report in (False, True):
                with self.subTest(escape=escape, with_report=with_report):
                    source = 'export const dom = {"probe.surrogate": "' + escape + ' Git表示です。"};\n'
                    before = source.encode('utf-8')
                    path.write_bytes(before)
                    args = ['--domain', 'dom', '--apply', '--force']
                    report = self.repo / f'unpaired-report-{suffix}.txt'
                    if with_report:
                        args += ['--report', str(report)]
                    p = self.run_cli(*args)
                    self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
                    self.assertIn('unpaired Unicode surrogate', p.stderr)
                    self.assertEqual(path.read_bytes(), before)
                    self.assertFalse(report.exists())

    def assert_metadata_outputs_refused(self, repo, metadata_dirs):
        before = (repo / mod.JA / 'dom.ts').read_bytes()
        for i, directory in enumerate(metadata_dirs):
            for option in ('--report', '--allow-terms-out'):
                for apply in (False, True):
                    with self.subTest(directory=directory, option=option, apply=apply):
                        output = directory / f'unexpected-{i}-{option[2:]}-{apply}.txt'
                        allowance = repo / f'outside-allowance-{i}-{option[2:]}.tsv'
                        args = ['--domain', 'dom', option, str(output)]
                        if apply:
                            args += ['--apply', '--force']
                            if option == '--report':
                                args += ['--allow-terms-out', str(allowance)]
                        p = subprocess.run([sys.executable, str(SCRIPT), *args], cwd=repo,
                                           capture_output=True, text=True)
                        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
                        self.assertIn('git metadata', p.stderr)
                        self.assertFalse(output.exists())
                        self.assertFalse(allowance.exists())
                        self.assertEqual((repo / mod.JA / 'dom.ts').read_bytes(), before)

    def test_separate_git_dir_artifact_outputs_refused(self):
        metadata = self.repo / 'real-metadata'
        shutil.move(self.repo / '.git', metadata)
        (self.repo / '.git').write_text(f'gitdir: {metadata}\n')
        self.assert_metadata_outputs_refused(self.repo, [metadata])
        p = self.run_cli('--domain', 'dom', '--report', str(self.repo / 'valid-report.txt'))
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_linked_worktree_git_and_common_dirs_artifact_outputs_refused(self):
        linked = self.repo / 'linked'
        self.run_git('worktree', 'add', '--detach', str(linked), 'HEAD')
        absolute_git_dir = Path(subprocess.check_output(['git', 'rev-parse', '--absolute-git-dir'], cwd=linked, text=True).strip())
        common_dir = (linked / subprocess.check_output(['git', 'rev-parse', '--git-common-dir'], cwd=linked, text=True).strip()).resolve()
        self.assertNotEqual(absolute_git_dir, common_dir)
        self.assertTrue((linked / '.git').is_file())
        self.assert_metadata_outputs_refused(linked, [absolute_git_dir, common_dir])

    def test_agent_and_speech_values_are_untouched_by_apply(self):
        self.write('dom', {key: '既に Git表示が30日で終わる。Workspace を起動。' for key in
                           ['wi.prompt_review', 'wi.prompt_read_generic', 'notif.terminal.speech_bare']})
        self.run_git('add', '-A')
        self.commit()
        before = (self.ja / 'dom.ts').read_bytes()
        p = self.run_cli('--domain', 'dom', '--apply')
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn('changed=0', p.stdout)
        self.assertEqual((self.ja / 'dom.ts').read_bytes(), before)

    def test_symlink_refusal(self):
        target = self.repo / 'outside.ts'
        target.write_bytes((self.ja / 'dom.ts').read_bytes())
        (self.ja / 'dom.ts').unlink()
        (self.ja / 'dom.ts').symlink_to(target)
        self.assertEqual(self.run_cli('--domain', 'dom', '--apply', '--force').returncode, 2)


@unittest.skipUnless(shutil.which('git') and (HERE.parent / mod.JA).is_dir(), 'real catalogue required')
class RealCatalogueTests(unittest.TestCase):
    def test_all_proposals_have_no_workspace_space_before_japanese(self):
        p = subprocess.run([sys.executable, str(SCRIPT), '--all'], cwd=HERE.parent,
                           capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        proposals = [line.split(' | ', 2) for line in p.stdout.splitlines()
                     if re.match(r'^[\w.]+ \| .* \[R[123](?:,R[123])*\]$', line)]
        self.assertIn('DOMAIN admin:', p.stdout)
        self.assertIn('DOMAIN settings:', p.stdout)
        self.assertEqual(proposal('この Workspace を破棄', rules=('R3',)).new, 'このワークスペースを破棄')
        stray = r'ワークスペース +[ぁ-んァ-ヶ一-龠]'
        self.assertRegex('ワークスペース を破棄', stray)
        hits = [f'{key} | {new}' for key, _, new in proposals if re.search(stray, new)]
        self.assertFalse(hits, '\n'.join(hits))


class StraySpaceLintTests(unittest.TestCase):
    def test_flags_space_between_japanese_characters(self):
        self.assertEqual(mod.stray_spaces('音声読み上げを オン にしました'), [7, 10])

    def test_spaces_next_to_latin_digits_placeholders_and_code_are_kept(self):
        for text in ('Git を開く', 'を Git で開く', '{n} 件を表示', '最大 {n} 件', '`x` を開く', '3 日後', '開く Git'):
            self.assertEqual(mod.stray_spaces(text), [], text)

    def test_spaces_inside_protected_spans_are_kept(self):
        self.assertEqual(mod.stray_spaces('「表示 設定」を開く'), [])
        self.assertEqual(mod.stray_spaces('`表示 設定`を開く'), [])

    def test_contexts_count_only_new_strays(self):
        old, new = '再開 中です', '再開 中です。起動 中です'
        added = mod.stray_contexts(new) - mod.stray_contexts(old)
        self.assertEqual(sum(added.values()), 1)


if __name__ == '__main__':
    unittest.main()
