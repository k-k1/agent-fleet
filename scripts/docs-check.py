#!/usr/bin/env python3
"""Structural checks over the documentation trees. Runs in CI
(.github/workflows/docs.yml) and locally with the same command.

The documentation splits by reader (ADR 0064): the product introduction is the root
README, `docs/` is for developers, `guide/` is the user guide. Only `guide/` is shipped
into containers, so the directory boundary *is* the distribution boundary. When that
boundary erodes, links break silently in the reader's copy, so the conventions are
machine-checked here rather than left to human review.

Twelve checks:

  links      relative links resolve (anchors ignored)
  anchors    a #fragment points at a heading that exists (matched with Console's slug rule)
  closure    no link out of guide/ — the shipped tree is self-contained
  chapters   chapter numbers agree with the file name and with cross-reference labels
  lang       bilingual closure (en links to .md, ja to .ja.md) and the counterpart exists
  header     every file on a living shelf has front matter (audience / source_of_truth / updated)
  vocab      no implementation vocabulary (AF_* / kind= / /api/) on reader-facing shelves
  frozen     no link from a living shelf into docs/log/ (the frozen archive)
  ref        ref/ tables agree with the source of truth in code, and the ✓ marks match the
             translation
  settings   the settings-tab chapter (member/12-settings) covers the tab list (ref/settings)
  features   member rows of the feature catalogue point at a procedure on the member/ shelf
  knowledge  the assistant knowledge covers the member rows of the feature catalogue
  notes      the operating policy shipped to every container points only at shipped shelves

ref is checked at three levels. (a) Axis coverage: the agent columns cover the session
kind constants, the deployment rows cover the runtime profiles. (b) Row agreement:
capabilities expressed by Caps() must match exactly (not ⊇ — marking a capability ✓ that
is not set is the worst kind of lie). (c) Translation agreement: the ✓ marks are in the
same places in en and ja. Checking only that a translation exists does not stop a
translation whose content has drifted, and a capability table that is stale on one side
does the same harm as having two tables.

`--strict` promotes warnings to errors, so shelves still being migrated can be tightened
in stages.
"""

from __future__ import annotations

import argparse
import os
import re
import sys
import unicodedata
from dataclasses import dataclass, field

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# --- the two trees ------------------------------------------------------------
# The documentation splits by reader (ADR 0064). The product introduction is the root
# README; the other two are the trees checked here.
#
#   guide/ … the user guide. The only tree shipped into containers, and everyone gets
#            the same copy.
#   docs/  … for developers. Shipped to nobody (decisions / log / build / conventions).
#
# The whole point of the split is that the distribution boundary coincides with the
# directory boundary. A link from guide/ into docs/ is always broken in the reader's
# copy (check_closure).
DOCS = os.path.join(ROOT, "docs")
GUIDE = os.path.join(ROOT, "guide")
TREES = (GUIDE, DOCS)

# --- shelf classification -----------------------------------------------------
# Living = shelves that all the conventions apply to (header / lang / vocab / frozen /
# anchors). Shelf names are unique across both trees, so a shelf is identified without
# saying which tree it is in.
LIVING = ("member", "admin", "operate", "ref", "build")
# Reader-facing = shelves that must not use implementation vocabulary. operate/ is
# written for a reader at a terminal, so commands, paths and variables are fine there,
# and ref/ is the shelf that carries the mapping table itself (screen column = Console,
# implementation column = code); neither belongs here (CONVENTIONS §4).
READER_FACING = ("member", "admin")
# Shelves of the guide/ tree, i.e. what is shipped into containers. Not cut by role
# (ADR 0064).
GUIDE_SHELVES = ("member", "admin", "operate", "ref")
# Bilingual = English is authoritative (X.md), Japanese accompanies it (X.ja.md).
# decisions/ is not LIVING (an ADR is immutable, so it carries no Updated:) but it is
# bilingual: as with the reader-based shelves, an English-only reader who cannot reach
# the reasoning behind a decision is the same gap.
BILINGUAL = LIVING + ("decisions",)
# Japanese-only = out of scope for the bilingual checks. log/ is the frozen archive.
JA_ONLY_DIRS = ("log",)
JA_ONLY_FILES = (
    "docs/HANDOFF.md",
    "docs/CHANGELOG-handoff.md",
    "docs/roadmap.md",
)

# Living files that are allowed to reference log/.
FROZEN_REF_ALLOWLIST: set[str] = {
    "docs/log/README.md",
}

