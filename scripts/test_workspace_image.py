#!/usr/bin/env python3
"""Tests for scripts/workspace_image.py against a small fake repository.

    python3 -m unittest scripts/test_workspace_image.py
"""

from __future__ import annotations

import importlib.util
import os
import tempfile
import unittest
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "workspace_image", os.path.join(os.path.dirname(os.path.abspath(__file__)), "workspace_image.py")
)
img = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(img)

DOCKERFILE = """\
ARG GO_VERSION=1.2.3
FROM golang:${GO_VERSION} AS build
FROM node:22-trixie-slim
# ARG COMMENTED_VERSION=9.9.9 is a comment and pins nothing
RUN apt-get update && apt-get install -y --no-install-recommends \\
      git \\
      jq \\
 && ln -sf "$(command -v jq)" /usr/local/bin/jqq
RUN pip install --no-cache-dir uv
ARG GH_VERSION=2.0.0
ARG CHROMIUM_VERSION=150.0.1-1~deb13u1
RUN apt-get install -y "chromium=${CHROMIUM_VERSION}"
COPY gh.sh /usr/local/bin/gh
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
"""

HEADER = "\t".join(img.TOOL_COLUMNS)
ROWS = {
    "gh": "gh\tcli\tgh\tGitHub CLI\thttps://example.org/gh\tMIT\thttps://example.org/gh/LICENSE\targ:GH_VERSION\tGitHub\tGitHub を使う",
    "jq": "jq\tcli\tjqq\tjq\thttps://example.org/jq\tMIT\thttps://example.org/jq/LICENSE\tapt:jq\tJSON\tJSON を加工する",
    "git": "git\tcli\t\tGit\thttps://example.org/git\tGPL-2.0-only\thttps://example.org/git/COPYING\tapt:git\tVCS\tバージョン管理",
    "uv": "uv\tcli\t\tuv\thttps://example.org/uv\tMIT OR Apache-2.0\thttps://example.org/uv/L\tlatest:uv\tPython\tPython",
    "go": "go\trt\t\tGo\thttps://example.org/go\tBSD-3-Clause\thttps://example.org/go/L\targ:GO_VERSION;select\tGo\tGo",
    "node": "node\trt\t\tNode.js\thttps://example.org/node\tMIT\thttps://example.org/node/L\tfrom:node;select\tJS\tJS",
    "java": "java\trt\t\tJava\thttps://example.org/java\tvendor\thttps://example.org/java/terms\tselect\tJava\tJava",
    "chromium": "chromium\trt\t\tChromium\thttps://example.org/c\tBSD-3-Clause\thttps://example.org/c/L\tdeb:CHROMIUM_VERSION\tBrowser\tブラウザ",
}


class WorkspaceImageTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.dockerfile = DOCKERFILE
        self.rows = dict(ROWS)
        self.write()

    def write(self):
        (self.root / "workspace").mkdir(exist_ok=True)
        (self.root / img.DOCKERFILE).write_text(self.dockerfile)
        tsv = self.root / img.TOOLS_TSV
        tsv.parent.mkdir(parents=True, exist_ok=True)
        tsv.write_text(HEADER + "\n" + "".join(r + "\n" for r in self.rows.values()))
        (self.root / img.GROUPS_TSV).write_text("group\ten\tja\ncli\tCLIs\tCLI\nrt\tRuntimes\tランタイム\n")
        (self.root / "guide/ref").mkdir(parents=True, exist_ok=True)

    def generate(self):
        for lang, rel in img.PAGES.items():
            (self.root / rel).write_text(img.render(str(self.root), lang))

    def problems(self):
        return "\n".join(img.problems(str(self.root)))

    def test_generated_pages_are_fresh(self):
        self.assertEqual(self.problems(), "")
        self.generate()
        self.assertEqual(img.stale_pages(str(self.root)), [])
        en = (self.root / img.PAGES["en"]).read_text()
        self.assertIn("| [GitHub CLI](https://example.org/gh) | GitHub | `2.0.0` | [MIT](https://example.org/gh/LICENSE) |", en)
        self.assertIn("| Debian 13 (trixie) package |", en)
        self.assertIn("Latest at image build", en)
        self.assertIn("image default `1.2.3`", en)
        self.assertIn("image default `22`", en)
        self.assertIn("| `150.0.1` |", en)
        self.assertIn("[Vendor terms](https://example.org/java/terms)", en)
        ja = (self.root / img.PAGES["ja"]).read_text()
        self.assertIn("イメージの既定は `22`", ja)
        self.assertIn("[提供元の規約](https://example.org/java/terms)", ja)

    def test_stale_page(self):
        self.generate()
        self.dockerfile = self.dockerfile.replace("GH_VERSION=2.0.0", "GH_VERSION=2.0.1")
        self.write()
        self.assertEqual(sorted(img.stale_pages(str(self.root))), sorted(img.PAGES.values()))

    def test_bin_without_a_row(self):
        del self.rows["gh"]
        self.write()
        out = self.problems()
        self.assertIn("installs /usr/local/bin/gh, which no row lists", out)
        self.assertIn("pins ARG GH_VERSION, which no row's version reads", out)
        self.assertNotIn("entrypoint.sh", out)
        self.assertNotIn("COMMENTED_VERSION", out)

    def test_unreadable_version_source(self):
        self.rows["gh"] = self.rows["gh"].replace("arg:GH_VERSION", "arg:GONE_VERSION")
        self.rows["git"] = self.rows["git"].replace("apt:git", "apt:mercurial")
        self.rows["node"] = self.rows["node"].replace("from:node", "from:python")
        self.rows["uv"] = self.rows["uv"].replace("latest:uv", "latest:poetry")
        self.write()
        out = self.problems()
        self.assertIn("has no `ARG GONE_VERSION=<default>`", out)
        self.assertIn("installs no apt package 'mercurial'", out)
        self.assertIn("not python:<major>", out)
        self.assertIn("no unpinned `pip install` of 'poetry'", out)

    def test_bad_licence_and_unknown_group(self):
        self.rows["jq"] = self.rows["jq"].replace("\tMIT\t", "\tprobably MIT\t").replace("jq\tcli", "jq\tnope", 1)
        self.write()
        out = self.problems()
        self.assertIn("is not an SPDX expression", out)
        self.assertIn("names group 'nope'", out)

    def test_main_check_exit_code(self):
        import contextlib
        import io
        import sys

        self.generate()
        argv = sys.argv
        try:
            sys.argv = ["workspace_image.py", "--check", "--root", str(self.root)]
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(img.main(), 0)
                del self.rows["gh"]
                self.write()
                self.assertEqual(img.main(), 1)
        finally:
            sys.argv = argv


if __name__ == "__main__":
    unittest.main()
