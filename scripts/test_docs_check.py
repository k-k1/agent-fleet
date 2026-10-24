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
        "\tManagedOnly bool\n\tManagedDefault bool\n\tMCPClient Tri\n}\n"
    )
    ROWS = (
        ("Terminal (CLI) execution", "✓", "—⁹", "✓"),
        ("Live chat mirror", "✓", "✓", "—"),
        ("Copy the conversation into a new session", "✓", "✓", "—"),
        ("Fork from a past message", "✓", "—", "—"),
        ("Choosing to skip permission prompts", "—", "✓", "—"),
        ("Receives integration (MCP) servers", "✓", "—", "—"),
        ("Starts child sessions and messages other sessions", "✓", "—", "—"),
        # Rows that rest on code other than Caps(): the rtk artifact, the image studio refusal.
        ("RTK (token savings)", "✓", "—", "—"),
        ("Bound to an image studio", "✓", "—", "—"),
    )
    STUDIO_STORE = (
        "package imagegen\n\nfunc StudioSessionUnsupported(kind, driver string) string {\n"
        "\tswitch kind {\n\tcase session.KindBeta:\n\t\treturn \"no\"\n\t}\n\treturn \"\"\n}\n"
    )

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        agents = self.root / "workspace/agent/internal/agents"
        self.struct = agents / "agents.go"
        for kind, caps in (
            ("alpha", "CanTranscript: true, CanFork: true, CanForkAt: true, MCPClient: agents.TriYes"),
            ("beta", "ManagedOnly: true, CanTranscript: true, CanFork: true, PermissionChoice: true,"
             " MCPClient: agents.TriNo"),
        ):
            (agents / kind).mkdir(parents=True)
            (agents / kind / f"{kind}.go").write_text(
                f"package {kind}\n\nfunc (agentImpl) Caps() agents.Caps {{\n"
                f"\treturn agents.Caps{{{caps}}}\n}}\n"
            )
        self.struct.write_text(self.CAPS_STRUCT)
        (agents / "alpha" / "rtk.go").write_text("package alpha\n\nfunc ApplyRTK(on bool) {}\n")
        imagegen = self.root / "workspace/agent/internal/imagegen"
        imagegen.mkdir(parents=True)
        (imagegen / "studio_store.go").write_text(self.STUDIO_STORE)
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

    def test_rtk_follows_the_artifact_in_code(self):
        # beta gains an rtk artifact (a hook command constant, as claude has): its cell must flip.
        (self.root / "workspace/agent/internal/agents/beta/rtk.go").write_text(
            'package beta\n\nconst rtkHookCommand = "rtk hook beta"\n'
        )
        self.assertIn("'RTK (token savings)' disagrees", self.errors())

    def test_studio_follows_the_refusal_in_code(self):
        # beta is ManagedOnly: refusing its Managed mode leaves it no route, so it is not
        # ticked. Dropping the refusal must therefore demand a tick; a refused kind that has
        # a Terminal route (alpha) stays ticked.
        store = self.root / "workspace/agent/internal/imagegen/studio_store.go"
        store.write_text(self.STUDIO_STORE.replace("KindBeta", "KindAlpha"))
        self.assertIn("'Bound to an image studio' disagrees", self.errors())
        self.write_table([r if r[0] != "Bound to an image studio" else (r[0], "✓", "✓", "—") for r in self.ROWS])
        self.assertEqual(self.errors(), "")

    def test_studio_reader_handles_a_multiline_case_and_refuses_shapes_it_cannot_read(self):
        store = self.root / "workspace/agent/internal/imagegen/studio_store.go"
        # A valid multi-line case still refuses beta: the table must stay as it is.
        store.write_text(self.STUDIO_STORE.replace("case session.KindBeta:", "case\n\t\tsession.KindBeta:"))
        self.assertEqual(self.errors(), "")
        # Even with a "corrected" table, a shape the reader cannot interpret is an error,
        # never a quiet approval.
        self.write_table([r if r[0] != "Bound to an image studio" else (r[0], "✓", "✓", "—") for r in self.ROWS])
        self.assertIn("disagrees", self.errors())
        for body in (
            "case session.KindBeta, session.KindNope:",   # unknown constant
            'case "beta":',                               # literal
            "case kindOf(x):",                            # expression
        ):
            with self.subTest(body=body):
                store.write_text(self.STUDIO_STORE.replace("case session.KindBeta:", body))
                self.assertIn("cannot be checked", self.errors())
        store.write_text(self.STUDIO_STORE.replace("\tswitch kind {", "\tif kind == \"x\" {}\n\tswitch kind {\n\tcase session.KindShell:\n\tswitch driver {"))
        self.assertIn("cannot be checked", self.errors())

    def test_derived_row_with_unreadable_source_is_an_error(self):
        (self.root / "workspace/agent/internal/imagegen/studio_store.go").unlink()
        self.assertIn("cannot be checked", self.errors())

    def test_missing_row_is_an_error(self):
        self.write_table(self.ROWS[1:])
        self.assertIn("row not found -> 'Terminal (CLI) execution'", self.errors())

    def test_unmapped_caps_field_is_an_error(self):
        self.struct.write_text(self.CAPS_STRUCT.replace("\tManagedOnly bool\n", "\tManagedOnly bool\n\tCanSing bool\n"))
        self.assertIn("field CanSing is neither mapped", self.errors())

    def test_unmapped_tri_field_is_an_error(self):
        self.struct.write_text(self.CAPS_STRUCT.replace("\tMCPClient Tri\n", "\tMCPClient Tri\n\tCanSing Tri\n"))
        self.assertIn("field CanSing is neither mapped", self.errors())

    def write_beta_mcp(self, value):
        """Rewrite beta's Caps() with MCPClient set to value ("" = left out)."""
        caps = "ManagedOnly: true, CanTranscript: true, CanFork: true, PermissionChoice: true"
        if value:
            caps += f", MCPClient: {value}"
        (self.root / "workspace/agent/internal/agents/beta/beta.go").write_text(
            f"package beta\n\nfunc (agentImpl) Caps() agents.Caps {{\n\treturn agents.Caps{{{caps}}}\n}}\n"
        )

    def test_tri_field_counts_only_yes(self):
        # no, unverified and unset all read as "not marked"; only TriYes is a ✓.
        for value in ("agents.TriNo", "agents.TriUnverified", ""):
            with self.subTest(value=value):
                self.write_beta_mcp(value)
                self.assertEqual(self.errors(), "")
        self.write_beta_mcp("agents.TriYes")
        self.assertIn("'Receives integration (MCP) servers' disagrees", self.errors())

    def test_tri_prefix_is_not_yes(self):
        # A longer constant that merely starts with TriYes must not count as yes.
        self.write_beta_mcp("agents.TriYesButNotMeasured")
        self.assertEqual(self.errors(), "")

    def test_real_struct_fields_are_all_accounted_for(self):
        patch.stopall()
        fields = check.source_caps_fields()
        self.assertIn("ManagedOnly", fields)
        self.assertIn("MCPClient", fields)
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