LINK_RE = re.compile(r"(?<!!)\[([^\]]*)\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
# Front matter (YAML fenced by ---). Values must always be double-quoted: a
# `Source of truth` value contains colons and commas, e.g. "the commands are the scripts
# under deploy/, …".
FM_RE = re.compile(r"^---\n(.*?)\n---\s*\n", re.S)
FM_KEY_RE = re.compile(r'^([a-z_]+):\s*"(.*)"\s*$', re.M)
FM_KEYS = ("audience", "source_of_truth", "updated")
UPDATED_RE = re.compile(r"^\d{4}-\d{2}$")

# Implementation vocabulary that must not appear on reader-facing shelves. The brake
# that keeps those shelves written in the names shown on screen.
VOCAB_BANNED = (
    (re.compile(r"\bAF_[A-Z][A-Z0-9_]+"), "an env variable name"),
    (re.compile(r"\bkind=[a-z]"), "an internal kind identifier"),
    (re.compile(r"(?<![\w/])/api/[a-z]"), "an API path"),
    (re.compile(r"(?<![\w/])/internal/[a-z]"), "an internal API path"),
)


@dataclass
class Findings:
    errors: list[str] = field(default_factory=list)
    warns: list[str] = field(default_factory=list)

    def error(self, msg: str) -> None:
        self.errors.append(msg)

    def warn(self, msg: str) -> None:
        self.warns.append(msg)


def rel(path: str) -> str:
    """Repository-relative path (`guide/member/01-first-day.md`).

    With two trees, returning only the shelf leaves `README.md` ambiguous about which
    tree it belongs to. Error messages stay in a form that can be handed straight to
    `git`.
    """
    return os.path.relpath(path, ROOT).replace(os.sep, "/")


def tree(relpath: str) -> str:
    """`guide` or `docs`. This is what decides whether a file is shipped."""
    return relpath.split("/", 1)[0]


def shelf(relpath: str) -> str:
    """Shelf name (`member` / `build` / `log` …). Empty for a file directly in a tree."""
    parts = relpath.split("/")
    return parts[1] if len(parts) > 2 else ""


def is_ja(relpath: str) -> bool:
    return relpath.endswith(".ja.md")


def counterpart(relpath: str) -> str:
    """Swap between the en and ja file names."""
    if is_ja(relpath):
        return relpath[: -len(".ja.md")] + ".md"
    return relpath[: -len(".md")] + ".ja.md"


def all_docs() -> list[str]:
    out = []
    for base in TREES:
        for dirpath, dirnames, filenames in os.walk(base):
            dirnames[:] = [d for d in dirnames if not d.startswith(".")]
            for name in filenames:
                if name.endswith(".md"):
                    out.append(os.path.join(dirpath, name))
    return sorted(out)


def bilingual_scope(relpath: str) -> bool:
    s = shelf(relpath)
    if s in JA_ONLY_DIRS or relpath in JA_ONLY_FILES:
        return False
    if not s:  # directly in a tree: only README / CONVENTIONS are bilingual
        return os.path.basename(relpath).split(".")[0] in ("README", "CONVENTIONS")
    return s in BILINGUAL


# --- checks -------------------------------------------------------------------


def check_links(files: list[str], f: Findings) -> None:
    for path in files:
        src = rel(path)
        body = strip_code(read(path))
        for m in LINK_RE.finditer(body):
            target = m.group(2)
            # A leading "/" is a site-absolute URL (e.g. an example of the open link
            # the Console returns), not a path inside the repository.
            if target.startswith(("http://", "https://", "mailto:", "#", "/")):
                continue
            target = target.split("#", 1)[0]
            if not target:
                continue
            resolved = os.path.normpath(
                os.path.join(os.path.dirname(path), target)
            )
            # Targets outside docs (../../deploy/... and the like) are resolved by the
            # same rule.
            if os.path.exists(resolved):
                continue
            f.error(f"{src}: broken link -> {m.group(2)}")


# --- anchors ------------------------------------------------------------------

INLINE_MARKUP = (
    (re.compile(r"\[([^\]]*)\]\([^)]*\)"), r"\1"),  # a link keeps only its visible text
    (re.compile(r"`([^`]*)`"), r"\1"),
    (re.compile(r"\*\*([^*]*)\*\*"), r"\1"),
    (re.compile(r"\*([^*]*)\*"), r"\1"),
)
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*?)\s*$", re.M)


def heading_text(raw: str) -> str:
    """Build, from a heading line, the same string a browser sees as `textContent`."""
    for pattern, repl in INLINE_MARKUP:
        raw = pattern.sub(repl, raw)
    return raw


def console_slug(text: str) -> str:
    """The id the Console assigns to a heading. This is not GitHub's rule.

    The source of truth is `slug()` in `console/src/lib/filemeta.ts`: lowercase and trim,
    drop everything that is not a letter, digit, space or hyphen, and collapse a *run* of
    spaces into a single hyphen.

    GitHub (github-slugger) emits one hyphen per space, so the two rules disagree on any
    heading containing a symbol surrounded by spaces, such as `—` or `/` (`a — b` is
    `a-b` in the Console and `a--b` on GitHub). Fullwidth parentheses go the other way:
    the Console drops them, GitHub keeps them.

    Which rule wins was decided by measurement: across the repository, 52 links resolve
    only under the Console rule and 10 only under GitHub's. The Console is also where
    readers open the guide (`Source of truth` is the Console), so it takes both the
    majority and the reader.
    """
    t = text.lower().strip()
    t = "".join(
        c for c in t if unicodedata.category(c)[0] in ("L", "N") or c in " -"
    )
    return re.sub(r"\s+", "-", t)


# github-slugger: lowercase and trim, drop punctuation (the general-punctuation range,
# which includes `—`, plus the supplemental-punctuation range and ASCII symbols),
# and emit one hyphen per space. `-`, `_` and fullwidth parentheses survive.
GITHUB_PUNCT_RE = re.compile(
    "[ -⁯⸀-⹿\\\\'!\"#$%&()*+,./:;<=>?@\\[\\]^`{|}~]"
)


def github_slug(text: str) -> str:
    return re.sub(r"\s", "-", GITHUB_PUNCT_RE.sub("", text.lower().strip()))


def heading_slugs(path: str) -> set[str]:
    """The document's heading ids, built with the rule of whatever renders `path`.

    The rule is decided by the destination tree. `guide/` is opened by readers in the
    container's Console, so it uses the Console's `slug()`; `docs/` and files at the
    repository root (CONTRIBUTING.md and the like) are only ever read on GitHub, so they
    use github-slugger. Applying one rule to both reports anchors that are correct on
    GitHub, such as `CONTRIBUTING.md#commits--prs`, as broken — which is what happened.
    """
    rule = (
        console_slug
        if path.startswith(GUIDE + os.sep)
        else github_slug
    )
    return {
        rule(heading_text(m.group(2)))
        for m in HEADING_RE.finditer(strip_code(read(path)))
    }


