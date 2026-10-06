"""Positive and negative controls for scripts/ja-notation-normalize.py.

Unit tests call the rule functions directly (imported from the script path so
the suite works no matter what the file is named); CLI tests build a throwaway
git repository with a miniature catalogue and run the script as a subprocess,
including the real scripts/catalog-diff-check.py guard on the emitted
allowances file.

    python3 -m unittest discover -s scripts -p test_ja_notation_normalize.py
"""
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
SCRIPT = Path(os.environ.get('JA_NOTATION_NORMALIZE') or HERE / 'ja-notation-normalize.py')
GUARD = HERE / 'catalog-diff-check.py'


def load():
    spec = importlib.util.spec_from_file_location('ja_notation_normalize', SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


jn = load()
A = jn.ALL_EXCLUSIONS

NEEDS_GIT = unittest.skipUnless(shutil.which('git'), 'git is required')
NEEDS_GUARD = unittest.skipUnless(
    shutil.which('node') and shutil.which('git'), 'node and git are required')


def changed(fn, raw, exclusions=A):
    out = fn(raw, exclusions)
    return out[0] if isinstance(out, tuple) else out


class R1Positive(unittest.TestCase):
    def test_latin_japanese(self):
        self.assertEqual(changed(jn.r1_spots, 'Gitホスティング'), 'Git ホスティング')

    def test_placeholder_japanese(self):
        self.assertEqual(changed(jn.r1_spots, '{n}人'), '{n} 人')
        self.assertEqual(changed(jn.r1_spots, 'あと{days}日で期限切れ'),
                         'あと {days} 日で期限切れ')

    def test_digit_japanese(self):
        self.assertEqual(changed(jn.r1_spots, 'ホスト{hosts}件'),
                         'ホスト {hosts} 件')

    def test_japanese_latin(self):
        self.assertEqual(changed(jn.r1_spots, 'フルConsoleに展開'), 'フル Console に展開')

    def test_already_spaced_untouched(self):
        self.assertEqual(changed(jn.r1_spots, 'Git ホスティング'), 'Git ホスティング')

    def test_double_space_never_collapsed(self):
        self.assertEqual(changed(jn.r1_spots, 'Git  ホスティング'), 'Git  ホスティング')

    def test_no_leading_trailing_space(self):
        for raw in ('Git', '日本語テスト', 'Gitホスティング'):
            new = changed(jn.r1_spots, raw)
            self.assertFalse(new.startswith(' '))
            self.assertFalse(new.endswith(' '))


class R1Negative(unittest.TestCase):
    def check_same(self, raw):
        new, inserts, skips = jn.r1_spots(raw)
        self.assertEqual(new, raw)
        self.assertEqual(inserts, [])
        return skips

    def test_code_span(self):
        # Backtick edges are not a Latin/Japanese class pair at all: silent no-op.
        self.check_same('実行`git push`後')

    def test_inside_kagi(self):
        skips = self.check_same('「APIトークン」から登録')
        self.assertTrue(any('「' in r for _, _, r in skips))

    def test_slot_tag(self):
        skips = self.check_same('ブランチ<0>名')
        self.assertTrue(any('slot' in r for _, _, r in skips))

    def test_url(self):
        self.check_same('詳細はhttps://example.com/docsを参照')

    def test_quoted_ident(self):
        self.check_same('設定は"config.json"です')

    def test_path(self):
        skips = self.check_same('a/bを実行')
        self.assertTrue(any('identifier' in r for _, _, r in skips))

    def test_env_var(self):
        self.check_same('AF_TOKENを設定してください')

    def test_snake(self):
        self.check_same('ws_timeoutを設定')

    def test_dotted(self):
        self.check_same('opencode.ai側に')

    def test_flag(self):
        self.check_same('--flag指定で動く')

    def test_range(self):
        # The range itself is never split; digits+の still take a space
        # (decision C writes 1〜10 の範囲で指定します).
        self.assertEqual(changed(jn.r1_spots, '1〜10の範囲で指定'), '1〜10 の範囲で指定')

    def test_time(self):
        skips = self.check_same('12:30開始の回')
        self.assertTrue(any('time' in r for _, _, r in skips))

    def test_version(self):
        self.check_same('v1.2.3仕様で動く')

    def test_multiplier(self):
        self.check_same('3xスピードで実行')

    def test_single_letter(self):
        skips = self.check_same('X線検査と治療')
        self.assertTrue(any('single Latin letter' in r for _, _, r in skips))

    def test_short_label(self):
        skips = self.check_same('30日')
        self.assertTrue(any('<=3' in r for _, _, r in skips))

    def test_jp_punct(self):
        # 、|G is not a class pair (silent); Git|管 would propose, so end there.
        self.check_same('設定、Git')

    def test_escapes_untouched(self):
        new, inserts, _ = jn.r1_spots('1行に1件\\n例）test')
        self.assertNotIn('n 例', new)


class R2Cases(unittest.TestCase):
    def test_sudeni(self):
        self.assertEqual(changed(jn.r2_spots, '既にある設定'), 'すでにある設定')

    def test_nai(self):
        self.assertEqual(changed(jn.r2_spots, '設定が無いため'), '設定がないため')

    def test_naku(self):
        self.assertEqual(changed(jn.r2_spots, 'ごみ箱にも無く、もう参照されない'),
                                 'ごみ箱にもなく、もう参照されない')

    def test_compounds_kept(self):
        for raw in ('無料モデル', '無効なため', '無制限です', '無視されます',
                    '無理な設定', '無事に終了', '無限ループ', '無駄な処理', '無数の候補'):
            self.assertEqual(changed(jn.r2_spots, raw), raw)

    def test_noun_nashi_kept(self):
        self.assertEqual(changed(jn.r2_spots, 'ボリューム無し'), 'ボリューム無し')

    def test_verbs_kept(self):
        self.assertEqual(changed(jn.r2_spots, '行き場が無くなるため'),
                         '行き場が無くなるため')
        self.assertEqual(changed(jn.r2_spots, 'データを無くす'), 'データを無くす')

    def test_past_out_of_scope(self):
        new, events = jn.r2_spots('モデルが1つも無かったため')
        self.assertEqual(new, 'モデルが1つも無かったため')
        self.assertTrue(any(a.startswith('SKIP') for _, _, a, _ in events))

    def test_kagi_kept(self):
        self.assertEqual(changed(jn.r2_spots, '「無い」は使わない'), '「無い」は使わない')

    def test_events_report_every_occurrence(self):
        _, events = jn.r2_spots('既にあるが権限が無い')
        self.assertEqual([w for _, w, _, _ in events], ['既に', '無い'])
        self.assertTrue(all(a.startswith('CHANGE') for _, _, a, _ in events))


class R3Cases(unittest.TestCase):
    def test_plain_word(self):
        new, n, _ = jn.r3_spots('Workspace を破棄')
        self.assertEqual(new, 'ワークスペースを破棄')
        self.assertEqual(n, 1)

    def test_particles_reattached(self):
        new, n, _ = jn.r3_spots('この Workspace では使えません')
        self.assertEqual(new, 'このワークスペースでは使えません')
        self.assertEqual(n, 1)

    def test_placeholder_edge_keeps_space(self):
        # で attaches (no space kept); the {n} edge keeps its space.
        new, n, _ = jn.r3_spots('同時 {n} Workspace で実行')
        self.assertEqual(new, '同時 {n} ワークスペースで実行')
        self.assertEqual(n, 1)

    def test_product_name_kept(self):
        new, n, skips = jn.r3_spots('Google Workspaceのアカウント')
        self.assertEqual(new, 'Google Workspaceのアカウント')
        self.assertEqual(n, 0)
        self.assertTrue(skips)

    def test_lowercase_kept(self):
        new, n, _ = jn.r3_spots('workspace/repoのPR')
        self.assertEqual((new, n), ('workspace/repoのPR', 0))

    def test_standalone_label_kept(self):
        new, n, skips = jn.r3_spots('Workspace')
        self.assertEqual((new, n), ('Workspace', 0))
        self.assertTrue(skips)

    def test_identifier_kept(self):
        self.assertEqual(jn.r3_spots('MyWorkspace設定')[1], 0)

    def test_quoted_kept(self):
        self.assertEqual(jn.r3_spots('`Workspace`を削除')[1], 0)

    def test_kagi_kept(self):
        self.assertEqual(jn.r3_spots('「Workspace」設定')[1], 0)


class Mutants(unittest.TestCase):
    """Each exclusion is load-bearing: turning it off must change the outcome."""

    def test_kagi_r1(self):
        self.assertEqual(changed(jn.r1_spots, '「APIトークン」から'), '「APIトークン」から')
        self.assertEqual(changed(jn.r1_spots, '「APIトークン」から', A - {jn.EXCL_KAGI}),
                         '「API トークン」から')

    def test_single_letter(self):
        self.assertEqual(changed(jn.r1_spots, 'X線検査と治療'), 'X線検査と治療')
        self.assertEqual(changed(jn.r1_spots, 'X線検査と治療', A - {jn.EXCL_SINGLE_LETTER}),
                         'X 線検査と治療')

    def test_short_label(self):
        self.assertEqual(changed(jn.r1_spots, 'AB語'), 'AB語')
        self.assertEqual(changed(jn.r1_spots, 'AB語', A - {jn.EXCL_SHORT_LABEL}), 'AB 語')

    def test_quoted_ident(self):
        self.assertEqual(changed(jn.r1_spots, 'shell/SSM端末'), 'shell/SSM端末')
        self.assertEqual(changed(jn.r1_spots, 'shell/SSM端末', A - {jn.EXCL_QUOTED_IDENT}),
                         'shell/SSM 端末')

    def test_version_time(self):
        self.assertEqual(changed(jn.r1_spots, '12:30開始の回'), '12:30開始の回')
        self.assertEqual(changed(jn.r1_spots, '12:30開始の回', A - {jn.EXCL_VERSION_TIME}),
                         '12:30 開始の回')

    def test_kagi_r2(self):
        self.assertEqual(changed(jn.r2_spots, '「無い」は使わない'), '「無い」は使わない')
        self.assertEqual(changed(jn.r2_spots, '「無い」は使わない', A - {jn.EXCL_KAGI}),
                         '「ない」は使わない')

    def test_kagi_r3(self):
        self.assertEqual(jn.r3_spots('「Workspace」設定')[1], 0)
        self.assertEqual(jn.r3_spots('「Workspace」設定', A - {jn.EXCL_KAGI})[1], 1)


class Idempotence(unittest.TestCase):
    def test_spots_are_fixpoints(self):
        raws = ['Gitホスティングと{n}人と30日',
                '既にあるが権限が無いし接続が無くても Google Workspace と Workspace を使う「無い」',
                'ブランチ<0>名と`code`とはhttps://example.com/xと"a/b"と12:30とv1.2とX線']
        for raw in raws:
            n1, _, _ = jn.r1_spots(raw)
            n2, _, _ = jn.r1_spots(n1)
            self.assertEqual(n1, n2, raw)
            m1, _ = jn.r2_spots(raw)
            m2, _ = jn.r2_spots(m1)
            self.assertEqual(m1, m2, raw)
            k1, _, _ = jn.r3_spots(raw)
            k2, _, _ = jn.r3_spots(k1)
            self.assertEqual(k1, k2, raw)

    def test_never_touch(self):
        self.assertTrue(jn.never_touch('plan.review_prompt_rules'))
        self.assertTrue(jn.never_touch('clean.reason.cache_orphan'))
        self.assertTrue(jn.never_touch('chat.report.answer_ready'))
        self.assertTrue(jn.never_touch('notif.question.speech'))
        self.assertTrue(jn.never_touch('admin.hibernate_ph'))
        self.assertTrue(jn.never_touch('clean.reason_badge.wt_dirty'))
        self.assertIsNone(jn.never_touch('admin.destroy_ws'))


FIXTURE_JA = '''// fixture catalogue / domain: dom
export const dom = {
  // the label comment must survive byte-identical
  "dom.label": "APIトークン",
  "dom.sent": "設定が無いため、{n}件を表示",
  "dom.ws": "この Workspace では無効",
  "dom.concat":
    "前半" +
    "後半Workspace",
  "dom.kagi": "「APIトークン」から登録",
  "dom.tts.speech": "既にある",
};
'''

FIXTURE_JA_AFTER = '''// fixture catalogue / domain: dom
export const dom = {
  // the label comment must survive byte-identical
  "dom.label": "API トークン",
  "dom.sent": "設定がないため、{n} 件を表示",
  "dom.ws": "このワークスペースでは無効",
  "dom.concat":
    "前半" +
    "後半ワークスペース",
  "dom.kagi": "「APIトークン」から登録",
  "dom.tts.speech": "既にある",
};
'''

FIXTURE_EN = '''export const dom = {
  "dom.label": "API token",
};
'''

FIXTURE_GLOSSARY = '''# glossary fixture
| 画面 | Screen | 備考 |
|---|---|---|
| ワークスペース | workspace | note |
'''


@NEEDS_GIT
class CliFixture(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ.get('AF_WORK_DIR') or None)
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        env = {**os.environ, 'GIT_AUTHOR_NAME': 't', 'GIT_AUTHOR_EMAIL': 't@example.invalid',
               'GIT_COMMITTER_NAME': 't', 'GIT_COMMITTER_EMAIL': 't@example.invalid',
               'GIT_CONFIG_NOSYSTEM': '1', 'HOME': self.tmp.name}
        self.env = env
        self.ja = self.repo / 'console/src/lib/i18n/locales/ja/dom.ts'
        self.en = self.repo / 'console/src/lib/i18n/locales/en/dom.ts'
        self.ja.parent.mkdir(parents=True)
        self.en.parent.mkdir(parents=True)
        self.ja.write_text(FIXTURE_JA, encoding='utf-8')
        self.en.write_text(FIXTURE_EN, encoding='utf-8')
        gloss = self.repo / 'guide/ref/glossary.ja.md'
        gloss.parent.mkdir(parents=True)
        gloss.write_text(FIXTURE_GLOSSARY, encoding='utf-8')
        for cmd in (['init', '-q'], ['add', '-A'], ['commit', '-q', '-m', 'base']):
            subprocess.run(['git', *cmd], cwd=self.repo, env=env, check=True)

    def run_tool(self, *args):
        p = subprocess.run([sys.executable, str(SCRIPT), *args], cwd=self.repo,
                           capture_output=True, text=True)
        return p.returncode, p.stdout + p.stderr

    def test_unknown_domain_exits_2(self):
        rc, out = self.run_tool('--domain', 'nope')
        self.assertEqual(rc, 2)
        self.assertIn('unknown domain', out)

    def test_dry_run_changes_nothing(self):
        rc, out = self.run_tool('--domain', 'dom')
        self.assertEqual(rc, 0)
        self.assertIn('dom.sent | 設定が無いため、{n}件を表示 | 設定がないため、{n} 件を表示', out)
        self.assertIn('SKIPPED', out)
        self.assertEqual(self.ja.read_text(encoding='utf-8'), FIXTURE_JA)

    def test_excluded_key_never_proposed(self):
        # dom.tts.speech is never_touch; it must stay 既にある in every mode.
        (self.repo / 'console/src/lib/i18n/locales/ja/sp.ts').write_text(
            'export const sp = {\n  "notif.x.speech": "既にある",\n};\n', encoding='utf-8')
        rc, out = self.run_tool('--domain', 'sp')
        self.assertEqual(rc, 0)
        self.assertIn('EXCLUDED notif.x.speech', out)
        self.assertNotIn('すでに', out)

    def test_apply_rewrites_values_only(self):
        rc, _ = self.run_tool('--domain', 'dom', '--apply')
        self.assertEqual(rc, 0)
        self.assertEqual(self.ja.read_text(encoding='utf-8'), FIXTURE_JA_AFTER)
        self.assertEqual(self.en.read_text(encoding='utf-8'), FIXTURE_EN)

    def test_apply_is_idempotent(self):
        self.run_tool('--domain', 'dom', '--apply')
        first = self.ja.read_text(encoding='utf-8')
        subprocess.run(['git', 'add', '-A'], cwd=self.repo, env=self.env, check=True)
        subprocess.run(['git', 'commit', '-q', '-m', 'apply'], cwd=self.repo,
                       env=self.env, check=True)
        rc, out = self.run_tool('--domain', 'dom', '--apply')
        self.assertEqual(rc, 0)
        self.assertEqual(self.ja.read_text(encoding='utf-8'), first)
        self.assertIn('no changes', out)

    def test_dirty_file_refused_without_force(self):
        with open(self.ja, 'a', encoding='utf-8') as fh:
            fh.write('// dirty\n')
        rc, out = self.run_tool('--domain', 'dom', '--apply')
        self.assertEqual(rc, 2)
        self.assertIn('dirty', out)
        self.assertIn('// dirty', self.ja.read_text(encoding='utf-8'))
        rc, _ = self.run_tool('--domain', 'dom', '--apply', '--force')
        self.assertEqual(rc, 0)
        self.assertNotIn('設定が無いため', self.ja.read_text(encoding='utf-8'))

    def test_report_file(self):
        rep = self.repo / 'rep.txt'
        rc, _ = self.run_tool('--domain', 'dom', '--report', str(rep))
        self.assertEqual(rc, 0)
        self.assertIn('dom.sent', rep.read_text(encoding='utf-8'))

    def test_allow_terms_out_written(self):
        allow = self.repo / 'allow.tsv'
        rc, _ = self.run_tool('--domain', 'dom', '--allow-terms-out', str(allow))
        self.assertEqual(rc, 0)
        lines = allow.read_text(encoding='utf-8').splitlines()
        self.assertIn('dom.ws\tWorkspace\tワークスペース\t1', lines)
        self.assertIn('dom.concat\tWorkspace\tワークスペース\t1', lines)


@NEEDS_GUARD
class GuardAccepts(unittest.TestCase):
    """The emitted allowances file explains the R3 drift to the real guard."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ.get('AF_WORK_DIR') or None)
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        env = {**os.environ, 'GIT_AUTHOR_NAME': 't', 'GIT_AUTHOR_EMAIL': 't@example.invalid',
               'GIT_COMMITTER_NAME': 't', 'GIT_COMMITTER_EMAIL': 't@example.invalid',
               'GIT_CONFIG_NOSYSTEM': '1', 'HOME': self.tmp.name}
        self.env = env
        ja = self.repo / 'console/src/lib/i18n/locales/ja/dom.ts'
        ja.parent.mkdir(parents=True)
        en = self.repo / 'console/src/lib/i18n/locales/en/dom.ts'
        en.parent.mkdir(parents=True)
        ja.write_text(FIXTURE_JA, encoding='utf-8')
        en.write_text(FIXTURE_EN, encoding='utf-8')
        gloss = self.repo / 'guide/ref/glossary.ja.md'
        gloss.parent.mkdir(parents=True)
        gloss.write_text(FIXTURE_GLOSSARY, encoding='utf-8')
        for cmd in (['init', '-q'], ['add', '-A'], ['commit', '-q', '-m', 'base']):
            subprocess.run(['git', *cmd], cwd=self.repo, env=env, check=True)

    def guard(self, *args):
        p = subprocess.run([sys.executable, str(GUARD), 'HEAD', *args], cwd=self.repo,
                           capture_output=True, text=True)
        return p.returncode, p.stdout + p.stderr

    def test_guard_green_with_allowances(self):
        tool = subprocess.run(
            [sys.executable, str(SCRIPT), '--domain', 'dom', '--apply',
             '--allow-terms-out', 'allow.tsv'], cwd=self.repo,
            capture_output=True, text=True)
        self.assertEqual(tool.returncode, 0, tool.stdout + tool.stderr)
        rc, out = self.guard('--allow-labels', '--allow-terms-file', 'allow.tsv')
        self.assertEqual(rc, 0, out)
        self.assertIn('ALLOWED term', out)

    def test_guard_red_without_allowances(self):
        # Without the terms file the same edit must fail: the guard really
        # sees the Workspace -> ワークスペース drift (no vacuous pass).
        tool = subprocess.run(
            [sys.executable, str(SCRIPT), '--domain', 'dom', '--apply',
             '--allow-terms-out', 'allow.tsv'], cwd=self.repo,
            capture_output=True, text=True)
        self.assertEqual(tool.returncode, 0, tool.stdout + tool.stderr)
        rc, out = self.guard('--allow-labels')
        self.assertEqual(rc, 1, out)
        self.assertIn('FAIL', out)


if __name__ == '__main__':
    unittest.main()
