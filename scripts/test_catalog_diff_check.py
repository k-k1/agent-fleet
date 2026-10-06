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
EN_EXT = "console/src/lib/i18n/locales/en/ext.ts"


NEEDS = unittest.skipUnless(shutil.which("node") and shutil.which("git"), "node and git are required")


class Base(unittest.TestCase):
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

    def commit_all(self, msg):
        subprocess.run(["git", "add", "-A"], cwd=self.repo, check=True)
        subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", msg],
                       cwd=self.repo, check=True)

    def assertTrips(self, category, *args):
        code, out = self.run_check(*args)
        self.assertEqual(code, 1, out)
        self.assertGreater(self.counts(out)[category], 0, out)
        return out



@NEEDS
class CatalogDiffCheckTests(Base):
    # Negative controls.

    def test_identical_passes_and_prints_counts(self):
        code, out = self.run_check(JA)
        self.assertEqual(code, 0, out)
        self.assertIn("17 value(s) checked, 0 changed", out)
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


    # Term allowances (--allow-term, --allow-terms-file).

    def test_allow_term_latin_to_glossary_passes_and_is_printed(self):
        self.edit(JA, "自動保存は OFF です。", "自動保存はオフです。")
        code, out = self.run_check("--allow-term", "dom.toggle:OFF>オフ")
        self.assertEqual(code, 0, out)
        self.assertIn("ALLOWED term: ", out)
        self.assertRegex(out, r"dom\.toggle: OFF -> オフ x1 \(OFF 1 -> 0, オフ 0 -> 1\)")
        self.assertIn("1 of 1 term allowance(s) applied", out)
        self.assertEqual(self.counts(out)["allow"], 0)

    def test_allow_term_missing_flag_still_fails_both_categories(self):
        self.edit(JA, "自動保存は OFF です。", "自動保存はオフです。")
        out = self.assertTrips("latin")
        self.assertGreater(self.counts(out)["glossary"], 0, out)
        self.assertNotIn("allow", self.counts(out))

    def test_allow_term_glossary_synonym_passes(self):
        self.edit(JA, '"dom.default_a": "既定の設定です。"', '"dom.default_a": "オフの設定です。"')
        code, out = self.run_check("--allow-term", "dom.default_a:既定>オフ")
        self.assertEqual(code, 0, out)
        self.assertRegex(out, r"既定 -> オフ x1 \(既定 1 -> 0, オフ 0 -> 1\)")

    def test_allow_term_non_glossary_new_is_counted_directly(self):
        self.edit(JA, '"dom.default_a": "既定の設定です。"', '"dom.default_a": "デフォルトの設定です。"')
        code, out = self.run_check("--allow-term", "dom.default_a:既定>デフォルト")
        self.assertEqual(code, 0, out)
        self.assertRegex(out, r"既定 1 -> 0, デフォルト 0 -> 1")
        self.assertTrips("allow", "--allow-term", "dom.default_a:既定>デフォルト*2")

    def test_allow_term_does_not_cover_the_same_change_in_another_key(self):
        self.edit(JA, '"dom.default_a": "既定の設定です。"', '"dom.default_a": "オフの設定です。"')
        self.edit(JA, '"dom.default_b": "既定の設定です。"', '"dom.default_b": "オフの設定です。"')
        out = self.assertTrips("glossary", "--allow-term", "dom.default_a:既定>オフ")
        self.assertRegex(out, r"FAIL glossary: \S+ dom\.default_b: ")
        self.assertNotRegex(out, r"FAIL glossary: \S+ dom\.default_a: ")
        code, out = self.run_check("--allow-term", "dom.default_a:既定>オフ", "--allow-term", "dom.default_b:既定>オフ")
        self.assertEqual(code, 0, out)
        self.assertIn("2 of 2 term allowance(s) applied", out)

    def test_allow_term_wrong_direction_fails_and_is_stale(self):
        self.edit(JA, "自動保存は OFF です。", "自動保存はオフです。")
        out = self.assertTrips("allow", "--allow-term", "dom.toggle:オフ>OFF")
        self.assertIn("matched no change", out)
        self.assertGreater(self.counts(out)["latin"], 0, out)

    def test_allow_term_failure_labels_direct_count_units(self):
        self.edit(JA, "設定を保存してから、画面を閉じてください。", "先に設定を保存し、そのあと画面を閉じてください。")
        out = self.assertTrips("allow", "--allow-term", "dom.wording:画面>表示")
        self.assertIn("表示 [direct] 0 -> 0 (allowed +1)", out)

    def test_allow_term_wrong_delta_fails(self):
        self.edit(JA, "自動保存は OFF です。", "自動保存はオフです。")
        out = self.assertTrips("allow", "--allow-term", "dom.toggle:OFF>オフ*2")
        self.assertIn("OFF [latin] 1 -> 0 (allowed -2)", out)
        self.assertGreater(self.counts(out)["latin"], 0, out)

    def test_allow_term_more_change_than_allowed_fails(self):
        self.edit(JA, "自動保存は OFF です。", "自動保存はオフです。オフです。")
        out = self.assertTrips("allow", "--allow-term", "dom.toggle:OFF>オフ")
        self.assertIn("オフ [glossary] 0 -> 2 (allowed +1)", out)

    def test_allow_term_count_covers_repeats(self):
        self.edit(JA, "自動保存は OFF です。", "OFF と OFF です。")
        self.commit_all("two OFF")
        self.edit(JA, "OFF と OFF です。", "オフとオフです。")
        code, out = self.run_check("--allow-term", "dom.toggle:OFF>オフ*2")
        self.assertEqual(code, 0, out)
        self.assertIn("x2", out)
        self.assertTrips("allow", "--allow-term", "dom.toggle:OFF>オフ")

    def test_allow_term_unrelated_drift_in_the_same_value_still_fails(self):
        self.edit(JA, "自動保存は OFF です。", "自動保存はオフです。ワークスペース")
        out = self.assertTrips("glossary", "--allow-term", "dom.toggle:OFF>オフ")
        self.assertIn("ワークスペース", out)

    def test_allow_term_stale_when_nothing_changed(self):
        out = self.assertTrips("allow", "--allow-term", "dom.toggle:OFF>オフ")
        self.assertIn("the key was not among the changed values", out)
        out = self.assertTrips("allow", "--allow-term", "dom.nothing:OFF>オフ")
        self.assertIn("stale", out)

    def test_allow_term_does_not_exempt_other_checks(self):
        self.edit(JA, "自動保存は OFF です。", "自動保存はオフです。 3 日")
        out = self.assertTrips("digits", "--allow-term", "dom.toggle:OFF>オフ")
        self.assertEqual(self.counts(out)["allow"], 0, out)

    def test_allow_terms_file(self):
        self.edit(JA, "自動保存は OFF です。", "自動保存はオフです。")
        self.edit(JA, '"dom.default_a": "既定の設定です。"', '"dom.default_a": "オフの設定です。"')
        self.write("allow.tsv", "# reviewed\n\ndom.toggle\tOFF\tオフ\ndom.default_a\t既定\tオフ\t1\n")
        code, out = self.run_check("--allow-terms-file", "allow.tsv")
        self.assertEqual(code, 0, out)
        self.assertIn("2 of 2 term allowance(s) applied", out)
        self.write("allow.tsv", "dom.toggle\tOFF\n")
        code, out = self.run_check("--allow-terms-file", "allow.tsv")
        self.assertEqual(code, 2, out)

    def test_allow_term_reversed_pair_cannot_cancel_out(self):
        self.edit(JA, "設定を保存してから、画面を閉じてください。", "先に設定を保存し、そのあと画面を閉じてください。")
        code, out = self.run_check("--allow-term", "dom.wording:OFF>オフ", "--allow-term", "dom.wording:オフ>OFF")
        self.assertEqual(code, 2, out)
        self.assertIn("both an OLD and a NEW", out)

    def test_allow_term_chain_is_refused(self):
        code, out = self.run_check("--allow-term", "dom.wording:OFF>オフ", "--allow-term", "dom.wording:オフ>既定")
        self.assertEqual(code, 2, out)

    def test_allow_term_same_direction_duplicates_sum(self):
        self.edit(JA, "自動保存は OFF です。", "OFF と OFF です。")
        self.commit_all("two OFF")
        self.edit(JA, "OFF と OFF です。", "オフとオフです。")
        code, out = self.run_check("--allow-term", "dom.toggle:OFF>オフ", "--allow-term", "dom.toggle:OFF>オフ")
        self.assertEqual(code, 0, out)  # 1 + 1 = the stated total of 2
        self.assertTrips("allow", "--allow-term", "dom.toggle:OFF>オフ*2", "--allow-term", "dom.toggle:OFF>オフ")

    def test_allow_terms_file_keeps_delimiter_characters_in_terms(self):
        self.edit(JA, "設定を保存してから", "あ>い を保存してから")
        self.commit_all("odd term")
        self.edit(JA, "あ>い を保存してから", "う*い を保存してから")
        self.write("allow.tsv", "dom.wording\tあ>い\tう*い\n")
        code, out = self.run_check("--allow-terms-file", "allow.tsv")
        self.assertEqual(code, 0, out)
        self.assertIn("あ>い -> う*い x1", out)
        self.write("allow.tsv", "dom.wording:x\tあ>い\tう*い\n")
        code, out = self.run_check("--allow-terms-file", "allow.tsv")
        self.assertEqual(code, 1, out)  # a colon in the key is part of the key, not a term
        self.assertIn("matched no change", out)

    def test_bad_allow_term_spec(self):
        for spec in ("dom.toggle", "dom.toggle:OFF", "dom.toggle:OFF>OFF", "dom.toggle:OFF>オフ*0", ":OFF>オフ"):
            code, out = self.run_check("--allow-term", spec)
            self.assertEqual(code, 2, f"{spec}: {out}")