def check_anchors(files: list[str], f: Findings) -> None:
    """Does a `#fragment` point at a heading that exists in the destination file?

    `check_links` only checks that the file exists and ignores the anchor, so "the page
    opens but lands somewhere else" — indistinguishable from a broken link to the reader
    — used to pass unnoticed.
    """
    for path in files:
        src = rel(path)
        if shelf(src) not in LIVING:
            continue
        for m in LINK_RE.finditer(strip_code(read(path))):
            target = m.group(2)
            if target.startswith(("http://", "https://", "mailto:", "/")):
                continue
            if "#" not in target:
                continue
            p, _, frag = target.partition("#")
            if not frag:
                continue
            dest = (
                path
                if not p
                else os.path.normpath(os.path.join(os.path.dirname(path), p))
            )
            if not dest.endswith(".md") or not os.path.exists(dest):
                continue  # a missing file is check_links' business
            if frag in heading_slugs(dest):
                continue
            who = "Console" if dest.startswith(GUIDE + os.sep) else "GitHub"
            f.error(
                f"{src}: anchor with no matching heading -> {target}"
                f" (does not match the id {who} assigns)"
            )


# --- closure of the shipped tree ----------------------------------------------

# Links that may point out of guide/ (prefix -> reason).
# State the reason explicitly. Before adding one, confirm that the reader can actually
# reach the target from inside the container; if they cannot, it is not an exception but
# a link to fix.
_RUNBOOK_REASON = (
    "a runbook sits next to what it operates and is part of the release bundle itself. "
    "deploy/release/stage-docs.sh copies it into operate/runbooks/ when shipping and "
    "rewrites this link to point there, so it stays live both ways: deploy/ on GitHub, "
    "runbooks/ in the container"
)
# Listed as individual paths. This used to be the prefix `deploy/`, which exempted the
# six links to `deploy/compose/.env.example` along with the five runbooks that are
# actually rewritten, leaving links that are dead inside the shipped tree reported as
# green. An exception is only kept in a form that explains, one by one, why the target
# is reachable.
CLOSURE_EXEMPT: dict[str, str] = {
    "deploy/compose/README.md": _RUNBOOK_REASON,
    "deploy/native/README.md": _RUNBOOK_REASON,
    "deploy/local/README-wsl.md": _RUNBOOK_REASON,
    "deploy/aws/ecs/README.md": _RUNBOOK_REASON,
    "deploy/aws/ec2-single/README.md": _RUNBOOK_REASON,
}


def check_closure(files: list[str], f: Findings) -> None:
    """Is the shipped tree (guide/) self-contained — not one link pointing outside it?

    This was the real cause of readers reporting "lots of broken links". `check_links`
    only checks existence in the repository, so a link from `guide/` into the developer
    tree `docs/` passes green. But what the reader opens is the tree shipped into the
    container, and `docs/` is not there. A whole family of links that exist in the
    repository yet always break in the reader's copy survived that way.

    Prose mentions are out of scope: only links are checked. Writing "the mechanism is
    described in the developer documentation" is correct; making it clickable is the
    error.
    """
    for path in files:
        src = rel(path)
        if tree(src) != "guide":
            continue
        for m in LINK_RE.finditer(strip_code(read(path))):
            target = m.group(2)
            if target.startswith(("http://", "https://", "mailto:", "#", "/")):
                continue
            p = target.split("#", 1)[0]
            if not p:
                continue
            dest = os.path.normpath(os.path.join(os.path.dirname(path), p))
            if dest == GUIDE or dest.startswith(GUIDE + os.sep):
                continue
            out = os.path.relpath(dest, ROOT).replace(os.sep, "/")
            if out in CLOSURE_EXEMPT:
                continue
            f.error(
                f"{src}: points outside the shipped tree -> {target} ({out})"
                " — only guide/ is shipped into containers, so this breaks for the reader"
            )


# --- chapter numbers ----------------------------------------------------------

CHAPTER_FILE_RE = re.compile(r"^(\d{2})-")
# The "NN." of an H1, and the "NN <chapter name>" of a cross-reference label in the body.
# Both must give the same number.
H1_NUM_RE = re.compile(r"^#\s+(\d{1,2})\.\s")
LABEL_NUM_RE = re.compile(r"^(\d{1,2})[.\s]")


def chapter_of(relpath: str) -> str | None:
    m = CHAPTER_FILE_RE.match(os.path.basename(relpath))
    return m.group(1) if m else None


def check_chapters(files: list[str], f: Findings) -> None:
    """Do the chapter numbers agree with each other?

    A numbered file has a numbered H1 and is referred to from other chapters as
    "NN <chapter name>". When the three disagree, the reader sees "11 Troubleshooting"
    in the index and "09. Troubleshooting" on the page — which happened, with
    `09-collaboration` and `11-troubleshooting` both claiming 09. The numbers are the
    table of contents, not decoration, so they are reconciled mechanically with the file
    name as the source of truth.
    """
    numbers = {rel(p): chapter_of(rel(p)) for p in files}
    for path in files:
        src = rel(path)
        if shelf(src) not in LIVING:
            continue
        want = numbers[src]
        body = read(path)
        # Front matter comes first, so the H1 is the first `# ` line, not the first line.
        h1 = next((ln for ln in body.splitlines() if ln.startswith("# ")), "")
        got = H1_NUM_RE.match(h1)
        if want and not got:
            f.error(f"{src}: H1 has no chapter number (start it with \"# {want}. …\")")
        elif want and got.group(1).zfill(2) != want:
            f.error(
                f"{src}: H1 chapter number differs from the file name"
                f" (H1={got.group(1)} / file name={want})"
            )
        elif not want and got:
            f.error(
                f"{src}: unnumbered file carries a chapter number (H1={got.group(1)})"
            )
        # Does the cross-reference label "NN <chapter name>" match the number of the
        # chapter it points at?
        for m in LINK_RE.finditer(strip_code(body)):
            label, target = m.group(1), m.group(2).split("#", 1)[0]
            lm = LABEL_NUM_RE.match(label.strip())
            if not lm or not target.endswith(".md"):
                continue
            dest = os.path.normpath(os.path.join(os.path.dirname(path), target))
            if not os.path.exists(dest):
                continue  # check_links' business
            dest_num = chapter_of(rel(dest))
            if dest_num and lm.group(1).zfill(2) != dest_num:
                f.error(
                    f"{src}: wrong chapter number in a cross-reference"
                    f" -> [{label}]({target}) (the target is {dest_num})"
                )