class RootDocTests(unittest.TestCase):
    """The repository root's *.md files get the link and anchor checks."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / "docs/build").mkdir(parents=True)
        (self.root / "docs/build/07-security.md").write_text("# 07. Security\n\n## 7.1 Threat model\n")
        self.addCleanup(patch.stopall)
        patch.object(check, "ROOT", str(self.root)).start()
        patch.object(check, "GUIDE", str(self.root / "guide")).start()

    def errors(self, link):
        source = self.root / "SECURITY.md"
        source.write_text(f"# Security Policy\n\nSee [it]({link}).\n")
        check._cache.clear()
        findings = check.Findings()
        files = check.root_docs()
        check.check_links(files, findings)
        check.check_anchors(files, findings)
        return "\n".join(findings.errors)

    def test_root_docs_lists_only_the_root(self):
        (self.root / "SECURITY.md").write_text("# Security Policy\n")
        self.assertEqual([Path(p).name for p in check.root_docs()], ["SECURITY.md"])

    def test_anchor_is_checked(self):
        self.assertEqual(self.errors("docs/build/07-security.md#71-threat-model"), "")
        self.assertIn("anchor with no matching heading", self.errors("docs/build/07-security.md#72-isolation"))

    def test_link_is_checked(self):
        self.assertIn("broken link", self.errors("docs/build/08-missing.md"))

    def test_link_into_guide_uses_the_github_id(self):
        # The Console gives this heading `a-b`; GitHub, where a root file is read, `a--b`.
        (self.root / "guide/ref").mkdir(parents=True)
        (self.root / "guide/ref/probe.md").write_text("# Probe\n\n## A — B\n")
        self.assertEqual(self.errors("guide/ref/probe.md#a--b"), "")
        self.assertIn("does not match the id GitHub assigns", self.errors("guide/ref/probe.md#a-b"))


class SettingTabsTests(unittest.TestCase):
    """ref/settings*.md rows against the Console's per-domain tab labels."""

    SETTINGS = (
        "export const settings = {\n"
        '  "set.tab_display": "Displ\\u0061y",\n'
        "  'set.tab_keys': 'Key\\'s',\n"
        "  \"set.tab_tts\": `Read aloud`,\n"
        '  // "set.tab_ghost": "Ghost" in a line comment\n'
        '  /* "set.tab_ghost": "Ghost" in a block comment */\n'
        "  \"set.help\": 'Example: \"set.tab_ghost\": \"Ghost\"',\n"
        '  "set.link": "https://example.com/a//b", // trailing comment\n'
        '  "set.tabs_title": "Not a tab",\n'
        "};\n"
    )
    ROWS = ["Display", "Key's", "Read aloud", "Engine access"]

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.locales = self.root / "console/src/lib/i18n/locales"
        for locale in ("en", "ja"):
            (self.locales / locale).mkdir(parents=True)
            # The composition file holds no key, as in the real tree.
            (self.locales / f"{locale}.ts").write_text(f"export const {locale} = {{ ...settings }};\n")
            (self.locales / locale / "settings.ts").write_text(self.SETTINGS)
            (self.locales / locale / "admin.ts").write_text(
                'export const admin = {\n  "tenant.tab_engine_access":\n    "Engine access",\n};\n'
            )
        self.ref = self.root / "guide/ref"
        self.ref.mkdir(parents=True)
        self.write_rows(self.ROWS)
        self.addCleanup(patch.stopall)
        patch.object(check, "ROOT", str(self.root)).start()
        patch.object(check, "GUIDE", str(self.root / "guide")).start()

    def write_rows(self, rows):
        for name in ("settings.md", "settings.ja.md"):
            table = ["| Tab | Configures |", "|---|---|"] + [f"| {r} | x |" for r in rows]
            (self.ref / name).write_text("\n".join(table) + "\n")

    def errors(self):
        check._cache.clear()
        findings = check.Findings()
        check.check_ref(findings)
        return "\n".join(findings.errors)

    def test_reads_only_real_tab_entries_and_decodes_them(self):
        check._cache.clear()
        tabs, problems = check.source_setting_tabs("en")
        self.assertEqual(
            tabs,
            {
                "set.tab_display": "Display",
                "set.tab_keys": "Key's",
                "set.tab_tts": "Read aloud",
                "tenant.tab_engine_access": "Engine access",
            },
        )
        self.assertEqual(problems, [])
        self.assertEqual(self.errors(), "")

    def test_url_in_a_value_survives(self):
        values = [v for k, v, _ in check.ts_tokens(self.SETTINGS) if k == "str"]
        self.assertIn("https://example.com/a//b", values)
        self.assertNotIn("set.tab_ghost", values)

    def test_unreadable_tab_value_is_an_error_with_its_place(self):
        (self.locales / "en" / "admin.ts").write_text(
            "export const admin = {\n"
            '  "tenant.tab_engine_access": `Engine ${x}`,\n'
            '  "tenant.tab_cost": COST_LABEL,\n'
            '  "tenant.note": `fine ${x}`,\n'
            '  "tenant.tab_members": "Members" + " list",\n'
            '  "tenant.tab_audit": "Audit" ? "Actual" : "Other",\n'
            '  "tenant.tab_usage": "Usage"}\n'
        )
        out = self.errors()
        self.assertIn("locales/en/admin.ts:2: 'tenant.tab_engine_access' is not a plain string", out)
        self.assertIn("locales/en/admin.ts:3: 'tenant.tab_cost' is not a plain string", out)
        self.assertIn("locales/en/admin.ts:5: 'tenant.tab_members' is not a plain string", out)
        self.assertIn("locales/en/admin.ts:6: 'tenant.tab_audit' is not a plain string", out)
        self.assertNotIn("tenant.note", out)
        check._cache.clear()
        tabs, _ = check.source_setting_tabs("en")
        self.assertNotIn("tenant.tab_members", tabs)
        self.assertNotIn("tenant.tab_audit", tabs)
        # A literal closed by `}` with no trailing comma is still a plain value.
        self.assertEqual(tabs["tenant.tab_usage"], "Usage")

    def test_missing_row_is_an_error(self):
        self.write_rows(self.ROWS[:-1])
        self.assertIn("missing from the table -> Engine access", self.errors())

    def test_no_labels_is_an_error_not_a_skip(self):
        for f in (self.locales / "en").iterdir():
            f.unlink()
        out = self.errors()
        self.assertIn("locales/en/: no settings tab labels", out)
        self.assertIn("so ref/settings.md cannot be checked", out)
        self.assertNotIn("locales/ja/", out)

    def test_missing_locale_dir_is_an_error(self):
        for f in (self.locales / "ja").iterdir():
            f.unlink()
        (self.locales / "ja").rmdir()
        self.assertIn("locales/ja/: no settings tab labels", self.errors())