@NEEDS
class CatalogDiffCheckEnTests(Base):
    """--lang en: the same fixtures, with locales/en/ as the target."""

    def run_en(self, *args):
        return self.run_check("--lang", "en", *args)

    def assertEnTrips(self, category, *args):
        code, out = self.run_en(*args)
        self.assertEqual(code, 1, out)
        self.assertGreater(self.counts(out)[category], 0, out)
        return out

    # Negative controls.

    def test_en_identical_passes_and_prints_counts(self):
        code, out = self.run_en(EN, EN_EXT)
        self.assertEqual(code, 0, out)
        self.assertIn("2 file(s), 23 value(s) checked, 0 changed", out)
        self.assertEqual(set(self.counts(out).values()), {0})
        self.assertIn("warnings: restriction=0", out)

    def test_en_no_changed_file_checks_nothing(self):
        code, out = self.run_en()
        self.assertEqual(code, 0, out)
        self.assertIn("0 file(s), 0 value(s) checked", out)

    def test_en_pure_wording_rewrite_passes(self):
        self.edit(EN, "Save your settings — then close this screen.", "Save your settings. Then close this screen.")
        code, out = self.run_en()
        self.assertEqual(code, 0, out)
        self.assertIn("1 changed", out)
        self.assertNotIn("WARN", out)

    def test_en_restriction_word_warns_but_passes(self):
        self.edit(EN_EXT, "Only admins can change this.", "Admins can change this.")
        code, out = self.run_en()
        self.assertEqual(code, 0, out)
        self.assertRegex(out, r"WARN restriction: .*ext\.restr: only 1 -> 0")
        self.assertIn("warnings: restriction=1", out)

    def test_en_contraction_counts_as_the_restriction_word(self):
        self.edit(EN, "This cannot be undone, so take care before you continue.",
                  "This can't be undone, so take care before you continue.")
        code, out = self.run_en()
        self.assertEqual(code, 0, out)
        self.assertNotIn("WARN", out)

    def test_en_ja_guide_and_readme_are_not_searched(self):
        self.write("guide/member/page.ja.md", "This cannot be undone, so take care before you continue.\n")
        self.write("guide/README.md", "This cannot be undone, so take care before you continue.\n")
        self.write("workspace/agent/knowledge/af-usage.md", "This cannot be undone, so take care before you continue.\n")
        self.edit(EN, "This cannot be undone, so take care before you continue.", "Take care: this is permanent.")
        code, out = self.run_en()
        self.assertEqual(code, 0, out)

    def test_en_unchanged_clause_still_quoted_is_not_pinned(self):
        self.write("guide/member/page.md", "# Page\n\nThis cannot be undone, so take care before you continue.\n")
        self.commit_all("quote")
        self.edit(EN, "This cannot be undone, so take care before you continue.",
                  "This cannot be undone, so take care before you continue. Really.")
        code, out = self.run_en()
        self.assertEqual(code, 0, out)

    # Structure.

    def test_en_skeleton_comment_change(self):
        self.edit(EN, "a comment that must not move", "a comment that moved")
        self.assertEnTrips("skeleton")

    def test_en_key_order(self):
        text = (self.repo / EN).read_text(encoding="utf-8")
        a = '  "dom.label": "Stop",\n'
        b = '  "dom.pinned": "This cannot be undone, so take care before you continue.",\n'
        (self.repo / EN).write_text(text.replace(a + b, b + a), encoding="utf-8")
        self.assertEnTrips("keys")

    def test_en_key_added_and_file_removed(self):
        self.edit(EN, '  "dom.label": "Stop",\n', '  "dom.label": "Stop",\n  "dom.new": "Added",\n')
        self.assertIn("dom.new", self.assertEnTrips("keys"))
        (self.repo / EN).unlink()
        self.assertEnTrips("keys", EN)

    def test_en_concatenation_structure(self):
        self.edit(EN, '"First half. " + "Second half."', '"First half. Second half."')
        self.assertEnTrips("skeleton")

    def test_en_ja_edit_is_outside(self):
        self.edit(JA, "自動保存は OFF です。", "自動保存は オフ です。")
        out = self.assertEnTrips("outside")
        self.assertIn("ja/dom.ts changed", out)

    def test_en_untracked_ja_file_is_outside(self):
        self.write("console/src/lib/i18n/locales/ja/new.ts", "export const n = {};\n")
        self.assertEnTrips("outside")

    def test_en_ja_file_is_refused_as_target(self):
        code, out = self.run_en(JA)
        self.assertEqual(code, 2, out)

    # Meaning-carrying content of a value.

    def test_en_placeholder(self):
        self.edit(EN, "{profiles} profiles", "{profile} profiles")
        self.assertEnTrips("placeholders")

    def test_en_trans_slot_dropped(self):
        self.edit(EN, "Change this in <0>Settings</0>.<1/>", "Change this in <0>Settings</0>.")
        self.assertEnTrips("slots")

    def test_en_digits(self):
        self.edit(EN, "after 3 days", "after 5 days")
        self.assertEnTrips("digits")

    def test_en_code_span(self):
        self.edit(EN, "`npm test`", "`npm run test`")
        self.assertEnTrips("code")

    def test_en_newline_removed(self):
        self.edit(EN, "Line one.\\nLine two.", "Line one. Line two.")
        self.assertEnTrips("newlines")

    def test_en_edge_whitespace(self):
        self.edit(EN, '" Leading space here."', '"Leading space here."')
        self.assertEnTrips("edge")

    def test_en_all_caps_word_changed(self):
        self.edit(EN, "Autosave is OFF.", "Autosave is off.")
        self.assertEnTrips("caps")

    def test_en_env_var_changed(self):
        self.edit(EN_EXT, "AF_MASTER_KEY", "AF_SECRET")
        out = self.assertEnTrips("caps")
        self.assertGreater(self.counts(out)["idents"], 0, out)

    def test_en_path_changed(self):
        self.edit(EN_EXT, "settings.json", "config.json")
        self.assertEnTrips("idents")

    def test_en_cli_name_changed_or_recased(self):
        for new in ("claude", "Codex"):
            with self.subTest(new=new):
                self.edit(EN_EXT, "Use codex or opencode", f"Use {new} or opencode")
                self.assertEnTrips("idents")
                self.edit(EN_EXT, f"Use {new} or opencode", "Use codex or opencode")

    def test_en_and_or_is_not_a_path(self):
        self.edit(EN_EXT, "Use codex or opencode here.", "Use codex and/or opencode here.")
        code, out = self.run_en()
        self.assertEqual(code, 0, out)

    def test_en_quoted_label_content_changed(self):
        self.edit(EN, 'Press \\"Restart\\"', 'Press \\"Reboot\\"')
        self.assertEnTrips("quoted")

    def test_en_quote_style_alone_is_not_a_change(self):
        self.edit(EN, 'Press \\"Restart\\"', 'Press “Restart”')
        code, out = self.run_en()
        self.assertEqual(code, 0, out)
        self.assertIn("1 changed", out)

    def test_en_curly_quoted_content_changed(self):
        self.edit(EN, 'Press \\"Restart\\"', 'Press “Reboot”')
        self.assertEnTrips("quoted")

    def test_en_product_name_recased(self):
        self.edit(EN_EXT, "Use codex or opencode here.", "Use codex or opencode here. Sign in to Cursor.")
        self.commit_all("cursor")
        self.edit(EN_EXT, "Sign in to Cursor.", "Sign in to cursor.")
        self.assertEnTrips("idents")

    def test_en_curly_apostrophe_contraction_is_same_word(self):
        self.edit(EN, "This cannot be undone", "This can’t be undone")
        code, out = self.run_en()
        self.assertEqual(code, 0, out)
        self.assertNotIn("WARN", out)

    def test_en_negation_added_or_removed_warns(self):
        self.edit(EN_EXT, "Only admins can change this.", "Only admins can’t change this.")
        code, out = self.run_en()
        self.assertEqual(code, 0, out)
        self.assertRegex(out, r"WARN restriction: .*ext\.restr: .*cannot 0 -> 1")
        self.assertIn("warnings: restriction=1", out)

    def test_en_arrow_dropped(self):
        self.edit(EN_EXT, "Open Settings → Connections.", "Open Settings, then Connections.")
        self.assertEnTrips("marks")

    def test_en_warning_mark_dropped(self):
        self.edit(EN_EXT, "⚠ This will stop every session.", "This will stop every session.")
        self.assertEnTrips("marks")

    def test_en_glossary_term_replaced_by_synonym(self):
        self.edit(EN, "Starting the Workspace.", "Starting the Environment.")
        out = self.assertEnTrips("glossary")
        self.assertIn('"Workspace" 1 -> 0', out)

    def test_en_glossary_is_case_insensitive_and_plural_tolerant(self):
        self.edit(EN_EXT, "stop every session", "stop all sessions")
        code, out = self.run_en()
        self.assertEqual(code, 0, out)  # session -> sessions is the same word
        self.assertIn("1 changed", out)

    def test_en_glossary_paren_note_dropped(self):
        self.edit(EN, "Starting the Workspace.", "Starting the Terminal.")
        out = self.assertEnTrips("glossary")
        self.assertIn('"Terminal" 0 -> 1', out)

    # Labels.

    def test_en_label_change_needs_flag(self):
        self.edit(EN, '"dom.label": "Stop"', '"dom.label": "Halt"')
        self.assertEnTrips("label")

    def test_en_label_change_with_flag_prints_old_and_new(self):
        self.edit(EN, '"dom.label": "Stop"', '"dom.label": "Halt"')
        code, out = self.run_en("--allow-labels")
        self.assertEqual(code, 0, out)
        self.assertRegex(out, r"LABEL .*dom\.label: Stop -> Halt")

    def test_en_label_threshold_is_30_chars_without_sentence_end(self):
        self.edit(EN, '"dom.label": "Stop"', '"dom.label": "Stop the whole thing right now"')
        self.assertEnTrips("label")  # 29 chars
        self.edit(EN, '"dom.lang": "Language"', '"dom.lang": "Language, as shown on screen."')
        self.assertEqual(self.counts(self.run_en("--allow-labels")[1])["label"], 0)  # a sentence

    def test_en_label_still_held_to_invariants(self):
        self.edit(EN, '"dom.label": "Stop"', '"dom.label": "STOP"')
        code, out = self.run_en("--allow-labels")
        self.assertEqual(code, 1, out)
        self.assertGreater(self.counts(out)["caps"], 0, out)

    # Pins.

    def en_pinned(self, rel, text, *args):
        self.write(rel, text)
        self.edit(EN, "This cannot be undone, so take care before you continue.", "Take care: this is permanent.")
        out = self.assertEnTrips("pinned", *args)
        self.assertRegex(out, r"PINNED: .*dom\.pinned: .*" + re.escape(rel))
        return out

    def test_en_pinned_in_english_guide_page(self):
        out = self.en_pinned("guide/member/page.md", "# Page\n\nThis cannot be undone, so take care before you continue.\n")
        self.assertIn("page.md:3", out)

    def test_en_pinned_in_guide_even_when_wrapped(self):
        self.en_pinned("guide/member/page.md", "# Page\n\nThis cannot be undone,\n  so take care before you continue.\n")

    def test_en_pinned_in_console_test(self):
        self.en_pinned("console/src/x.test.tsx", 'expect(t).toBe("This cannot be undone, so take care before you continue");\n')

    def test_en_pinned_in_go_source(self):
        self.en_pinned("control-plane/msg.go", 'package p\n\nvar m = "This cannot be undone, so take care before you continue"\n')

    def test_en_pinned_label_in_bold_and_quotes(self):
        for text in ("Press **Stop** now\n", 'Press "Stop" now\n'):
            with self.subTest(text=text):
                self.write("guide/member/page.md", text)
                self.edit(EN, '"dom.label": "Stop"', '"dom.label": "Halt"')
                code, out = self.run_en("--allow-labels")
                self.assertEqual(code, 1, out)
                self.assertGreater(self.counts(out)["pinned"], 0, out)
                self.edit(EN, '"dom.label": "Halt"', '"dom.label": "Stop"')

    def test_en_pinned_label_in_markdown_quote_forms(self):
        for text in ("Click `Stop` now\n", "Click 'Stop' now\n"):
            with self.subTest(text=text):
                self.write("guide/member/page.md", text)
                self.edit(EN, '"dom.label": "Stop"', '"dom.label": "Halt"')
                self.assertEnTrips("pinned", "--allow-labels")
                code, out = self.run_en("--list-pinned")
                self.assertIn("guide/member/page.md:1\t", out)
                code, out = self.run_en("--allow-labels", "--exempt-pin=dom.label@guide/member/page.md")
                self.assertEqual(code, 0, out)
                self.edit(EN, '"dom.label": "Halt"', '"dom.label": "Stop"')

    def test_en_citation_updated_in_same_change_passes(self):
        self.write("guide/member/page.md", "Press **Halt** now\n")
        self.edit(EN, '"dom.label": "Stop"', '"dom.label": "Halt"')
        code, out = self.run_en("--allow-labels")
        self.assertEqual(code, 0, out)

    def test_en_list_pinned_prints_locations_and_exits_zero(self):
        self.write("guide/member/page.md", "# Page\n\nPress **Stop** now\n")
        self.edit(EN, '"dom.label": "Stop"', '"dom.label": "Halt"')
        code, out = self.run_en("--list-pinned")
        self.assertEqual(code, 0, out)
        self.assertIn("guide/member/page.md:3\t", out)
        self.assertNotIn("failures:", out)

    def test_en_exempt_pin(self):
        self.write("guide/member/page.md", "Press **Stop** now\n")
        self.edit(EN, '"dom.label": "Stop"', '"dom.label": "Halt"')
        code, out = self.run_en("--allow-labels", "--exempt-pin=dom.label@guide/member/page.md")
        self.assertEqual(code, 0, out)
        self.assertIn("1 pin(s) exempted", out)
        code, out = self.run_en("--allow-labels", "--exempt-pin=dom.label@guide/member/other.md")
        self.assertEqual(code, 1, out)
        self.assertIn("matched nothing", out)

    # --triples.

    def test_en_triples_prints_ja_old_new_and_exits_zero(self):
        self.edit(EN, '"dom.label": "Stop"', '"dom.label": "Halt"')
        self.edit(EN, "{profiles} profiles", "{profile} profiles")  # a failing change still lists
        self.edit(EN, "Line one.\\nLine two.", "Line one. Line two.")
        code, out = self.run_en("--triples")
        self.assertEqual(code, 0, out)
        rows = [l.split("\t") for l in out.splitlines()]
        self.assertEqual(rows[0], ["key", "ja", "en old", "en new"])
        self.assertIn(["dom.label", "停止", "Stop", "Halt"], rows)
        self.assertIn(["dom.multi", "1 行目です。\\n2 行目です。", "Line one.\\nLine two.", "Line one. Line two."], rows)
        self.assertNotIn("FAIL", out)

    def test_en_triples_with_nothing_changed_prints_only_the_header(self):
        code, out = self.run_en("--triples")
        self.assertEqual(code, 0, out)
        self.assertEqual(out.splitlines()[0], "key\tja\ten old\ten new")

    def test_triples_needs_lang_en(self):
        code, out = self.run_check("--triples")
        self.assertEqual(code, 2, out)

    def test_en_unknown_ref(self):
        p = subprocess.run([sys.executable, str(SCRIPT), "no-such-ref", "--lang", "en"], cwd=self.repo, capture_output=True, text=True)
        self.assertEqual(p.returncode, 2, p.stderr)


    # Term allowances, en mode.

    def test_en_allow_term_glossary_synonym_passes_and_other_key_fails(self):
        self.edit(EN, '"dom.default_a": "The default setting."', '"dom.default_a": "The standard setting."')
        self.edit(EN, '"dom.default_b": "The default setting."', '"dom.default_b": "The standard setting."')
        out = self.assertEnTrips("glossary", "--allow-term", "dom.default_a:Default>standard")
        self.assertRegex(out, r"dom\.default_b: \"Default\" 1 -> 0")
        self.assertNotRegex(out, r"FAIL glossary: \S+ dom\.default_a: ")
        code, out = self.run_en("--allow-term", "dom.default_a:Default>standard",
                                "--allow-term", "dom.default_b:default>standard")
        self.assertEqual(code, 0, out)
        self.assertIn("ALLOWED term: ", out)
        self.assertIn("2 of 2 term allowance(s) applied", out)

    def test_en_allow_term_case_variants_sum_as_one_glossary_unit(self):
        self.edit(EN, '"dom.default_a": "The default setting."', '"dom.default_a": "The default default setting."')
        self.commit_all("two defaults")
        self.edit(EN, "The default default setting.", "The standard standard setting.")
        code, out = self.run_en("--allow-term", "dom.default_a:Default>standard",
                                "--allow-term", "dom.default_a:default>standard")
        self.assertEqual(code, 0, out)
        self.assertIn("2 of 2 term allowance(s) applied", out)
        self.assertEnTrips("allow", "--allow-term", "dom.default_a:Default>standard")

    def test_en_allow_term_glossary_and_caps_units_sum_per_category(self):
        self.edit(EN, '"dom.default_a": "The default setting."', '"dom.default_a": "The default DEFAULT setting."')
        self.commit_all("default DEFAULT")
        self.edit(EN, "The default DEFAULT setting.", "The standard standard setting.")
        code, out = self.run_en("--allow-term", "dom.default_a:Default>standard",
                                "--allow-term", "dom.default_a:DEFAULT>standard")
        self.assertEqual(code, 0, out)
        self.assertIn("2 of 2 term allowance(s) applied", out)
        self.assertIn("DEFAULT [glossary] 2 -> 0, DEFAULT [caps] 1 -> 0, standard 0 -> 2", out)
        # An unapproved caps drift stays red: only the glossary half is allowed.
        code, out = self.run_en("--allow-term", "dom.default_a:Default>standard*2")
        self.assertEqual(code, 1, out)
        self.assertGreater(self.counts(out)["caps"], 0, out)

    def test_en_allow_term_caps_word(self):
        self.edit(EN, "Autosave is OFF.", "Autosave is off.")
        code, out = self.run_en("--allow-term", "dom.toggle:OFF>off")
        self.assertEqual(code, 0, out)
        self.assertRegex(out, r"OFF 1 -> 0, off 0 -> 1")
        self.assertEnTrips("allow", "--allow-term", "dom.toggle:off>OFF")
        self.assertEnTrips("allow", "--allow-term", "dom.toggle:OFF>off*2")
        self.assertEnTrips("caps")

    def test_en_allow_term_stale_and_default_output_unchanged(self):
        out = self.assertEnTrips("allow", "--allow-term", "dom.toggle:OFF>off")
        self.assertIn("stale", out)
        code, out = self.run_en()
        self.assertEqual(code, 0, out)
        self.assertNotIn("allow", out)


