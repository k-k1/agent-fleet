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


if __name__ == "__main__":
    unittest.main()