def check_lang(files: list[str], f: Findings) -> None:
    present = {rel(p) for p in files}
    for path in files:
        src = rel(path)
        if not bilingual_scope(src):
            continue
        mate = counterpart(src)
        if mate not in present:
            f.error(f"{src}: no translation ({mate} is required)")
        body = strip_code(read(path))
        for m in LINK_RE.finditer(body):
            target = m.group(2).split("#", 1)[0]
            if target.startswith(("http://", "https://", "mailto:")) or not target:
                continue
            if not target.endswith(".md"):
                continue
            dest = os.path.normpath(os.path.join(os.path.dirname(path), target))
            if not any(dest.startswith(b + os.sep) for b in TREES):
                continue  # outside both trees (deploy/ etc.) there is no language
            dest_rel = rel(dest)
            if not bilingual_scope(dest_rel):
                continue  # both languages point at the same target on a ja-only shelf
            # The language switcher right after the H1 is the one legitimate
            # cross-language link.
            if dest_rel == mate:
                continue
            if is_ja(src) != is_ja(dest_rel):
                want = counterpart(dest_rel)
                f.error(
                    f"{src}: link crosses languages -> {target} (point at {want})"
                )


def front_matter(path: str) -> dict[str, str] | None:
    """The leading YAML front matter as key -> value, or None if there is none.

    The Console splits front matter off from the body and renders it in a metadata frame
    (`splitYamlFrontMatter` in `console/src/features/viewer/MarkdownView.tsx`), so the
    lines meant for machines never mix into the prose. This reads it as structure too,
    not by string matching.
    """
    m = FM_RE.match(read(path))
    if m is None:
        return None
    return dict(FM_KEY_RE.findall(m.group(1)))


def is_shelf_readme(relpath: str) -> bool:
    return os.path.basename(relpath) in ("README.md", "README.ja.md")


def check_header(files: list[str], f: Findings, strict: bool) -> None:
    """Does every file on a living shelf have front matter?

    `source_of_truth` alone may be inherited from the shelf README. The 16 files of
    `guide/member/` and the 6 of `guide/admin/` carry a value that is word-for-word
    identical, so the same sentence appeared 22 times — boilerplate carrying no
    information for the reader. Where the value differs per file, as in `guide/ref/`
    (all 10 differ) and `guide/operate/`, it is what tells a reader who meets a
    contradiction which side to believe, so it is written in each file.
    """
    defaults: dict[tuple[str, bool], dict[str, str]] = {}
    for path in files:
        src = rel(path)
        if shelf(src) in LIVING and is_shelf_readme(src):
            defaults[(shelf(src), is_ja(src))] = front_matter(path) or {}

    for path in files:
        src = rel(path)
        if shelf(src) not in LIVING:
            continue
        fm = front_matter(path)
        if fm is None:
            f.error(
                f"{src}: no front matter at the top of the file"
                " (fence it with --- and give audience / source_of_truth / updated)"
            )
            continue
        inherited = (
            {} if is_shelf_readme(src)
            else defaults.get((shelf(src), is_ja(src)), {})
        )
        for key in FM_KEYS:
            if key in fm:
                continue
            if key == "source_of_truth" and key in inherited:
                continue  # the shelf README declares it on their behalf
            f.error(f"{src}: front matter has no {key}")
        if "updated" in fm and not UPDATED_RE.match(fm["updated"]):
            f.error(f"{src}: updated must be YYYY-MM (currently {fm['updated']!r})")


def check_vocab(files: list[str], f: Findings, strict: bool) -> None:
    for path in files:
        src = rel(path)
        if shelf(src) not in READER_FACING:
            continue
        body = strip_code(read(path))
        for pattern, label in VOCAB_BANNED:
            hit = pattern.search(body)
            if hit:
                msg = (
                    f"{src}: {label} appears on a reader-facing shelf"
                    f" ({hit.group(0)})"
                )
                (f.error if strict else f.warn)(msg)


def check_frozen(files: list[str], f: Findings) -> None:
    for path in files:
        src = rel(path)
        if shelf(src) not in LIVING:
            continue
        if src in FROZEN_REF_ALLOWLIST:
            continue
        for m in LINK_RE.finditer(strip_code(read(path))):
            target = m.group(2)
            dest = os.path.normpath(os.path.join(os.path.dirname(path), target))
            if dest.startswith(os.path.join(DOCS, "log")):
                f.error(
                    f"{src}: references the frozen archive log/ -> {target}"
                    " (copy the fact into a current document instead)"
                )


# --- check the ref/ axes against the source of truth in code -------------------


def source_kinds() -> set[str]:
    path = os.path.join(
        ROOT, "workspace", "agent", "internal", "session", "session.go"
    )
    body = read(path)
    return set(re.findall(r'^\s*Kind\w+\s*=\s*"([a-z]+)"', body, re.M))


def source_caps() -> dict[str, set[str]]:
    """kind -> the set of field names its Caps() sets to true.

    Only the body of the Caps() method under workspace/agent/internal/agents/<kind>/ is
    read. That is the implementation's source of truth for what a kind can do, and the
    corresponding row of ref/agents.md must agree with it.
    """
    base = os.path.join(ROOT, "workspace", "agent", "internal", "agents")
    out: dict[str, set[str]] = {}
    if not os.path.isdir(base):
        return out
    for kind in sorted(os.listdir(base)):
        d = os.path.join(base, kind)
        if not os.path.isdir(d):
            continue
        fields: set[str] = set()
        for name in sorted(os.listdir(d)):
            if not name.endswith(".go") or name.endswith("_test.go"):
                continue
            body = read(os.path.join(d, name))
            for m in re.finditer(r"\)\s*Caps\(\)\s*(?:agents\.)?Caps\s*\{", body):
                # The method body runs to the first "\n}". Caps only returns a plain
                # struct literal, so that is both sufficient and free of false hits.
                end = body.find("\n}", m.end())
                blk = body[m.end() : end if end > 0 else len(body)]
                fields |= set(re.findall(r"(\w+)\s*:\s*true", blk))
        if fields:
            out[kind] = fields
    return out