def rail_tab_keys(path):
    """The i18n keys of the `[section, key]` items in a settings rail source."""
    toks = check.ts_tokens(Path(path).read_text())
    keys = set()
    for i in range(len(toks) - 4):
        shape = [t[:2] if t[0] == "punct" else (t[0],) for t in toks[i : i + 5]]
        if shape == [("punct", "["), ("str",), ("punct", ","), ("str",), ("punct", "]")]:
            if check.SETTING_TAB_KEY_RE.fullmatch(toks[i + 3][1]):
                keys.add(toks[i + 3][1])
    return keys


class RealSettingTabsTests(unittest.TestCase):
    """The real catalogue against the items the two settings rails define."""

    RAILS = ("console/src/features/settings/SettingsDialog.tsx", "console/src/features/settings/tenant/tenantScope.tsx")

    def test_rail_reader_accepts_either_quote_and_skips_comments(self):
        with tempfile.TemporaryDirectory() as tmp:
            src = Path(tmp, "rail.tsx")
            src.write_text(
                "const a = [\n  ['display', 'set.tab_display'],\n  [\"keys\", \"set.tab_keys\"],\n"
                '  // ["ghost", "set.tab_ghost"],\n  tr("set.tab_other"),\n];\n'
            )
            self.assertEqual(rail_tab_keys(src), {"set.tab_display", "set.tab_keys"})

    def test_catalogue_matches_the_rail_items(self):
        # A non-tab key the parser picks up, or a tab it misses, breaks the equality. So does
        # a rail whose items move out of `[section, key]` literals in these two files (a
        # constant, another module): then extend rail_tab_keys, do not pin a key list.
        used = set()
        for rail in self.RAILS:
            keys = rail_tab_keys(Path(check.ROOT, rail))
            self.assertTrue(keys, f"{rail}: no [section, key] rail items found; the rail moved")
            used |= keys
        for locale in ("en", "ja"):
            with self.subTest(locale=locale):
                tabs, problems = check.source_setting_tabs(locale)
                self.assertEqual(problems, [])
                self.assertEqual(set(tabs), used)


