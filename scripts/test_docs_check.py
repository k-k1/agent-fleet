"""Regression and positive-control fixtures for the shipped policy gate."""
import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("docs_check", Path(__file__).with_name("docs-check.py"))
check = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = check
spec.loader.exec_module(check)


class GithubSlugTests(unittest.TestCase):
    """github_slug against ids GitHub actually rendered for docs/ headings."""

    def test_rendered_ids(self):
        for text, rendered in (
            ("1.7 できていること・いないこと", "17-できていることいないこと"),
            ("1.6 ポート&アダプタ（プラットフォーム依存の差し替え点）", "16-ポートアダプタプラットフォーム依存の差し替え点"),
            ("7.3 L1 Console 認証（AUTH 3 モード）", "73-l1-console-認証auth-3-モード"),
            ("5.1 公開面（Console ↔ CP）", "51-公開面console--cp"),
            ("Commits & PRs", "commits--prs"),
            ("snake_case — kept", "snake_case--kept"),
            ("A ⓘ B", "a-ⓘ-b"),  # an Alphabetic symbol (So) is kept
            ("🄰 ↔ Ⓩ 🅐 🆉 ⓪", "🄰--ⓩ-🅐-🆉-"),  # the kept ranges end where they should
        ):
            with self.subTest(text=text):
                self.assertEqual(check.github_slug(text), rendered)


class DecisionAnchorTests(unittest.TestCase):
    """Anchors in an ADR are checked, same-file ones included."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.adr = self.root / "docs/decisions/0001-x.md"
        self.adr.parent.mkdir(parents=True)
        self.addCleanup(patch.stopall)
        patch.object(check, "ROOT", str(self.root)).start()
        patch.object(check, "GUIDE", str(self.root / "guide")).start()

    def errors(self, link):
        self.adr.write_text(f"# 0001. X\n\n## Revision — 取り消し（2026-09-27）\n\nSee [it]({link}).\n")
        check._cache.clear()
        findings = check.Findings()
        check.check_anchors([str(self.adr)], findings)
        return "\n".join(findings.errors)

    def test_same_file_anchor(self):
        self.assertEqual(self.errors("#revision--取り消し2026-09-27"), "")
        self.assertIn("anchor with no matching heading", self.errors("#revision--no-such-heading"))


class IndexTests(unittest.TestCase):
    """A living shelf's README links every file on the shelf, per language."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.shelf = self.root / "docs/build"
        self.shelf.mkdir(parents=True)
        for name in ("01-a.md", "01-a.ja.md", "lite.md", "lite.ja.md"):
            (self.shelf / name).write_text("# x\n")
        (self.shelf / "README.md").write_text("[1](01-a.md#top) and [lite](lite.md)\n")
        (self.shelf / "README.ja.md").write_text("[1](01-a.ja.md) and [lite](lite.ja.md)\n")
        self.addCleanup(patch.stopall)
        patch.object(check, "ROOT", str(self.root)).start()

    def errors(self):
        check._cache.clear()
        findings = check.Findings()
        files = sorted(str(p) for p in self.shelf.glob("*.md"))
        check.check_index(files, findings)
        return "\n".join(findings.errors)

    def test_complete_index_passes(self):
        self.assertEqual(self.errors(), "")

    def test_unlisted_chapter_is_an_error_per_language(self):
        (self.shelf / "02-b.md").write_text("# x\n")
        (self.shelf / "02-b.ja.md").write_text("# x\n")
        (self.shelf / "README.ja.md").write_text("[1](01-a.ja.md) [lite](lite.ja.md) [2](02-b.md)\n")
        errors = self.errors()
        self.assertIn("docs/build/02-b.md: not linked from the shelf index docs/build/README.md", errors)
        self.assertIn("docs/build/02-b.ja.md: not linked from the shelf index docs/build/README.ja.md", errors)

    def test_missing_index_is_an_error(self):
        for gone in (("README.ja.md",), ("README.md", "README.ja.md")):
            with self.subTest(gone=gone):
                self.setUp()
                for name in gone:
                    (self.shelf / name).unlink()
                errors = self.errors()
                for name in gone:
                    self.assertIn(f"has no index docs/build/{name}", errors)
                self.assertEqual(errors.count("has no index"), len(gone))

    def test_link_inside_code_does_not_count(self):
        (self.shelf / "README.md").write_text("`[1](01-a.md)` [lite](lite.md)\n")
        self.assertIn("01-a.md: not linked", self.errors())


class NotesTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.notes = self.root / "workspace/notes"
        self.notes.mkdir(parents=True)
        self.policy = self.root / "workspace/workspace-notes.md"
        self.policy.write_text("`notes/example.md`\n")
        self.topic = self.notes / "example.md"
        self.valid = '---\nname: af-example\ndescription: "Session: [agent-fleet:peer ...] (CLI)"\nuser-invocable: false\n---\nBody\n'
        self.topic.write_text(self.valid)
        self.addCleanup(patch.stopall)
        patch.object(check, "ROOT", str(self.root)).start()
        patch.object(check, "GUIDE", str(self.root / "guide")).start()

    def errors(self):
        check._cache.clear()
        findings = check.Findings()
        check.check_notes(findings)
        return "\n".join(findings.errors)

    def test_both_index_forms_and_final_paragraph(self):
        for prefix in ("notes/", "/usr/local/share/agent-fleet/notes/"):
            with self.subTest(prefix=prefix):
                self.policy.write_text("Intro\n\nRead before work:\n`" + prefix + "example.md`")
                self.assertEqual(self.errors(), "")
                self.policy.write_text("Intro\n\nRead before work:\n`" + prefix + "missing.md`")
                self.assertIn("not in the image", self.errors())
                self.assertIn("never named", self.errors())

    def test_topic_references_are_checked(self):
        self.topic.write_text(self.valid + "\nRead:\n`notes/missing.md`\n`ref/missing.md`\n`log/private.md`")
        errors = self.errors()
        for message in ("not in the image", "does not exist", "not shipped"):
            self.assertIn(message, errors)

    def test_frontmatter_positive_controls(self):
        for old, new, error in (
            ("af-example", "af-wrong", "name must be"),
            ('"Session: [agent-fleet:peer ...] (CLI)"', '""', "empty"),
            ('"Session: [agent-fleet:peer ...] (CLI)"', '"' + "x" * 1537 + '"', "1,536"),
            ("user-invocable: false", "user-invocable: true", "user-invocable: false"),
            ('(CLI)"', '(CLI)', "invalid frontmatter"),
            ('(CLI)"', '(CLI)"oops"', "invalid frontmatter"),
            ('(CLI)"', '(CLI)\\q"', "invalid frontmatter"),
        ):
            with self.subTest(new=new[:40]):
                self.topic.write_text(self.valid.replace(old, new))
                self.assertIn(error, self.errors())