def table_check_marks(path: str, row_label: str) -> set[str] | None:
    """Column names marked ✓ in the table row row_label, or None if the row is absent."""
    header: list[str] = []
    for line in read(path).splitlines():
        line = line.strip()
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        if not header:
            header = [c.strip("`*") for c in cells]
            continue
        if cells[0].startswith(row_label):
            return {
                header[i]
                for i, c in enumerate(cells)
                if i < len(header) and c.startswith("✓")
            }
    return None


# File and function name holding the switch over runtime forms. The location moves when
# code is relocated: the alias relocation of ADR 0067 moved newRuntimeFactory from
# control-plane/runtime.go to NewFactory in control-plane/internal/runtime/runtime.go,
# leaving a thin wrapper in main that only calls runtime.NewFactory. Tried newest first.
RUNTIME_FACTORY_SOURCES = (
    (("control-plane", "internal", "runtime", "runtime.go"), "func NewFactory("),
    (("control-plane", "runtime.go"), "func newRuntimeFactory("),
)


def source_runtime_groups() -> list[set[str]]:
    """Deployment forms, returned as groups of aliases.

    The runtime factory's switch allows several spellings for one adapter (local=docker /
    ecs=aws / native=wsl). Whichever spelling the table picked should pass, so a group
    counts as satisfied when any one of its members is present. The groups come from that
    switch, the required set from the "want ..." error message in the same function.

    Fail hard when it cannot be found. Returning [] here silently removes just the
    deploy-targets.md check, leaving a rotten table green — and since this check is
    exactly the one that breaks when files are relocated, being quietly disabled is the
    most expensive outcome.
    """
    for parts, fn in RUNTIME_FACTORY_SOURCES:
        path = os.path.join(ROOT, *parts)
        if not os.path.exists(path):
            continue
        body = read(path)
        start = body.find(fn)
        if start >= 0:
            break
    else:
        raise SystemExit(
            "docs-check: cannot find the switch over runtime forms. If it was relocated,"
            " add the new location to RUNTIME_FACTORY_SOURCES in scripts/docs-check.py"
            " (looked in: "
            + ", ".join(f + " in " + "/".join(p) for p, f in RUNTIME_FACTORY_SOURCES)
            + ")"
        )
    end = body.find("\nfunc ", start + 1)
    scope = body[start : end if end > 0 else len(body)]
    groups = [
        {a.strip().strip('"') for a in labels.split(",")}
        for labels in re.findall(r"case\s+(\"[^\n:]*)\s*:", scope)
    ]
    groups = [{a for a in g if a} for g in groups]
    want = re.search(r"want ([a-z0-9|-]+)", scope)
    required = set(want.group(1).split("|")) if want else set()
    # Only groups that touch the required set are checked.
    return [g for g in groups if g & required]


def table_columns(path: str) -> set[str]:
    """Cells of the first table's header row, excluding the first column."""
    for line in read(path).splitlines():
        line = line.strip()
        if line.startswith("|") and line.count("|") >= 3:
            cells = [c.strip() for c in line.strip("|").split("|")]
            return {c for c in cells[1:] if c}
    return set()


def table_first_column(path: str) -> set[str]:
    out = set()
    for line in read(path).splitlines():
        line = line.strip()
        if not line.startswith("|") or set(line) <= set("|-: "):
            continue
        cell = line.strip("|").split("|")[0].strip()
        if cell:
            out.add(cell)
    return out


def source_setting_tabs(locale: str) -> dict[str, str]:
    """Console settings tab labels (key -> displayed string).

    A user looks for the name shown on screen, so a row of ref/settings.md must be the
    Console label verbatim. A new tab means a new row.
    """
    path = os.path.join(
        ROOT, "console", "src", "lib", "i18n", "locales", f"{locale}.ts"
    )
    if not os.path.exists(path):
        return {}
    return {
        m.group(1): m.group(2)
        for m in re.finditer(
            r'"((?:set|tenant)\.tab_[a-z_]+)":\s*"([^"]+)"', read(path)
        )
    }


def table_mark_shape(path: str) -> list[tuple[str, ...]]:
    """Every table in the file flattened to marks only, preserving row order.

    Each cell becomes one of three values: ✓, — or "." (prose). The normalisation exists
    so that comparison ignores the wording of a translation and looks only at where the
    ✓ marks are.
    """
    shape: list[tuple[str, ...]] = []
    for line in read(path).splitlines():
        line = line.strip()
        if not line.startswith("|") or set(line) <= set("|-: "):
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        shape.append(
            tuple(
                "✓" if c.startswith("✓") else "—" if c.startswith("—") else "."
                for c in cells
            )
        )
    return shape


