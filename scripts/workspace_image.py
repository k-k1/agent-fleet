#!/usr/bin/env python3
"""Generate "What's in the workspace", guide/ref/workspace-image.md and .ja.md.

The tools, their purpose, upstream and licence come from scripts/workspace-image/tools.tsv
(group titles in groups.tsv). Every version is read from workspace/Dockerfile, the file the
image and the versions.json pins are built from, so the page cannot advertise a version the
image does not carry. The licence column is hand-kept: each one was checked against the
upstream LICENSE file or terms page named in licence_url, never guessed. A licence_url may hold
`{version}` (the resolved version) or `{v0}`, `{v1}`, ... (its dot-separated parts), so a link
names the file at the tag that is installed and follows a pin bump; a project can change its
licence between releases, so after a bump that moves to a new major, re-check the licence.

    python3 scripts/workspace_image.py          # rewrite both pages
    python3 scripts/workspace_image.py --check  # exit 1 if a row is missing or a page is stale

docs-check.py runs the same check as `image`.

Version sources (the `version` column; `;select` adds "selectable in Settings > Toolchains"):
    arg:NAME     the default of `ARG NAME=` in the Dockerfile
    deb:NAME     the same, without the Debian revision (`1.2.3-1~deb13u1` -> `1.2.3`)
    from:IMAGE   the tag's major version of the final stage's `FROM IMAGE:<major>-...`
    apt:PACKAGE  a package of the base image's Debian release; it must be in an apt-get install
    release      ships with Agent Fleet itself
    latest:PKG   installed unpinned at image build; it must be a `pip install` line
    select       chosen per member only, with no image default
"""

from __future__ import annotations

import argparse
import csv
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

DOCKERFILE = "workspace/Dockerfile"
TOOLS_TSV = "scripts/workspace-image/tools.tsv"
GROUPS_TSV = "scripts/workspace-image/groups.tsv"
PAGES = {"en": "guide/ref/workspace-image.md", "ja": "guide/ref/workspace-image.ja.md"}
GENERATOR = "scripts/workspace_image.py"

TOOL_COLUMNS = ("id", "group", "bins", "name", "url", "licence", "licence_url", "version", "en", "ja")

# Version pins in the Dockerfile that no row shows, and why. Any other `ARG *_VERSION` without
# a row fails the check, so a newly pinned tool cannot stay off the page.
UNSHOWN_ARGS = {
    "VERSION": "the workspace-agent build stamp; the row says 'same as Agent Fleet'",
    "CHROMIUM_CFT_VERSION": "on-demand Chromium for images built without the browser",
    "CHROMIUM_DL_VERSION": "on-demand Chromium for images built without the browser",
    "NOTO_CJK_VERSION": "on-demand fonts for images built without the browser",
}
# Files the Dockerfile puts in /usr/local/bin that are not tools a member runs.
UNSHOWN_BINS = {"entrypoint.sh"}

# Debian codename -> release number, for "Debian 13 (trixie) package".
DEBIAN = {"bookworm": "12", "trixie": "13", "forky": "14", "duke": "15"}

LICENCE_REPO = "https://github.com/k-k1/agent-fleet/blob/main"


class ImageError(Exception):
    """The Dockerfile or the description files cannot be turned into a page."""


def _read(root: str, rel: str) -> str:
    try:
        with open(os.path.join(root, rel), encoding="utf-8") as fh:
            return fh.read()
    except OSError as e:
        raise ImageError(f"{rel}: cannot read ({e})") from e


def _read_tsv(root: str, rel: str, columns: tuple[str, ...]) -> list[dict]:
    rows = list(csv.reader(_read(root, rel).splitlines(), delimiter="\t", quoting=csv.QUOTE_NONE))
    if not rows or tuple(rows[0]) != columns:
        raise ImageError(f"{rel}: the first line must be the header {' / '.join(columns)}")
    out = []
    for n, row in enumerate(rows[1:], start=2):
        if not row:
            continue
        if len(row) != len(columns):
            raise ImageError(f"{rel}:{n}: {len(row)} columns, want {len(columns)}")
        out.append({c: v.strip() for c, v in zip(columns, row)} | {"_line": n})
    return out