class CapsRowTests(unittest.TestCase):
    """ref/agents.md rows against the Go Caps() of each kind."""

    CAPS_STRUCT = (
        "package agents\n\ntype Caps struct {\n"
        "\tCanFork       bool // fork\n\tCanTranscript bool\n\tUsesLabel     bool\n"
        "\t// PermissionChoice doc.\n\tPermissionChoice bool\n\tCanForkAt bool\n"
        "\tManagedOnly bool\n}\n"
    )
    ROWS = (
        ("Terminal (CLI) execution", "✓", "—⁹", "✓"),
        ("Live chat mirror", "✓", "✓", "—"),
        ("Copy the conversation into a new session", "✓", "✓", "—"),
        ("Fork from a past message", "✓", "—", "—"),
        ("Choosing to skip permission prompts", "—", "✓", "—"),
    )

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        agents = self.root / "workspace/agent/internal/agents"
        self.struct = agents / "agents.go"
        for kind, caps in (
            ("alpha", "CanTranscript: true, CanFork: true, CanForkAt: true"),
            ("beta", "ManagedOnly: true, CanTranscript: true, CanFork: true, PermissionChoice: true"),
        ):
            (agents / kind).mkdir(parents=True)
            (agents / kind / f"{kind}.go").write_text(
                f"package {kind}\n\nfunc (agentImpl) Caps() agents.Caps {{\n"
                f"\treturn agents.Caps{{{caps}}}\n}}\n"
            )
        self.struct.write_text(self.CAPS_STRUCT)
        session = self.root / "workspace/agent/internal/session"
        session.mkdir(parents=True)
        (session / "session.go").write_text(
            'const (\n\tKindAlpha = "alpha"\n\tKindBeta = "beta"\n\tKindShell = "shell"\n)\n'
        )
        self.table = self.root / "guide/ref/agents.md"
        self.table.parent.mkdir(parents=True)
        self.write_table(self.ROWS)
        self.addCleanup(patch.stopall)
        patch.object(check, "ROOT", str(self.root)).start()
        patch.object(check, "GUIDE", str(self.root / "guide")).start()

    def write_table(self, rows):
        lines = ["| Capability | alpha | beta | shell |", "|---|:--:|:--:|:--:|"]
        lines += ["| " + " | ".join(r) + " |" for r in rows]
        self.table.write_text("\n".join(lines) + "\n")

    def errors(self):
        check._cache.clear()
        findings = check.Findings()
        check.check_ref(findings)
        return "\n".join(findings.errors)

    def test_matching_table_passes(self):
        self.assertEqual(self.errors(), "")

    def test_every_mapped_row_catches_a_flipped_cell(self):
        for i, (label, *_cells) in enumerate(self.ROWS):
            for col in (1, 2, 3):
                with self.subTest(row=label, col=col):
                    rows = [list(r) for r in self.ROWS]
                    rows[i][col] = "—" if rows[i][col].startswith("✓") else "✓"
                    self.write_table(rows)
                    self.assertIn(f"'{label}' disagrees", self.errors())

    def test_missing_row_is_an_error(self):
        self.write_table(self.ROWS[1:])
        self.assertIn("row not found -> 'Terminal (CLI) execution'", self.errors())

    def test_unmapped_caps_field_is_an_error(self):
        self.struct.write_text(self.CAPS_STRUCT.replace("\tManagedOnly bool\n", "\tManagedOnly bool\n\tCanSing bool\n"))
        self.assertIn("field CanSing is neither mapped", self.errors())

    def test_real_struct_fields_are_all_accounted_for(self):
        patch.stopall()
        fields = check.source_caps_fields()
        self.assertIn("ManagedOnly", fields)
        mapped = {field for _, field, _ in check.CAPS_ROWS} | set(check.CAPS_UNMAPPED)
        self.assertEqual(sorted(set(fields) - mapped), [])


class AnchorTests(unittest.TestCase):
    """#fragment links against the heading ids GitHub renders for docs/."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.shelf = self.root / "docs/build"
        self.shelf.mkdir(parents=True)
        self.addCleanup(patch.stopall)
        patch.object(check, "ROOT", str(self.root)).start()
        patch.object(check, "GUIDE", str(self.root / "guide")).start()

    def errors(self, heading, link):
        target = self.shelf / "02-target.md"
        target.write_text(f"# Target\n\n{heading}\n\nBody\n")
        source = self.shelf / "01-source.md"
        source.write_text(f"See [it]({link}).\n")
        check._cache.clear()
        findings = check.Findings()
        check.check_anchors([str(source), str(target)], findings)
        return "\n".join(findings.errors)

    def test_inline_code_in_a_heading_keeps_its_text(self):
        heading = "## 2.2 Where things live (`console/src/`)"
        self.assertEqual(self.errors(heading, "02-target.md#22-where-things-live-consolesrc"), "")
        self.assertIn("anchor with no matching heading", self.errors(heading, "02-target.md#22-where-things-live-"))

    def test_heading_inside_a_fence_is_not_a_heading(self):
        for heading in ("```sh\n# not a heading\n```", "~~~markdown\n# `not a heading`\n~~~"):
            with self.subTest(heading=heading):
                self.assertIn("anchor with no matching heading", self.errors(heading, "02-target.md#not-a-heading"))

    def test_triple_backtick_code_span_is_not_a_fence(self):
        self.assertEqual(self.errors("## Syntax ```target```", "02-target.md#syntax-target"), "")

    def test_code_span_content_is_literal(self):
        heading = "## Syntax `[label](target)`"
        self.assertEqual(self.errors(heading, "02-target.md#syntax-labeltarget"), "")
        self.assertIn("anchor with no matching heading", self.errors(heading, "02-target.md#syntax-label"))


if __name__ == "__main__":
    unittest.main()