def check_knowledge(f: Findings) -> None:
    """Does the assistant knowledge cover the member rows of the feature catalogue?

    `workspace/agent/knowledge/af-usage.md` summarises `docs/use/` and is baked into the
    agent binary with //go:embed. It cannot be generated: the agent image's build context
    is only `workspace/agent/`, so `docs/` is not visible, and the file is not
    concatenated prose but a prompt folded into the order it should be read in.

    Being hand-copied it will always lag, so make the lag visible. What it is checked
    against are the member rows of ref/features.ja.md; admin and operator rows have a
    different reader and are outside this assistant's remit.

    What can be checked is only whether a record exists saying someone decided to write
    about a feature, not whether what they wrote is correct. The possibility that the
    ledger merely claims coverage cannot be ruled out — what can be ruled out is a new
    catalogue row that nobody looked at.
    """
    cat = os.path.join(GUIDE, "ref", "features.ja.md")
    doc = os.path.join(ROOT, "workspace", "agent", "knowledge", "af-usage.md")
    led = os.path.join(ROOT, "workspace", "agent", "knowledge", "af-usage.coverage.tsv")
    if not (os.path.exists(cat) and os.path.exists(doc) and os.path.exists(led)):
        return

    # Member rows of the catalogue: the "who" column starts with the Japanese word
    # for member (「メンバー」), which is what the catalogue is written in.
    wanted: list[str] = []
    for line in read(cat).splitlines():
        line = line.strip()
        if not line.startswith("|") or set(line) <= set("|-: "):
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        if not cells[0] or cells[0] == "機能":
            continue
        if len(cells) > 1 and cells[1].startswith("メンバー"):
            wanted.append(cells[0])

    ledger: dict[str, str] = {}
    for i, line in enumerate(read(led).splitlines(), 1):
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if "\t" not in line:
            f.error(f"af-usage.coverage.tsv:{i}: not tab-separated -> {line!r}")
            continue
        name, where = line.split("\t", 1)
        ledger[name.strip()] = where.strip()

    sections = set(re.findall(r"^##\s+(\d+)\.", read(doc), re.M))

    for name in wanted:
        if name not in ledger:
            f.error(
                f"af-usage.coverage.tsv: feature catalogue row missing from the ledger"
                f" -> '{name}'"
                " (write it into the assistant knowledge, or give the reason for not"
                " writing it after a `-`)"
            )
            continue
        where = ledger[name]
        if where.startswith("-"):
            if not where[1:].strip():
                f.error(
                    f"af-usage.coverage.tsv: give a reason for excluding '{name}'"
                )
        elif where not in sections:
            f.error(
                f"af-usage.coverage.tsv: '{name}' points at a section that does not"
                f" exist in af-usage.md -> {where}"
            )

    for name in ledger:
        if name not in wanted:
            f.error(
                f"af-usage.coverage.tsv: ledger row absent from the feature catalogue"
                f" -> '{name}' (it was removed or renamed in the catalogue)"
            )


def table_first_column_under(path: str, heading: str) -> list[str]:
    """First column of the table inside the `## <heading>` section, in order, no header.

    ref/settings.md holds three tables (personal settings / tenant settings / deployment
    variables), so collecting from the whole file would demand that use/ also cover the
    tenant settings tabs. Those belong to admin/ and have a different reader.
    """
    rows: list[str] = []
    inside = False
    header_seen = False
    for line in read(path).splitlines():
        s = line.strip()
        if s.startswith("## "):
            if inside:
                break
            inside = s[3:].strip() == heading
            continue
        if not inside or not s.startswith("|"):
            continue
        if set(s) <= set("|-: "):
            continue
        cell = s.strip("|").split("|")[0].strip()
        if not header_seen:  # the table's header row
            header_seen = True
            continue
        if cell:
            rows.append(cell)
    return rows


def heading3_under(path: str, groups: tuple[str, ...]) -> list[str]:
    """The `###` headings under a group heading (`## <one of groups>`), in order.

    A `###` outside a group (such as "when it takes effect") is not a tab, so it is not
    collected.
    """
    out: list[str] = []
    inside = False
    for line in read(path).splitlines():
        s = line.strip()
        if s.startswith("## "):
            inside = s[3:].strip() in groups
            continue
        if inside and s.startswith("### "):
            out.append(s[4:].strip())
    return out


# Personal settings tabs of ref/settings.ja.md that need no section in
# use/12-settings.ja.md (tab name -> reason). Every exemption states its reason: an
# exemption list that can be extended without one fills up with "it failed, so I removed
# it" and the check goes back to passing everything. Currently empty.
USE_SETTINGS_EXEMPT: dict[str, str] = {}

# Group headings matched in the documents, so these stay in the documents' own language.
USE_SETTINGS_GROUPS_JA = ("個人設定", "接続", "ワークスペース")
USE_SETTINGS_GROUPS_EN = ("Personal", "Connections", "Workspace")


def check_use_settings(f: Findings) -> None:
    """Does the settings-tab chapter (use/12-settings) cover the tab list (ref/settings)?

    check_ref already matches the rows of ref/settings against the Console labels, so a
    new screen always adds a row to ref/. Nothing checked use/, though, so ref/ could be
    correct while use/ had a hole and everything still passed — in practice the work-item
    and cloud-cost tabs were missing in both languages, while ref/features pointed at
    those empty seats as "see 12 Settings". This adds that missing step.

    The name-to-name comparison is between the Japanese versions. The rows of the English
    ref/settings.md are the English Console labels (Keyboard / Read aloud …) and are not
    word-for-word the section headings of use/12-settings.md (Keys / Speech …), so only
    the ja side can be matched by name. The English version is checked by whether it has
    the same number of sections as its translation, which catches a section added in only
    one language.

    Only the personal settings table of ref/settings.ja.md is read
    (`table_first_column_under`).
    """
    ref = os.path.join(GUIDE, "ref", "settings.ja.md")
    use_ja = os.path.join(GUIDE, "member", "12-settings.ja.md")
    use_en = os.path.join(GUIDE, "member", "12-settings.md")
    if not all(os.path.exists(p) for p in (ref, use_ja, use_en)):
        return

    tabs = table_first_column_under(ref, "個人設定")
    if not tabs:
        f.error(
            "ref/settings.ja.md: cannot read the personal settings table"
            " (the heading or the table's shape changed)"
        )
        return
    sections = heading3_under(use_ja, USE_SETTINGS_GROUPS_JA)

    for tab in tabs:
        if tab in USE_SETTINGS_EXEMPT:
            continue
        if tab not in sections:
            f.error(
                f"use/12-settings.ja.md: no section for a settings tab -> '### {tab}'"
                " (a tab present in ref/settings.ja.md needs a section in both languages)"
            )
    for name in sections:
        if name not in tabs:
            f.error(
                f"use/12-settings.ja.md: section left behind with no tab"
                f" -> '### {name}' (the tab was removed or renamed in the Console)"
            )

    en = heading3_under(use_en, USE_SETTINGS_GROUPS_EN)
    if len(en) != len(sections):
        f.error(
            "use/12-settings.md: number of tab sections differs from the translation"
            f" (en={len(en)} / ja={len(sections)})"
        )