class Dockerfile:
    """The facts the page needs from workspace/Dockerfile, comments stripped."""

    def __init__(self, text: str):
        lines = [ln for ln in text.splitlines() if not ln.lstrip().startswith("#")]
        self.text = "\n".join(lines)
        self.args = dict(re.findall(r"^ARG\s+([A-Z0-9_]+)=(\S+)", self.text, re.M))
        froms = re.findall(r"^FROM\s+(?:--platform=\S+\s+)?(\S+)", self.text, re.M)
        if not froms:
            raise ImageError(f"{DOCKERFILE}: no FROM line")
        self.base = froms[-1]
        m = re.search(r"-(" + "|".join(DEBIAN) + r")\b", self.base)
        self.debian = f"{DEBIAN[m.group(1)]} ({m.group(1)})" if m else None
        self.bins = set(re.findall(r"/usr/local/bin/([A-Za-z0-9._-]+)", self.text))
        # One logical line per RUN, so an install's package list is the text up to its && or ;.
        joined = self.text.replace("\\\n", " ")
        apt = " ".join(re.findall(r"apt-get install\s([^&;]*)", joined))
        self.apt = {re.sub(r"=.*", "", t.strip('"')) for t in apt.split() if not t.startswith("-")}
        self.pip = " ".join(re.findall(r"pip install\s[^&;]*", joined))

    def version_args(self) -> set[str]:
        return {a for a in self.args if a == "VERSION" or a.endswith("_VERSION")}


def resolve(df: Dockerfile, spec: str) -> dict:
    """{"value": str|None, "kind": str, "select": bool}; raises ImageError when unreadable."""
    parts = spec.split(";")
    select = "select" in parts[1:] or parts[0] == "select"
    src = parts[0]
    if any(p != "select" for p in parts[1:]):
        raise ImageError(f"unknown version modifier in '{spec}'")
    kind, _, arg = src.partition(":")
    if kind in ("arg", "deb"):
        if arg not in df.args:
            raise ImageError(f"'{spec}': {DOCKERFILE} has no `ARG {arg}=<default>`")
        v = df.args[arg]
        return {"value": v.split("-", 1)[0] if kind == "deb" else v, "kind": "pin", "select": select}
    if kind == "from":
        m = re.fullmatch(re.escape(arg) + r":(\d+)(?:[.-].*)?", df.base)
        if not m:
            raise ImageError(f"'{spec}': the final stage is FROM {df.base}, not {arg}:<major>")
        return {"value": m.group(1), "kind": "pin", "select": select}
    if kind == "apt":
        if not df.debian:
            raise ImageError(f"'{spec}': cannot tell the Debian release from FROM {df.base}")
        if arg not in df.apt:
            raise ImageError(f"'{spec}': {DOCKERFILE} installs no apt package '{arg}'")
        return {"value": df.debian, "kind": "apt", "select": select}
    if src in ("release", "select"):
        return {"value": None, "kind": src, "select": select}
    if kind == "latest":
        if not re.search(r"pip install\b[^\n]*\s" + re.escape(arg or "") + r"(\s|$)", df.pip):
            raise ImageError(f"'{spec}': {DOCKERFILE} has no unpinned `pip install` of '{arg}'")
        return {"value": None, "kind": "latest", "select": select}
    raise ImageError(f"unknown version source '{spec}'")


_PLACEHOLDER = re.compile(r"\{([a-z0-9]*)\}")