@NEEDS
class LabelSyncTests(Base):
    def sync_path(self, lang='ja', other=False):
        return f"console/src/lib/i18n/locales/{lang}/sync{'_other' if other else ''}.ts"

    def change_unique(self, lang='ja'):
        self.edit(self.sync_path(lang), '操作ボタン' if lang == 'ja' else 'Action button',
                  '実行ボタン' if lang == 'ja' else 'Run button')

    def sync_run(self, *args, lang='ja'):
        return self.run_check('--lang', lang, '--allow-labels', *args)

    def page(self, text, lang='ja'):
        path = f"guide/member/sync{'.ja' if lang == 'ja' else ''}.md"
        self.write(path, text)
        self.commit_all('citation fixture')
        return path

    def test_split_partial_change_fails_across_domains(self):
        for lang, old, new in [('ja', '共通ラベル', '統一ラベル'), ('en', 'Shared label', 'Unified label')]:
            with self.subTest(lang=lang):
                self.edit(self.sync_path(lang), old, new)
                code, out = self.sync_run(self.sync_path(lang), lang=lang)
                self.assertEqual(code, 1, out)
                self.assertIn('FAIL split:', out)
                self.assertIn('sync.other', out)
                self.edit(self.sync_path(lang), new, old)

    def test_split_all_keys_changed_passes(self):
        self.edit(self.sync_path(), '共通ラベル', '統一ラベル')
        self.edit(self.sync_path(other=True), '共通ラベル', '統一ラベル')
        code, out = self.sync_run()
        self.assertEqual(code, 0, out)
        self.assertNotIn('split=', out)

    def test_split_approval_requires_all_participants_and_rejects_stale(self):
        self.edit(self.sync_path(), '共通ラベル', '統一ラベル')
        code, out = self.sync_run('--allow-split=sync.shared')
        self.assertEqual(code, 1, out)
        code, out = self.sync_run('--allow-split=sync.shared,sync.other')
        self.assertEqual(code, 0, out)
        self.assertIn('ALLOWED split:', out)
        code, out = self.sync_run('--allow-split=sync.shared,sync.other,absent')
        self.assertEqual(code, 1, out)
        self.assertIn('stale approval', out)
        self.assertEqual(self.sync_run('--allow-split=sync.shared,')[0], 2)

    def test_approved_split_is_never_auto_rewritten(self):
        page = self.page('**共通ラベル**\n')
        self.edit(self.sync_path(), '共通ラベル', '統一ラベル')
        code, out = self.sync_run('--rewrite-guide', '--allow-split=sync.shared,sync.other')
        self.assertEqual(code, 1, out)
        self.assertEqual((self.repo / page).read_text(), '**共通ラベル**\n')

    def test_rewrite_bold_and_quote_but_not_prose_or_longer_words(self):
        page = self.page('**操作ボタン**と「操作ボタン」。\n操作ボタン\n「操作ボタン中」 **操作ボタン中** 操作ボタン中\n')
        self.change_unique()
        code, out = self.sync_run('--rewrite-guide')
        self.assertEqual(code, 1, out)
        self.assertIn('bare-in-prose-exact-match', out)
        self.assertEqual((self.repo / page).read_text(),
                         '**実行ボタン**と「実行ボタン」。\n操作ボタン\n「操作ボタン中」 **操作ボタン中** 操作ボタン中\n')

    def test_rewrite_menu_segments(self):
        page = self.page('設定 > 操作ボタン > 詳細\n設定 → 操作ボタン\n操作ボタン → 詳細\n設定 > 操作ボタン中\n')
        self.change_unique()
        code, out = self.sync_run('--rewrite-guide')
        self.assertEqual(code, 0, out)
        self.assertEqual((self.repo / page).read_text(),
                         '設定 > 実行ボタン > 詳細\n設定 → 実行ボタン\n実行ボタン → 詳細\n設定 > 操作ボタン中\n')

    def test_rewrite_heading_left_unresolved(self):
        page = self.page('# **操作ボタン**\n\n操作ボタン\n---\n\n**操作ボタン**\n')
        self.change_unique()
        code, out = self.sync_run('--rewrite-guide')
        self.assertEqual(code, 1, out)
        self.assertIn('\theading\t', out)
        self.assertIn('# **操作ボタン**', (self.repo / page).read_text())
        self.assertTrue((self.repo / page).read_text().endswith('**実行ボタン**\n'))

    def test_rewrite_test_citation_left_unresolved(self):
        self.write('console/src/sync.test.ts', 'expect(value).toBe("操作ボタン");\n')
        page = self.page('**操作ボタン**\n')
        self.change_unique()
        code, out = self.sync_run('--rewrite-guide')
        self.assertEqual(code, 1, out)
        self.assertIn('console/src/sync.test.ts:1', out)
        self.assertEqual((self.repo / page).read_text(), '**実行ボタン**\n')
        code, out = self.sync_run('--rewrite-guide', '--exempt-pin=sync.unique@console/src/sync.test.ts:1')
        self.assertEqual(code, 0, out)
        self.assertIn('EXEMPT:', out)

    def test_exemption_protects_independent_span_from_rewrite(self):
        page = self.page('**操作ボタン**\n「操作ボタン」\n')
        self.change_unique()
        code, out = self.sync_run('--rewrite-guide', '--exempt-pin=sync.unique@' + page + ':1')
        self.assertEqual(code, 0, out)
        self.assertEqual((self.repo / page).read_text(), '**操作ボタン**\n「実行ボタン」\n')
        self.assertIn('1 pin(s) exempted', out)

    def test_list_citations_sections_exactness_and_read_only(self):
        self.write('console/src/sync.test.ts', 'const label = "操作ボタン"; const longer = "操作ボタン中";\n')
        self.write('workspace/agent/knowledge/af-usage.md', '**操作ボタン**\n')
        self.write('workspace/agent/knowledge/af-usage.coverage.tsv', 'fixture\t操作ボタン\t操作ボタン中\n')
        self.write('control-plane/sync.go', 'package fixture\nvar label = "操作ボタン"\n')
        page = self.page('「操作ボタン」 **操作ボタン**\n操作ボタン\n# 操作ボタン\n設定 > 操作ボタン\n「操作ボタン中」 **操作ボタン中**\n')
        original = (self.repo / page).read_bytes()
        self.change_unique()
        code, out = self.sync_run('--list-citations')
        self.assertEqual(code, 0, out)
        rows = [l.split('\t') for l in out.splitlines() if '\t' in l]
        self.assertEqual(len(rows), 9, out)
        self.assertTrue(all(len(r) == 5 and r[2:] == ['操作ボタン', '実行ボタン', 'sync.unique'] for r in rows), out)
        self.assertEqual({r[1] for r in rows}, {'bracket-quote', 'bold', 'bare-in-prose-exact-match', 'heading', 'menu-segment', 'quoted', 'tsv-cell'})
        self.assertEqual((self.repo / page).read_bytes(), original)
        code, out = self.sync_run('--rewrite-guide')
        self.assertEqual(code, 1, out)
        for section in ('af-usage.md', 'af-usage.coverage.tsv', 'Go sources'):
            self.assertIn('# ' + section, out)

    def test_rewrite_idempotent(self):
        page = self.page('「操作ボタン」 **操作ボタン**\n')
        self.change_unique()
        code, out = self.sync_run('--rewrite-guide')
        self.assertEqual(code, 0, out)
        first = (self.repo / page).read_bytes()
        code, out = self.sync_run('--rewrite-guide')
        self.assertEqual(code, 0, out)
        self.assertEqual((self.repo / page).read_bytes(), first)
        self.assertNotRegex(out, r'guide/[^\n]+ 操作ボタン -> 実行ボタン')

    def test_rewrite_dirty_refusal_and_force(self):
        page = self.page('**操作ボタン**\n')
        self.change_unique()
        self.write(page, '**操作ボタン**\nUser edit\n')
        code, out = self.sync_run('--rewrite-guide')
        self.assertEqual(code, 2, out)
        self.assertIn('dirty guide file:', out)
        self.assertEqual((self.repo / page).read_text(), '**操作ボタン**\nUser edit\n')
        code, out = self.sync_run('--rewrite-guide', '--force')
        self.assertEqual(code, 0, out)
        self.assertEqual((self.repo / page).read_text(), '**実行ボタン**\nUser edit\n')

    def test_dirty_preflight_never_partially_writes(self):
        first = self.page('**操作ボタン**\n')
        second = 'guide/member/z-sync.ja.md'
        self.write(second, '**操作ボタン**\n')
        self.change_unique()
        self.assertEqual(self.sync_run('--rewrite-guide')[0], 2)
        self.assertEqual((self.repo / first).read_text(), '**操作ボタン**\n')

    def test_settings_first_column_rewritten_and_warned(self):
        for lang, old, new in [('ja', '専用タブ', '特別タブ'), ('en', 'Special tab', 'Dedicated tab')]:
            with self.subTest(lang=lang):
                page = f"guide/ref/settings{'.ja' if lang == 'ja' else ''}.md"
                self.write(page, f'| {old} | {old}説明 |\n')
                self.commit_all('settings fixture')
                self.edit(self.sync_path(lang), old, new)
                code, out = self.sync_run('--list-citations', lang=lang)
                self.assertEqual(code, 0, out)
                self.assertIn('WARN SETTINGS TAB', out)
                self.assertIn('\ttable-cell\t', out)
                code, out = self.sync_run('--rewrite-guide', lang=lang)
                self.assertEqual(code, 0, out)
                self.assertEqual((self.repo / page).read_text(), f'| {new} | {old}説明 |\n')
                self.edit(self.sync_path(lang), new, old)

    def test_en_twin_rewrites_only_english_guide(self):
        ja = self.page('**Action button**\n')
        en = self.page('**Action button**\nSettings > Action button\n', lang='en')
        self.change_unique('en')
        code, out = self.sync_run('--rewrite-guide', lang='en')
        self.assertEqual(code, 0, out)
        self.assertEqual((self.repo / ja).read_text(), '**Action button**\n')
        self.assertEqual((self.repo / en).read_text(), '**Run button**\nSettings > Run button\n')

    def test_rewrite_fenced_code_is_manual(self):
        page = self.page('```md\n**操作ボタン**\n```\n')
        self.change_unique()
        code, out = self.sync_run('--rewrite-guide')
        self.assertEqual(code, 1, out)
        self.assertIn('\tcode\t', out)
        self.assertEqual((self.repo / page).read_text(), '```md\n**操作ボタン**\n```\n')

    def test_rewrite_refuses_symlink_outside_guide(self):
        outside = self.repo / 'manual.ja.md'
        outside.write_text('**操作ボタン**\n')
        page = self.repo / 'guide/member/sync.ja.md'
        page.symlink_to(outside)
        self.commit_all('symlink fixture')
        self.change_unique()
        code, out = self.sync_run('--rewrite-guide', '--force')
        self.assertEqual(code, 2, out)
        self.assertIn('outside guide/', out)
        self.assertEqual(outside.read_text(), '**操作ボタン**\n')

    def test_rewrite_refuses_chains_for_idempotence(self):
        self.page('**操作ボタン** **専用タブ**\n')
        self.edit(self.sync_path(), '操作ボタン', '専用タブ')
        self.edit(self.sync_path(), '"set.tab_fixture": "専用タブ"', '"set.tab_fixture": "特別タブ"')
        code, out = self.sync_run('--rewrite-guide')
        self.assertEqual(code, 2, out)
        self.assertIn('idempotently', out)


if __name__ == "__main__":
    unittest.main()