class LabelTests(unittest.TestCase):
    """A quoted bold label must exist in the Console catalogue of the same language."""

    EN = (
        "// comment: \"Ghost label\" is only in a comment\n"
        "export const x = {\n"
        "  \"a.stop\": \"Stop\",\n"
        "  \"a.fast\": \"Fast-forward from {ref}\",\n"
        "  \"a.long\":\n"
        "    \"Tidy every stopped session (archived ones can be restored)\",\n"
        "  \"a.any\": \"{count}\",\n"
        "  \"a.from\": \"From {name}\",\n"
        "  \"a.new\": \"New chat\",\n"
        "  /* \"a.dead\": \"Dead label\", */\n"
        "};\n"
    )
    JA = "export const x = {\n  \"a.stop\": \"停止する\",\n  \"a.sel\": \"選択（{count} 件）\",\n};\n"

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        loc = self.root / "console/src/lib/i18n/locales"
        (loc / "en").mkdir(parents=True)
        (loc / "ja").mkdir(parents=True)
        (loc / "en/a.ts").write_text(self.EN)
        (loc / "ja/a.ts").write_text(self.JA)
        self.member = self.root / "guide/member"
        self.member.mkdir(parents=True)
        self.addCleanup(patch.stopall)
        patch.object(check, "ROOT", str(self.root)).start()
        patch.object(check, "LOCALES", str(loc)).start()
        patch.object(check, "LABEL_ALLOWLIST", str(self.root / "allow.txt")).start()

    def errors(self, en="", ja=""):
        (self.member / "01-a.md").write_text(en)
        (self.member / "01-a.ja.md").write_text(ja)
        check._cache.clear()
        findings = check.Findings()
        check.check_labels(sorted(str(p) for p in self.member.glob("*.md")), findings)
        return "\n".join(findings.errors)

    def test_known_labels_pass(self):
        self.assertEqual(
            self.errors(
                en=(
                    'Press **"Stop"**, then **"+ New chat"** or **"Fast-forward from origin/develop"**.\n'
                    'Also **"Tidy every stopped session"** and **"Stop" → "New chat"**.\n'
                    'Wrapped **"Tidy every\nstopped session"** too.\n'
                ),
                ja="「**停止する**」と「**選択（3 件）**」と「**選択**」。\n",
            ),
            "",
        )

    def test_stale_label_is_an_error_per_language(self):
        out = self.errors(en='Press **"Halt"**.\n', ja="「**止める**」を押す。\n")
        self.assertIn("01-a.md:1: bold label '\"Halt\"'", out)
        self.assertIn("01-a.ja.md:1: bold label '止める'", out)

    def test_a_label_is_checked_against_its_own_language(self):
        self.assertIn("停止する", self.errors(en='**"停止する"**\n'))
        self.assertIn("Stop", self.errors(ja="「**Stop**」\n"))

    def test_plain_bold_is_emphasis(self):
        self.assertEqual(self.errors(en="This is **not a label** at all.\n"), "")

    def test_code_and_front_matter_are_skipped(self):
        body = '---\naudience: "**\\"Nope\\"**"\n---\n`**"Nope"**`\n```\n**"Nope"**\n```\n'
        self.assertEqual(self.errors(en=body), "")

    def test_comment_and_dead_catalogue_entries_are_not_labels(self):
        self.assertIn("Ghost label", self.errors(en='**"Ghost label"**\n'))
        self.assertIn("Dead label", self.errors(en='**"Dead label"**\n'))

    def test_a_bare_placeholder_value_accepts_nothing(self):
        # "{count}" and "From {name}" would otherwise match any label or any "From ..." text.
        self.assertIn("Whatever", self.errors(en='**"Whatever"**\n'))
        self.assertIn("From the boss", self.errors(en='**"From the boss"**\n'))
        self.assertEqual(self.errors(en='**"From <name>"**\n'), "")

    def test_shapes_with_nothing_fixed_are_skipped(self):
        self.assertEqual(self.errors(en='**"+N"** and **"≈$X"**\n'), "")

    def test_allowlist_exempts_one_file_and_text(self):
        Path(check.LABEL_ALLOWLIST).write_text("# why\nguide/member/01-a.md: Halt\n")
        self.assertEqual(self.errors(en='**"Halt"**\n'), "")
        self.assertIn("Halt", self.errors(en='**"Halt"**\n', ja="「**Halt**」\n"))

    def test_unreadable_catalogue_is_an_error_not_a_skip(self):
        (Path(check.LOCALES) / "en/a.ts").unlink()
        self.assertIn("no strings could be read", self.errors(en='**"Stop"**\n'))

    def test_a_quote_may_shorten_a_label_but_not_cut_a_word(self):
        self.assertEqual(self.errors(en='**"Tidy every stopped"**\n'), "")
        self.assertIn("Tidy ev", self.errors(en='**"Tidy ev"**\n'))
        self.assertIn("Ne", self.errors(en='**"Ne"**\n'))

    def test_real_catalogue_rejects_a_stale_label_a_loose_pattern_would_accept(self):
        patch.stopall()
        check._cache.clear()
        cat = check.LabelCatalogue("en")
        for stale in ("Delete the safe ones", "Credential", "St", "For", "From the boss"):
            with self.subTest(stale=stale):
                self.assertFalse(check.known_label(cat, stale))
        for fine in ("Credentials", "Delete all safe", "Tidy every stopped session", "Tidy every stopped"):
            with self.subTest(fine=fine):
                self.assertTrue(check.known_label(cat, fine))

    def test_real_guide_has_no_stale_label(self):
        patch.stopall()
        check._cache.clear()
        findings = check.Findings()
        check.check_labels(check.all_docs(), findings)
        self.assertEqual(findings.errors, [])