def licence_url(r: dict, v: dict) -> str:
    """The row's licence_url with the installed version filled in."""
    url = r["licence_url"]
    names = _PLACEHOLDER.findall(url)
    if not names:
        return url
    if v["kind"] != "pin":
        raise ImageError(f"licence_url has {{{names[0]}}} but version '{r['version']}' pins nothing")
    fields = {"version": v["value"]} | {f"v{i}": part for i, part in enumerate(v["value"].split("."))}
    for n in names:
        if n not in fields:
            raise ImageError(f"licence_url names {{{n}}}, which version {v['value']} does not have")
    return _PLACEHOLDER.sub(lambda m: fields[m.group(1)], url)


def load_rows(root: str) -> tuple[list[dict], list[dict]]:
    groups = _read_tsv(root, GROUPS_TSV, ("group", "en", "ja"))
    tools = _read_tsv(root, TOOLS_TSV, TOOL_COLUMNS)
    return groups, tools


def problems(root: str) -> list[str]:
    """Why the description files do not match the Dockerfile; empty when they do."""
    try:
        groups, rows = load_rows(root)
        df = Dockerfile(_read(root, DOCKERFILE))
    except ImageError as e:
        return [str(e)]
    out = []
    group_ids = [g["group"] for g in groups]
    for g in groups:
        if group_ids.count(g["group"]) > 1:
            out.append(f"{GROUPS_TSV}:{g['_line']}: group '{g['group']}' is listed twice")
    seen: dict[str, int] = {}
    covered_bins: set[str] = set()
    used_args: set[str] = set()
    for r in rows:
        where = f"{TOOLS_TSV}:{r['_line']}"
        if r["id"] in seen:
            out.append(f"{where}: {r['id']} is already described on line {seen[r['id']]}")
            continue
        seen[r["id"]] = r["_line"]
        if r["group"] not in group_ids:
            out.append(f"{where}: {r['id']} names group '{r['group']}', which {GROUPS_TSV} does not list")
        for col in ("name", "url", "licence", "licence_url", "version", "en", "ja"):
            if not r[col]:
                out.append(f"{where}: {r['id']} has no {col}")
            elif "|" in r[col]:
                out.append(f"{where}: {r['id']}'s {col} contains '|', which breaks the table")
        if r["licence_url"] and not r["licence_url"].startswith("https://"):
            out.append(f"{where}: {r['id']}'s licence_url must be the https URL the licence was checked at")
        if r["licence"] == "see upstream":
            pass
        elif r["licence"] != "vendor" and not re.fullmatch(r"[A-Za-z0-9.+-]+( (OR|AND|WITH) [A-Za-z0-9.+-]+)*", r["licence"]):
            out.append(f"{where}: {r['id']}'s licence '{r['licence']}' is not an SPDX expression, 'vendor' or 'see upstream'")
        covered_bins |= set(r["bins"].split())
        try:
            v = resolve(df, r["version"])
        except ImageError as e:
            out.append(f"{where}: {r['id']}: version {e}")
        else:
            try:
                licence_url(r, v)
            except ImageError as e:
                out.append(f"{where}: {r['id']}: {e}")
        m = re.match(r"(?:arg|deb):([A-Z0-9_]+)", r["version"])
        if m:
            used_args.add(m.group(1))
    for b in sorted(df.bins - covered_bins - UNSHOWN_BINS):
        out.append(
            f"{TOOLS_TSV}: {DOCKERFILE} installs /usr/local/bin/{b}, which no row lists in its bins;"
            f" add a row (or the name to an existing row's bins), then run {GENERATOR}"
        )
    for a in sorted(df.version_args() - used_args - set(UNSHOWN_ARGS)):
        out.append(
            f"{TOOLS_TSV}: {DOCKERFILE} pins ARG {a}, which no row's version reads;"
            f" add a row (arg:{a}), or list it in UNSHOWN_ARGS in {GENERATOR} with the reason"
        )
    for g in groups:
        if not any(r["group"] == g["group"] for r in rows):
            out.append(f"{GROUPS_TSV}:{g['_line']}: group '{g['group']}' has no tools")
    return out