# Member rows of the feature catalogue whose Details column need not point at use/
# (feature name -> reason). State the reason explicitly, and before adding one confirm
# that the reader really can reach the procedure: a row exempted while pointing nowhere
# near `use/` is exactly the shape in which the work-item inbox stayed green without a
# single chapter.
FEATURES_EXEMPT: dict[str, str] = {}

# The Details column heading (locale -> (file name, heading word, word meaning member)).
# All three are matched against the documents, so they stay in the document's language.
FEATURES_FILES = (
    ("features.ja.md", "詳細", "メンバー"),
    ("features.md", "Details", "member"),
)


def check_features(f: Findings) -> None:
    """Do member rows of the feature catalogue point at a procedure on the member shelf?

    `ref/features` declares itself an index of what exists and who can use it, with how
    to do it living behind the link. A row whose Details column only points at a
    capability table (`agents.md` / `repos.md`) is therefore a dead end for the reader —
    the work-item inbox row pointed only at `repos.md` (which provider surfaces what)
    while use/ had no chapter about the feature at all, and the catalogue still looked
    complete.

    Only member rows are checked. Admin and operator rows have a different reader and
    lead into `admin/`, `operate/` and `ref/`. A table with no Details column (personal
    settings) is out of scope because its section preamble points at the shelf instead —
    it drops out automatically by having no Details heading.

    Both languages are checked. `check_ref_parity` only looks at the shape of the ✓
    marks, so a destination that is stale in one language alone is not caught there.
    """
    for name, details_head, member in FEATURES_FILES:
        path = os.path.join(GUIDE, "ref", name)
        if not os.path.exists(path):
            continue
        header: list[str] = []
        for line in read(path).splitlines():
            s = line.strip()
            if not s.startswith("|") or set(s) <= set("|-: "):
                continue
            cells = [c.strip() for c in s.strip("|").split("|")]
            if details_head in cells:  # the table's header row
                header = cells
                continue
            if not header or len(cells) != len(header):
                continue
            who = cells[header.index(details_head) - 2] if len(header) > 2 else ""
            if not who.startswith(member):
                continue
            feature = cells[0]
            if feature in FEATURES_EXEMPT:
                continue
            details = cells[header.index(details_head)]
            if "../member/" not in details:
                f.error(
                    f"guide/ref/{name}: Details of '{feature}' does not point at"
                    f" member/ ({details or 'empty'})"
                    " — a member row must point at exactly one chapter that explains how"
                )


def check_notes(f: Findings) -> None:
    """Does the operating policy shipped to every container name only existing shelves?

    `workspace/workspace-notes.md` is baked into the image and read by every agent at
    startup. If it is left behind when the tree is rearranged, one stale spot misdirects
    every session in every container at once — and since readers simply follow the
    instructions, nobody notices anything is wrong (the P4 shelf rearrangement left
    `dev/93-…` behind).

    One rule: if it names a shelf, it must be a `guide/` shelf. That is the only tree
    shipped into containers; `docs/` (for developers) is in nobody's container.

    There used to be a rule that pointing at a shelf which is not guaranteed required a
    "may be absent" caveat in the same paragraph, because the mount was per-role and a
    member received only use/ and ref/. Role-scoped distribution was dropped (ADR 0064),
    so there is no room and no need to escape via a caveat: a shelf can either be pointed
    at or not, and what cannot be pointed at gets rewritten.
    """
    notes = os.path.join(ROOT, "workspace", "workspace-notes.md")
    if not os.path.exists(notes):
        return
    shelves = LIVING + ("decisions", "log")
    ref_re = re.compile(
        r"`(" + "|".join(shelves) + r")/([A-Za-z0-9._-]+\.md)`"
    )
    # Search within a paragraph, not a line: searching by line fails on nothing more than
    # a wrap, and "write it on the same line" is a formatting constraint, not a semantic
    # one.
    lines = read(notes).splitlines()
    blocks: list[tuple[int, str]] = []  # (first line number, paragraph)
    start, buf = 1, []
    for i, line in enumerate(lines, 1):
        if line.strip():
            if not buf:
                start = i
            buf.append(line)
        elif buf:
            blocks.append((start, "\n".join(buf)))
            buf = []
    if buf:
        blocks.append((start, "\n".join(buf)))

    for lineno, block in blocks:
        for m in ref_re.finditer(block):
            name, fname = m.group(1), m.group(2)
            if name not in GUIDE_SHELVES:
                f.error(
                    f"workspace-notes.md:{lineno}: points at a shelf that is not shipped"
                    f" -> {name}/{fname}"
                    " (only guide/ is in the container; point at a guide/ shelf)"
                )
                continue
            if not os.path.exists(os.path.join(GUIDE, name, fname)):
                f.error(
                    f"workspace-notes.md:{lineno}: points at a file that does not exist"
                    f" on that shelf -> {name}/{fname}"
                    " (every agent reads these instructions, so a stale pointer"
                    " misdirects all of them)"
                )


