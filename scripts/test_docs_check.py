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


if __name__ == "__main__":
    unittest.main()