TOOLCHAINS = {"en": "../user/workspace/toolchains.md", "ja": "../user/workspace/toolchains.ja.md"}

TEXT = {
    "en": {
        "release": "Same as Agent Fleet",
        "latest": "Latest at image build",
        "apt": "Debian {v} package",
        "select": "[Selectable]({tc})",
        "select_default": "[Selectable]({tc}); image default `{v}`",
        "vendor": "[Vendor terms]({u})",
        "upstream": "[See upstream]({u})",
        "head": "| Tool | What it is for | Version | Licence |",
    },
    "ja": {
        "release": "Agent Fleet と同じ",
        "latest": "イメージのビルド時の最新",
        "apt": "Debian {v} のパッケージ",
        "select": "[選択式]({tc})",
        "select_default": "[選択式]({tc})。イメージの既定は `{v}`",
        "vendor": "[提供元の規約]({u})",
        "upstream": "[提供元を参照]({u})",
        "head": "| ツール | 用途 | バージョン | ライセンス |",
    },
}

INTRO = {
    "en": """---
audience: "anyone who wants to know which tools a workspace has, at which version, and under which licence"
source_of_truth: "workspace/Dockerfile for versions and scripts/workspace-image/tools.tsv for the rest; this page is generated from them by scripts/workspace_image.py"
---

# What's in the workspace

English | [日本語](workspace-image.ja.md)

<!-- Generated by scripts/workspace_image.py. Do not edit: change scripts/workspace-image/tools.tsv and run it. -->

The tools a workspace comes with, the version the image fixes, and each tool's licence.

- **Version**: "Selectable" means each member picks the version under ⚙ Settings → Toolchains
  ([Choose your toolchains](../user/workspace/toolchains.md)); the image default is used until they do.
  Most agent CLIs can also be updated to their latest version at start, from the same tab
- Some tools, such as the agent CLIs, rtk, the Google Cloud CLI and the database servers, are downloaded into your home on
  first start or first use, at the version shown here
- A deployment that runs without a browser has no Chromium ([deployment targets](deploy-targets.md))
- The agent CLIs are third-party software, each under its vendor's licence or terms. Each member signs in with their own
  account
- You can install more tools into `~/.local` yourself
""",
    "ja": """---
audience: "ワークスペースにどのツールが、どのバージョンとライセンスで入っているかを知りたい人"
source_of_truth: "バージョンは workspace/Dockerfile、それ以外は scripts/workspace-image/tools.tsv。このページは scripts/workspace_image.py がそれから生成する"
---

# ワークスペースに入っているもの

[English](workspace-image.md) | 日本語

<!-- Generated by scripts/workspace_image.py. Do not edit: change scripts/workspace-image/tools.tsv and run it. -->

ワークスペースに入っているツールと、イメージが固定しているバージョン、それぞれのライセンスです。

- **バージョン**: 「選択式」は、メンバーが ⚙設定 →「ツールチェーン」でバージョンを選べるものです
  （[ツールチェーンを選ぶ](../user/workspace/toolchains.ja.md)）。選ぶまではイメージの既定が使われます。
  エージェントの CLI の多くも、同じタブで起動時に最新版へ更新できます
- エージェントの CLI、rtk、Google Cloud CLI、データベースのサーバーなどは、初回の起動時か初めて使うときに、ここに書いたバージョンがホームにダウンロードされます
- ブラウザのない配備には Chromium がありません（[配備形態](deploy-targets.ja.md)）
- エージェントの CLI は、それぞれの提供元のライセンスか規約に従うサードパーティのソフトウェアです。メンバーはそれぞれ自分のアカウントでサインインします
- ほかのツールは、自分で `~/.local` に入れられます
""",
}

