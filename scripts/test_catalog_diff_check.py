"""Positive and negative controls for scripts/catalog-diff-check.py.

Each test builds a throwaway git repository from scripts/catalog_diff_check_fixtures/,
commits it, mutates the working tree the way a bad rewrite would, and runs the script
against HEAD. Every failure category has a mutation that must trip it.

    python3 -m unittest discover -s scripts -p test_catalog_diff_check.py
"""
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
# CATALOG_DIFF_CHECK points the suite at a mutated copy (checking that each test can fail).
SCRIPT = Path(os.environ.get("CATALOG_DIFF_CHECK") or HERE / "catalog-diff-check.py")
FIXTURES = HERE / "catalog_diff_check_fixtures"
JA = "console/src/lib/i18n/locales/ja/dom.ts"
EN = "console/src/lib/i18n/locales/en/dom.ts"


@unittest.skipUnless(shutil.which("node") and shutil.which("git"), "node and git are required")
class CatalogDiffCheckTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ.get("AF_WORK_DIR") or None)
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        shutil.copytree(FIXTURES, self.repo, dirs_exist_ok=True)
        env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.invalid",
               "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.invalid"}
        for cmd in (["init", "-q"], ["add", "-A"], ["commit", "-q", "-m", "base"]):
            subprocess.run(["git", *cmd], cwd=self.repo, env=env, check=True)

    def run_check(self, *args):
        p = subprocess.run([sys.executable, str(SCRIPT), "HEAD", *args], cwd=self.repo,
                           capture_output=True, text=True)
        return p.returncode, p.stdout + p.stderr

    def edit(self, rel, old, new):
        path = self.repo / rel
        text = path.read_text(encoding="utf-8")
        self.assertIn(old, text, "fixture drifted")
        path.write_text(text.replace(old, new, 1), encoding="utf-8")

    def write(self, rel, text):
        path = self.repo / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def counts(self, out):
        line = next(l for l in out.splitlines() if l.startswith("failures:"))
        return {k: int(v) for k, v in re.findall(r"(\w+)=(\d+)", line)}

    def assertTrips(self, category, *args):
        code, out = self.run_check(*args)
        self.assertEqual(code, 1, out)
        self.assertGreater(self.counts(out)[category], 0, out)
        return out

    # Negative controls.

    def test_identical_passes_and_prints_counts(self):
        code, out = self.run_check(JA)
        self.assertEqual(code, 0, out)
        self.assertIn("14 value(s) checked, 0 changed", out)
        self.assertEqual(set(self.counts(out).values()), {0})

    def test_no_changed_file_checks_nothing_but_says_so(self):
        code, out = self.run_check()
        self.assertEqual(code, 0, out)
        self.assertIn("0 file(s), 0 value(s) checked", out)

    def test_pure_wording_rewrite_passes(self):
        self.edit(JA, "設定を保存してから、画面を閉じてください。", "先に設定を保存し、そのあと画面を閉じてください。")
        code, out = self.run_check()
        self.assertEqual(code, 0, out)
        self.assertIn("1 changed", out)

    def test_unchanged_clause_still_quoted_is_not_pinned(self):
        self.write("guide/member/page.ja.md", "# ページ\n\nこの操作は取り消せませんので注意してください。\n")
        subprocess.run(["git", "add", "-A"], cwd=self.repo, check=True)
        subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "quote"],
                       cwd=self.repo, check=True)
        self.edit(JA, "この操作は取り消せませんので注意してください。",
                  "この操作は取り消せませんので注意してください。元には戻せません。")
        code, out = self.run_check()
        self.assertEqual(code, 0, out)

    # Structure.

    def test_comment_change(self):
        self.edit(JA, "a comment that must not move", "a comment that moved")
        self.assertTrips("skeleton")

    def test_comment_with_apostrophe_is_not_a_string(self):
        self.edit(JA, "// a comment that must not move", "// don't move this one")
        self.assertTrips("skeleton")

    def test_key_order(self):
        text = (self.repo / JA).read_text(encoding="utf-8")
        a = '  "dom.label": "停止",\n'
        b = '  "dom.pinned": "この操作は取り消せませんので注意してください。",\n'
        (self.repo / JA).write_text(text.replace(a + b, b + a), encoding="utf-8")
        self.assertTrips("keys")

    def test_key_renamed(self):
        self.edit(JA, '"dom.days"', '"dom.day_count"')
        self.assertTrips("keys")

    def test_key_added(self):
        self.edit(JA, '  "dom.label": "停止",\n', '  "dom.label": "停止",\n  "dom.new": "追加",\n')
        out = self.assertTrips("keys")
        self.assertIn("dom.new", out)

    def test_concatenation_structure(self):
        self.edit(JA, '"前半です。" + "後半です。"', '"前半です。後半です。"')
        self.assertTrips("skeleton")

    def test_added_and_removed_file(self):
        self.write("console/src/lib/i18n/locales/ja/extra.ts", 'export const extra = {};\n')
        self.assertTrips("keys", "console/src/lib/i18n/locales/ja/extra.ts")
        (self.repo / JA).unlink()
        self.assertTrips("keys", JA)

    def test_en_edit(self):
        self.edit(EN, "Autosave is OFF.", "Autosave is off.")
        self.assertTrips("outside")

    def test_untracked_en_file(self):
        self.write("console/src/lib/i18n/locales/en/new.ts", "export const n = {};\n")
        self.assertTrips("outside")

    def test_file_outside_ja_dir_is_refused(self):
        code, out = self.run_check("guide/ref/glossary.ja.md")
        self.assertEqual(code, 2, out)

    def test_unknown_ref(self):
        p = subprocess.run([sys.executable, str(SCRIPT), "no-such-ref"], cwd=self.repo, capture_output=True, text=True)
        self.assertEqual(p.returncode, 2, p.stderr)

    # Meaning-carrying content of a value.

    def test_off_to_on(self):
        self.edit(JA, "自動保存は OFF です。", "自動保存は ON です。")
        self.assertTrips("latin")

    def test_digit_change(self):
        self.edit(JA, "3 日たつと", "5 日たつと")
        self.assertTrips("digits")

    def test_added_kagi_label(self):
        self.edit(JA, "設定を保存してから、", "「保存」してから、")
        self.assertTrips("kagi")

    def test_broken_placeholder(self):
        self.edit(JA, "{profiles} 件", "{profile} 件")
        self.assertTrips("placeholders")

    def test_trans_slot_dropped(self):
        self.edit(JA, "<0>設定</0>で変更できます。<1/>", "<0>設定</0>で変更できます。")
        self.assertTrips("slots")

    def test_code_span(self):
        self.edit(JA, "`npm test`", "`npm run test`")
        self.assertTrips("code")

    def test_newline_removed(self):
        self.edit(JA, "1 行目です。\\n2 行目です。", "1 行目です。2 行目です。")
        self.assertTrips("newlines")

    def test_edge_whitespace(self):
        self.edit(JA, '" 先頭に空白があります。"', '"先頭に空白があります。"')
        self.assertTrips("edge")

    def test_glossary_term_replaced(self):
        self.edit(JA, "ワークスペースを起動します。", "作業場を起動します。")
        out = self.assertTrips("glossary")
        self.assertIn("ワークスペース", out)

    # Labels.

    def test_label_change_needs_flag(self):
        self.edit(JA, '"dom.label": "停止"', '"dom.label": "中断"')
        self.assertTrips("label")

    def test_label_change_with_flag_prints_old_and_new(self):
        self.edit(JA, '"dom.label": "停止"', '"dom.label": "中断"')
        code, out = self.run_check("--allow-labels")
        self.assertEqual(code, 0, out)
        self.assertRegex(out, r"LABEL .*dom\.label: 停止 -> 中断")

    def test_label_still_held_to_invariants(self):
        self.edit(JA, '"dom.label": "停止"', '"dom.label": "Stop"')
        code, out = self.run_check("--allow-labels")
        self.assertEqual(code, 1, out)
        self.assertGreater(self.counts(out)["latin"], 0, out)

    # Pins.

    def pinned(self, rel, text, *args, edit=("この操作は取り消せませんので注意してください。", "元に戻せません。注意してください。")):
        self.write(rel, text)
        self.edit(JA, *edit)
        out = self.assertTrips("pinned", *args)
        self.assertRegex(out, r"PINNED: .*dom\.pinned: .*" + re.escape(rel))
        return out

    def test_pinned_in_console_test(self):
        out = self.pinned("console/src/x.test.tsx", 'expect(text).toBe("この操作は取り消せませんので注意してください");\n')
        self.assertIn("x.test.tsx:1", out)

    def test_pinned_in_go_source(self):
        self.pinned("control-plane/msg.go", 'package p\n\nvar m = "この操作は取り消せませんので注意してください"\n')

    def test_pinned_in_guide_even_when_wrapped(self):
        out = self.pinned("guide/member/page.ja.md", "# ページ\n\nこの操作は取り消せません\nので注意してください。\n")
        self.assertIn("page.ja.md:", out)

    def test_pinned_in_indented_continuation_with_right_line(self):
        out = self.pinned("guide/member/page.ja.md",
                          "# ページ\n\n- 手順\n  この操作は取り消せません\n  ので注意してください。\n")
        self.assertIn("page.ja.md:4", out)

    def test_paragraph_break_is_not_joined_into_a_quote(self):
        self.write("guide/member/page.ja.md", "この操作は取り消せません\n\nので注意してください。\n")
        self.edit(JA, "この操作は取り消せませんので注意してください。", "元に戻せません。注意してください。")
        code, out = self.run_check()
        self.assertEqual(code, 0, out)

    def test_pinned_in_af_usage(self):
        self.pinned("workspace/agent/knowledge/af-usage.md", "この操作は取り消せませんので注意してください\n")

    def test_pinned_label_in_kagi_and_bold(self):
        for text in ("「停止」を押す\n", "**停止** を押す\n"):
            with self.subTest(text=text):
                self.write("guide/member/page.ja.md", text)
                self.edit(JA, '"dom.label": "停止"', '"dom.label": "中断"')
                code, out = self.run_check("--allow-labels")
                self.assertEqual(code, 1, out)
                self.assertGreater(self.counts(out)["pinned"], 0, out)
                self.edit(JA, '"dom.label": "中断"', '"dom.label": "停止"')

    def reword_lang(self):
        self.write("workspace/agent/chat_test.go", 'package p\n\nvar a = strings.Contains(out, "言語")\n\nvar b = strings.Contains(out, "言語")\n')
        self.write("console/src/z.test.tsx", 'getByText("言語");\n')
        self.edit(JA, '"dom.lang": "言語"', '"dom.lang": "表示言語"')

    def test_exempt_pin_accepts_only_the_reviewed_hit(self):
        self.reword_lang()
        go = "workspace/agent/chat_test.go"
        code, out = self.run_check("--allow-labels", f"--exempt-pin=dom.lang@{go}")
        self.assertEqual(code, 1, out)  # the real getByText citation still fails
        self.assertIn("z.test.tsx:1", out)
        self.assertEqual(out.count("EXEMPT:"), 2)
        self.write("console/src/z.test.tsx", 'getByText("表示言語");\n')
        code, out = self.run_check("--allow-labels", f"--exempt-pin=dom.lang@{go}")
        self.assertEqual(code, 0, out)
        self.assertIn("2 pin(s) exempted", out)

    def test_exempt_pin_with_line_and_stale_warning(self):
        self.reword_lang()
        self.write("console/src/z.test.tsx", "\n")
        go = "workspace/agent/chat_test.go"
        code, out = self.run_check("--allow-labels", f"--exempt-pin=dom.lang@{go}:3")
        self.assertEqual(code, 1, out)  # line 5 is not exempted
        self.assertIn("chat_test.go:5", out)
        code, out = self.run_check("--allow-labels", f"--exempt-pin=dom.lang@{go}", "--exempt-pin=dom.lang@nowhere.go")
        self.assertEqual(code, 0, out)
        self.assertIn("WARN: --exempt-pin dom.lang@nowhere.go matched nothing", out)

    def test_exempt_pin_other_key_does_not_apply(self):
        self.reword_lang()
        self.write("console/src/z.test.tsx", "\n")
        code, out = self.run_check("--allow-labels", "--exempt-pin=dom.other@workspace/agent/chat_test.go")
        self.assertEqual(code, 1, out)

    def test_bad_exempt_pin_spec(self):
        code, out = self.run_check("--exempt-pin=oops")
        self.assertEqual(code, 2, out)

    def test_pinned_label_quoted_in_test_code(self):
        self.write("console/src/y.test.tsx", 'getByText("停止");\n')
        self.edit(JA, '"dom.label": "停止"', '"dom.label": "中断"')
        code, out = self.run_check("--allow-labels")
        self.assertEqual(code, 1, out)
        self.assertGreater(self.counts(out)["pinned"], 0, out)

    def test_citation_updated_in_same_change_passes(self):
        self.write("guide/member/page.ja.md", "「中断」を押す\n")
        self.edit(JA, '"dom.label": "停止"', '"dom.label": "中断"')
        code, out = self.run_check("--allow-labels")
        self.assertEqual(code, 0, out)

    def test_list_pinned_prints_only_locations_and_exits_zero(self):
        self.write("guide/member/page.ja.md", "# ページ\n\n「停止」を押す\n")
        self.edit(JA, '"dom.label": "停止"', '"dom.label": "中断"')
        code, out = self.run_check("--list-pinned")
        self.assertEqual(code, 0, out)
        self.assertIn("guide/member/page.ja.md:3\t", out)
        self.assertNotIn("failures:", out)


if __name__ == "__main__":
    unittest.main()