def check_ref_parity(f: Findings) -> None:
    """ref/ tables must have their ✓ marks in the same places in English and Japanese.

    The lang check confirms a translation exists, but that alone does not stop a
    translation whose content has drifted. A capability table that is stale on one side
    does the same harm as having two tables.
    """
    refdir = os.path.join(GUIDE, "ref")
    if not os.path.isdir(refdir):
        return
    for name in sorted(os.listdir(refdir)):
        if not name.endswith(".md") or name.endswith(".ja.md"):
            continue
        ja = os.path.join(refdir, name[: -len(".md")] + ".ja.md")
        if not os.path.exists(ja):
            continue  # the lang check reports this separately
        en_shape = table_mark_shape(os.path.join(refdir, name))
        ja_shape = table_mark_shape(ja)
        if len(en_shape) != len(ja_shape):
            f.error(
                f"ref/{name}: table row count differs from the translation"
                f" (en={len(en_shape)} / ja={len(ja_shape)})"
            )
            continue
        for i, (a, b) in enumerate(zip(en_shape, ja_shape)):
            if a != b:
                f.error(
                    f"ref/{name}: row {i + 1} has ✓ marks in different places from the"
                    f" translation (en={''.join(a)} / ja={''.join(b)})"
                )
                break


def check_ref(f: Findings) -> None:
    agents = os.path.join(GUIDE, "ref", "agents.md")
    if os.path.exists(agents):
        cols = {c.strip("`*") for c in table_columns(agents)}
        missing = source_kinds() - cols
        if missing:
            f.error(
                "ref/agents.md: kinds present in code are missing from the table -> "
                + ", ".join(sorted(missing))
            )
    # Rows expressed by Caps() must match the implementation 1:1 (exact match, not ⊇ —
    # marking a capability ✓ that is not set is the worst kind of lie).
    if os.path.exists(agents):
        caps = source_caps()
        for row, field in (
            ("Copy the conversation into a new session", "CanFork"),
            ("Fork from a past message", "CanForkAt"),
            ("Choosing to skip permission prompts", "PermissionChoice"),
        ):
            marked = table_check_marks(agents, row)
            if marked is None:
                f.error(f"ref/agents.md: row not found -> '{row}'")
                continue
            want = {k for k, fields in caps.items() if field in fields}
            if marked != want:
                f.error(
                    f"ref/agents.md: '{row}' disagrees with {field} in the"
                    f" implementation (table={sorted(marked)} / code={sorted(want)})"
                )

    # The Console labels are the source of truth for the settings tabs. Each language is
    # matched against the labels of its own locale, so a new screen cannot end up
    # silently undocumented.
    for locale, name in (("en", "settings.md"), ("ja", "settings.ja.md")):
        path = os.path.join(GUIDE, "ref", name)
        tabs = source_setting_tabs(locale)
        if not os.path.exists(path) or not tabs:
            continue
        rows = table_first_column(path)
        missing = sorted({v for v in tabs.values()} - rows)
        if missing:
            f.error(
                f"ref/{name}: settings tabs present in the Console are missing from the"
                " table -> " + ", ".join(missing)
            )

    # Loop over both languages. While only the English version was checked, breaking the
    # table in `.ja.md` still passed. (The settings tab check ten lines above always
    # looped; this only brings the two into the same shape.)
    for name in ("deploy-targets.md", "deploy-targets.ja.md"):
        targets = os.path.join(GUIDE, "ref", name)
        if not os.path.exists(targets):
            continue
        rows = {c.strip("`*") for c in table_first_column(targets)}
        for group in source_runtime_groups():
            if not (group & rows):
                f.error(
                    f"ref/{name}: a form present in code is missing from the table -> "
                    + "|".join(sorted(group))
                )


# --- helpers ------------------------------------------------------------------

FENCE_RE = re.compile(r"```.*?```", re.S)
INLINE_RE = re.compile(r"`[^`\n]*`")


def strip_code(body: str) -> str:
    """The vocabulary check ignores code blocks and inline code.

    Even reader-facing prose sometimes quotes a real config file or a list of env
    variables. What is forbidden is using implementation vocabulary in the running text,
    so anything explicitly marked as code is out of scope.
    """
    return INLINE_RE.sub("", FENCE_RE.sub("", body))


_cache: dict[str, str] = {}


def read(path: str) -> str:
    if path not in _cache:
        with open(path, encoding="utf-8") as fh:
            _cache[path] = fh.read()
    return _cache[path]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument(
        "--strict", action="store_true", help="promote warnings to errors"
    )
    ap.add_argument(
        "--only",
        default="",
        help=(
            "comma-separated check names "
            "(links,anchors,closure,chapters,lang,header,vocab,frozen,"
            "ref,settings,features,knowledge,notes)"
        ),
    )
    args = ap.parse_args()

    want = set(filter(None, args.only.split(",")))
    run = lambda name: not want or name in want  # noqa: E731

    files = all_docs()
    f = Findings()
    if run("links"):
        check_links(files, f)
    if run("anchors"):
        check_anchors(files, f)
    if run("closure"):
        check_closure(files, f)
    if run("chapters"):
        check_chapters(files, f)
    if run("lang"):
        check_lang(files, f)
    if run("header"):
        check_header(files, f, args.strict)
    if run("vocab"):
        check_vocab(files, f, args.strict)
    if run("frozen"):
        check_frozen(files, f)
    if run("ref"):
        check_ref(f)
        check_ref_parity(f)
    if run("settings"):
        check_use_settings(f)
    if run("features"):
        check_features(f)
    if run("knowledge"):
        check_knowledge(f)
    if run("notes"):
        check_notes(f)

    for w in f.warns:
        print(f"warn: {w}")
    for e in f.errors:
        print(f"ERROR: {e}")
    print(
        f"\ndocs-check: {len(files)} files, "
        f"{len(f.errors)} error(s), {len(f.warns)} warning(s)"
    )
    return 1 if f.errors else 0


if __name__ == "__main__":
    sys.exit(main())