LICENCE = {
    "en": f"""## Licence

- Agent Fleet itself is licensed under the [Apache License 2.0]({LICENCE_REPO}/LICENSE)
- The third-party components bundled with Agent Fleet, and their licences, are listed in [NOTICE]({LICENCE_REPO}/NOTICE)
- The tools on this page keep their own licences, shown in the Licence column. "Vendor terms" means the tool is
  proprietary and used under its vendor's terms
""",
    "ja": f"""## ライセンス

- Agent Fleet 自体のライセンスは [Apache License 2.0]({LICENCE_REPO}/LICENSE) です
- Agent Fleet に同梱しているサードパーティのコンポーネントとそのライセンスは、[NOTICE]({LICENCE_REPO}/NOTICE) にあります
- このページのツールは、それぞれ自分のライセンスに従います。ライセンスの列に書いてあります。「提供元の規約」は、
  提供元の規約のもとで使うプロプライエタリなツールです
""",
}


def _link(url: str, lang: str) -> str:
    """A guide-relative url names the English page; the Japanese page links its translation."""
    if url.startswith("https://") or lang == "en":
        return url
    return url[: -len(".md")] + ".ja.md"


def _version_cell(v: dict, lang: str) -> str:
    t = TEXT[lang]
    tc = TOOLCHAINS[lang]
    if v["kind"] == "pin":
        base = f"`{v['value']}`"
    elif v["kind"] == "apt":
        base = t["apt"].format(v=v["value"])
    elif v["kind"] in ("release", "latest"):
        base = t[v["kind"]]
    else:
        base = None
    if v["select"]:
        if base is None:
            return t["select"].format(tc=tc)
        if v["kind"] == "pin":
            return t["select_default"].format(tc=tc, v=v["value"])
        raise ImageError("only a pinned version can be an image default for a selectable tool")
    return base


def _licence_cell(r: dict, v: dict, lang: str) -> str:
    url = licence_url(r, v)
    if r["licence"] == "vendor":
        return TEXT[lang]["vendor"].format(u=url)
    if r["licence"] == "see upstream":
        return TEXT[lang]["upstream"].format(u=url)
    return f"[{r['licence']}]({url})"


def render(root: str, lang: str) -> str:
    """The page for one language. Raises ImageError when problems() is not empty."""
    probs = problems(root)
    if probs:
        raise ImageError("\n".join(probs))
    groups, rows = load_rows(root)
    df = Dockerfile(_read(root, DOCKERFILE))
    out = [INTRO[lang]]
    for g in groups:
        out.append(f"## {g[lang]}\n")
        out.append(TEXT[lang]["head"])
        out.append("|---|---|---|---|")
        for r in rows:
            if r["group"] != g["group"]:
                continue
            v = resolve(df, r["version"])
            out.append(
                f"| [{r['name']}]({_link(r['url'], lang)}) | {r[lang]} | {_version_cell(v, lang)}"
                f" | {_licence_cell(r, v, lang)} |"
            )
        out.append("")
    out.append(LICENCE[lang])
    return "\n".join(out).rstrip("\n") + "\n"


def stale_pages(root: str) -> list[str]:
    """Pages whose content is not what render() produces now."""
    out = []
    for lang, rel in PAGES.items():
        want = render(root, lang)
        try:
            with open(os.path.join(root, rel), encoding="utf-8") as fh:
                have = fh.read()
        except OSError:
            have = None
        if have != want:
            out.append(rel)
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--check", action="store_true", help="write nothing; exit 1 if a row is missing or a page is stale")
    ap.add_argument("--root", default=ROOT, help=argparse.SUPPRESS)
    args = ap.parse_args()
    probs = problems(args.root)
    if probs:
        for p in probs:
            print(f"ERROR: {p}", file=sys.stderr)
        return 1
    try:
        if args.check:
            stale = stale_pages(args.root)
            for rel in stale:
                print(f"{rel}: stale; run python3 {GENERATOR}")
            return 1 if stale else 0
        for lang, rel in PAGES.items():
            text = render(args.root, lang)
            with open(os.path.join(args.root, rel), "w", encoding="utf-8") as fh:
                fh.write(text)
            print(f"wrote {rel}")
    except ImageError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