class SizeAndHeaderTests(unittest.TestCase):
    """The chapter budget warns and never errors; `updated` is no longer a required key."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.shelf = self.root / "guide/member"
        self.shelf.mkdir(parents=True)
        self.addCleanup(patch.stopall)
        patch.object(check, "ROOT", str(self.root)).start()

    def size(self, lines, name="01-a.md"):
        path = self.shelf / name
        path.write_text("x\n" * lines)
        check._cache.clear()
        findings = check.Findings()
        check.check_size([str(path)], findings)
        return findings

    def test_over_budget_warns_only(self):
        findings = self.size(check.CHAPTER_LINE_BUDGET + 1)
        self.assertEqual(findings.errors, [])
        self.assertEqual(len(findings.warns), 1)
        self.assertIn("chapter budget", findings.warns[0])

    def test_at_budget_and_shelf_readme_are_quiet(self):
        self.assertEqual(self.size(check.CHAPTER_LINE_BUDGET).warns, [])
        self.assertEqual(self.size(check.CHAPTER_LINE_BUDGET + 50, "README.md").warns, [])

    def test_strict_run_stays_green_on_a_budget_warning(self):
        # --strict promotes only what a check opts in to; the budget is not one of them.
        (self.shelf / "README.md").write_text('---\naudience: "a"\nsource_of_truth: "b"\n---\n# T\n')
        (self.shelf / "01-a.md").write_text('---\naudience: "a"\n---\n' + "x\n" * 700)
        check._cache.clear()
        with patch.object(sys, "argv", ["docs-check", "--strict", "--only", "size"]), \
                patch.object(check, "all_docs", lambda: [str(self.shelf / "01-a.md")]), \
                patch("builtins.print") as out:
            self.assertEqual(check.main(), 0)
        self.assertIn("chapter budget", str(out.call_args_list))

    def header(self, front):
        for name in ("README.md", "01-a.md"):
            (self.shelf / name).write_text(front + "\n# T\n")
        check._cache.clear()
        findings = check.Findings()
        check.check_header([str(self.shelf / "01-a.md")], findings, False)
        return "\n".join(findings.errors)

    def test_header_needs_audience_and_source_only(self):
        self.assertEqual(self.header('---\naudience: "a"\nsource_of_truth: "b"\n---\n'), "")
        self.assertIn("no source_of_truth", self.header('---\naudience: "a"\n---\n'))


if __name__ == "__main__":
    unittest.main()
