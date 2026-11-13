#!/usr/bin/env python3
"""Generate the MCP tool catalogue, guide/ref/mcp/README.md and README.ja.md.

The facts come from the two golden tools/list files the Go tests keep byte-identical to the
servers: which tools exist, which parameters they require, and on which audience each is
advertised. The group and the one-line descriptions come from scripts/mcp-catalogue/tools.tsv
(group titles in groups.tsv). Nothing about where a tool is offered is typed by hand, so the
page cannot claim a tool on a route the server does not serve.

    python3 scripts/mcp_catalogue.py          # rewrite both pages
    python3 scripts/mcp_catalogue.py --check  # exit 1 if a row is missing or a page is stale

docs-check.py runs the same check as `mcp`.
"""

from __future__ import annotations

import argparse
import csv
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

WS_GOLDEN = "workspace/agent/internal/mcpx/testdata/mcp_tools/list.json"
CP_GOLDEN = "control-plane/internal/mcpsrv/testdata/mcp_tools/list.json"
TOOLS_TSV = "scripts/mcp-catalogue/tools.tsv"
GROUPS_TSV = "scripts/mcp-catalogue/groups.tsv"
PAGES = {"en": "guide/ref/mcp/README.md", "ja": "guide/ref/mcp/README.ja.md"}

GENERATOR = "scripts/mcp_catalogue.py"

# The naming rule for tool names (docs/decisions/0135-mcp-tool-naming.md, rules 1 to 3): lower-case
# snake_case, no `af_` prefix, a first word from a closed verb list. Rule 4 and the list_/get_
# convention are for the reviewer and are not checked here.
TOOL_NAME_RE = re.compile(r"^[a-z][a-z0-9]*(_[a-z0-9]+)*$")
TOOL_VERBS = frozenset(
    "add answer archive ask attach create delete detach flush generate get link list pause peek "
    "propose purge rename request respond restore resume run search send set stop tail unwatch "
    "update watch".split()
)
# A bare verb names its operation and its subject on its own only for these two (rule 3); any
# other single word, `get` or `delete` say, leaves the object open.
TOOL_BARE_VERBS = frozenset({"watch", "unwatch"})
# Names that predate the rule and stay as they are: the shared memory API (noun-first), two
# single exceptions, and the two af_ tools (ADR 0135 option A). A new tool never joins this list.
TOOL_NAME_EXCEPTIONS = frozenset(
    {
        "af_report", "af_stop_after_turn", "branch_name", "skill_restore",
        "memory_feedback", "memory_forget", "memory_index", "memory_patch",
        "memory_projects", "memory_read", "memory_save", "memory_search",
    }
)


def naming_problems(root: str) -> list[str]:
    """Rule violations among the names the two goldens advertise (kept apart from problems(), which
    the page generator also raises on: a bad name must fail the check, not stop a regeneration)."""
    try:
        return name_problems(golden_tools(root))
    except CatalogueError as e:
        return [str(e)]


def name_problems(tools) -> list[str]:
    """Names in `tools` that break rules 1 to 3 of ADR 0135 and are not grandfathered."""
    out = []
    for tool in sorted(tools):
        if tool in TOOL_NAME_EXCEPTIONS:
            continue
        if not TOOL_NAME_RE.fullmatch(tool):
            out.append(f"{tool}: not lower-case snake_case (docs/decisions/0135-mcp-tool-naming.md rule 2)")
        elif tool.startswith("af_"):
            out.append(f"{tool}: carries the af_ prefix (rule 1); clients already add mcp__<server>__")
        elif "_" not in tool:
            if tool not in TOOL_BARE_VERBS:
                out.append(
                    f"{tool}: a bare verb names no object (rule 3); name it <verb>_<object>, or add it to"
                    f" TOOL_BARE_VERBS in {GENERATOR} if the verb alone fixes the subject"
                )
        elif tool.split("_")[0] not in TOOL_VERBS:
            out.append(
                f"{tool}: does not start with a known verb (rule 3); name it <verb>_<object>, or add the"
                f" verb to TOOL_VERBS in {GENERATOR} if it is a new one"
            )
    return out


class CatalogueError(Exception):
    """The goldens or the description file cannot be turned into a page."""


# --- the in-workspace server's audiences (mcpx/mcp_tool_golden_test.go toolAudiences) -------
#
# Every audience name must be listed here, interpreted or ignored. A new audience is a new way
# the server can be started, so an unknown one stops the generator rather than being dropped.

# A session always runs with --self-report --chromium-attach (mcpreg/builtin.go); the bare
# "session" audience is never what an agent actually sees.
WS_SESSION_ALWAYS = "session-chromium-nobrowser"
WS_SESSION_BROWSER = "session-chromium"
# Single-switch audiences, in the order a cell names them.
WS_SESSION_GATES = (
    ("session-peer", "peer"),
    ("session-spawn", "spawn"),
    ("session-search", "search"),
    ("session-memory", "memory"),
    ("session-imagegen", "imagegen"),
    ("session-studio", "studio"),
)
WS_ASSISTANT = (
    # (audience, the same without a browser, grant)
    ("assistant-read", "assistant-read-nobrowser", "read"),
    ("assistant-write", "assistant-write-nobrowser", "write"),
)
# Audiences that prove a boundary (flag conjunctions, every flag at once) but add no row fact.
WS_IGNORED = {"session", "session-write", "session-all", "session-all-write", "assistant-session-flags"}

# --- the Control Plane's audiences (mcpsrv/mcp_tool_golden_test.go): "<endpoint> memory=<bool>
# <role> <scope>" -----------------------------------------------------------------------------
CP_ENDPOINTS = ("mcp", "memory")
CP_ROLES = ("member", "tenant_admin", "super_admin")
CP_SCOPES = ("read", "write", "admin:dangerous")

LABELS = {
    "en": {
        "always": "✓",
        "peer": '"Messages between sessions"',
        "spawn": '"Starting sessions from sessions"',
        "search": '"Past-session search"',
        "memory": '"Agent memory"',
        "imagegen": '"Image generation"',
        "studio": "image studio",
        "browser": "with a browser",
        "read": "AF read",
        "write": "AF write",
        "admin": "admin",
        "ext-memory": "memory",
        "sep": ", ",
        "none": "—",
    },
    "ja": {
        "always": "✓",
        "peer": "「セッション間メッセージ」",
        "spawn": "「セッションからのセッション起動」",
        "search": "「過去セッションの検索」",
        "memory": "「エージェントメモリ」",
        "imagegen": "「画像生成」",
        "studio": "画像スタジオ",
        "browser": "ブラウザあり",
        "read": "AF 読み取り",
        "write": "AF 書き込み",
        "admin": "管理者",
        "ext-memory": "メモリ",
        "sep": "、",
        "none": "—",
    },
}


def _read_json(root: str, rel: str) -> dict:
    path = os.path.join(root, rel)
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, ValueError) as e:
        raise CatalogueError(f"{rel}: cannot read the golden ({e})") from e


def _read_tsv(root: str, rel: str, columns: tuple[str, ...]) -> list[dict]:
    path = os.path.join(root, rel)
    try:
        with open(path, encoding="utf-8", newline="") as fh:
            rows = list(csv.reader(fh, delimiter="\t", quoting=csv.QUOTE_NONE))
    except OSError as e:
        raise CatalogueError(f"{rel}: cannot read ({e})") from e
    if not rows or tuple(rows[0]) != columns:
        raise CatalogueError(f"{rel}: the first line must be the header {' / '.join(columns)}")
    out = []
    for n, row in enumerate(rows[1:], start=2):
        if not row:
            continue
        if len(row) != len(columns):
            raise CatalogueError(f"{rel}:{n}: {len(row)} columns, want {len(columns)}")
        out.append({c: v.strip() for c, v in zip(columns, row)} | {"_line": n})
    return out


def _tool_name(key: str) -> str:
    # The workspace golden keys a tool by name#variant when one name has several schemas.
    return key.split("#", 1)[0]


def golden_tools(root: str) -> dict[str, list[dict]]:
    """Every tool name in either golden, with each schema it is advertised with."""
    out: dict[str, list[dict]] = {}
    for rel in (WS_GOLDEN, CP_GOLDEN):
        for key, schema in _read_json(root, rel).get("tools", {}).items():
            out.setdefault(_tool_name(key), []).append(schema)
    return out


def _required(schema: dict) -> tuple[str, ...]:
    return tuple((schema.get("inputSchema") or {}).get("required") or [])


ROUTES = ("session", "assistant", "outside")


def required_by_route(root: str) -> dict[str, dict[str, tuple[str, ...]]]:
    """tool -> route -> the parameters a call on that route cannot leave out.

    The same name is advertised with a different schema per audience (a session's text and
    the assistant's), and the Control Plane's schema is its own; get_session_status, for one,
    requires `name` from an assistant or from outside but not from a session. Two audiences of
    one route that disagree cannot be summed up in one cell, so that stops the generator.
    """
    ws = _read_json(root, WS_GOLDEN)
    cp = _read_json(root, CP_GOLDEN)
    seen: dict[str, dict[str, dict[tuple[str, ...], str]]] = {}
    for aud, keys in ws.get("audiences", {}).items():
        route = "assistant" if aud.startswith("assistant") else "session"
        for key in keys:
            schema = ws.get("tools", {}).get(key)
            if schema is None:
                raise CatalogueError(f"{WS_GOLDEN}: audience '{aud}' lists {key}, which 'tools' does not hold")
            seen.setdefault(_tool_name(key), {}).setdefault(route, {}).setdefault(_required(schema), aud)
    for tool, schema in cp.get("tools", {}).items():
        seen.setdefault(tool, {}).setdefault("outside", {}).setdefault(_required(schema), "/mcp")
    out: dict[str, dict[str, tuple[str, ...]]] = {}
    for tool, routes in seen.items():
        for route, variants in routes.items():
            if len(variants) > 1:
                detail = "; ".join(f"{a}: {', '.join(r) or 'none'}" for r, a in variants.items())
                raise CatalogueError(
                    f"{tool} requires different parameters on audiences of the same route ({detail});"
                    f" {GENERATOR} shows one list per route"
                )
            out.setdefault(tool, {})[route] = next(iter(variants))
    return out


def availability(root: str) -> dict[str, dict]:
    """tool -> {"session": [...], "assistant": [...], "outside": [...]} as label keys.

    An empty list means not offered there; ["always"] means offered without a condition.
    """
    ws = _read_json(root, WS_GOLDEN).get("audiences", {})
    cp = _read_json(root, CP_GOLDEN).get("audiences", {})
    known = {WS_SESSION_ALWAYS, WS_SESSION_BROWSER} | WS_IGNORED
    known |= {a for a, _ in WS_SESSION_GATES}
    known |= {a for a, b, _ in WS_ASSISTANT} | {b for a, b, _ in WS_ASSISTANT}
    unknown = sorted(set(ws) - known)
    if unknown:
        raise CatalogueError(
            f"{WS_GOLDEN}: audiences {', '.join(unknown)} are not known to {GENERATOR};"
            " say there what each one means for the catalogue"
        )
    missing = sorted(known - WS_IGNORED - set(ws))
    if missing:
        raise CatalogueError(f"{WS_GOLDEN}: audiences {', '.join(missing)} are gone; update {GENERATOR}")
    names = lambda aud: {_tool_name(k) for k in ws[aud]}  # noqa: E731

    out: dict[str, dict] = {}

    def row(tool: str) -> dict:
        return out.setdefault(tool, {"session": [], "assistant": [], "outside": []})

    every_session = names("session-all")
    for tool in every_session:
        if tool in names(WS_SESSION_ALWAYS):
            row(tool)["session"] = ["always"]
        elif tool in names(WS_SESSION_BROWSER):
            row(tool)["session"] = ["browser"]
        else:
            gates = [g for aud, g in WS_SESSION_GATES if tool in names(aud)]
            if not gates:
                raise CatalogueError(
                    f"{WS_GOLDEN}: {tool} is offered to a session with every switch on but by no"
                    f" single switch; {GENERATOR} cannot tell which switch gates it"
                )
            row(tool)["session"] = gates
    for tool in names("session-studio") - every_session:
        row(tool)["session"] = ["studio"]
    for full, nobrowser, grant in WS_ASSISTANT:
        for tool in names(full):
            if row(tool)["assistant"]:
                continue
            row(tool)["assistant"] = [grant] if tool in names(nobrowser) else [grant, "browser"]

    # Every tool the workspace golden holds has to land somewhere, and a tool in any session or
    # assistant audience has to land in that column: a tool whose audience references went
    # missing, or that sits in a combination the rules above do not read, would otherwise be
    # printed as "not offered" without a word.
    for aud, keys in ws.items():
        col = "assistant" if aud.startswith("assistant") else "session"
        for tool in {_tool_name(k) for k in keys}:
            if not row(tool)[col]:
                raise CatalogueError(
                    f"{WS_GOLDEN}: {tool} is in audience '{aud}' but {GENERATOR} cannot place it in"
                    f" the {col} column; say there what that combination means"
                )
    for key in _read_json(root, WS_GOLDEN).get("tools", {}):
        if not any(key in keys for keys in ws.values()):
            raise CatalogueError(f"{WS_GOLDEN}: {key} is in 'tools' but in no audience")

    entries: dict[str, list[tuple[str, str, str, str]]] = {}
    for aud, tools in cp.items():
        parts = aud.split(" ")
        if (
            len(parts) != 4
            or parts[0] not in CP_ENDPOINTS
            or parts[1] not in ("memory=true", "memory=false")
            or parts[2] not in CP_ROLES
            or parts[3] not in CP_SCOPES
        ):
            raise CatalogueError(f"{CP_GOLDEN}: audience '{aud}' is not known to {GENERATOR}")
        for tool in tools:
            entries.setdefault(tool, []).append(tuple(parts))
    for tool in _read_json(root, CP_GOLDEN).get("tools", {}):
        if tool not in entries:
            raise CatalogueError(f"{CP_GOLDEN}: {tool} is in 'tools' but in no audience")
    memory_endpoint, memory_gated = set(), set()
    for tool, es in entries.items():
        if any(e[0] == "memory" for e in es):
            memory_endpoint.add(tool)
        mcp = [e for e in es if e[0] == "mcp"]
        if not mcp:
            raise CatalogueError(f"{CP_GOLDEN}: {tool} is served at /mcp/memory but not at /mcp")
        scope = min((CP_SCOPES.index(e[3]) for e in mcp))
        cell = [CP_SCOPES[scope]]
        if not any(e[2] == "member" for e in mcp):
            cell.append("admin")
        if not any(e[1] == "memory=false" for e in mcp):
            cell.append("ext-memory")
            memory_gated.add(tool)
        row(tool)["outside"] = cell
    # The page says the memory tools are the whole of /mcp/memory; hold it to that.
    if memory_endpoint != memory_gated:
        raise CatalogueError(
            f"{CP_GOLDEN}: the tools at /mcp/memory ({', '.join(sorted(memory_endpoint))}) are no"
            f" longer the memory-gated tools of /mcp ({', '.join(sorted(memory_gated))});"
            f" the page's sentence about /mcp/memory in {GENERATOR} needs rewriting"
        )
    return out


def load_rows(root: str) -> tuple[list[dict], list[dict]]:
    groups = _read_tsv(root, GROUPS_TSV, ("group", "en", "ja"))
    tools = _read_tsv(root, TOOLS_TSV, ("tool", "group", "en", "ja"))
    return groups, tools


def problems(root: str) -> list[str]:
    """Why the description file does not match the goldens; empty when it does."""
    try:
        groups, rows = load_rows(root)
        golden = golden_tools(root)
    except CatalogueError as e:
        return [str(e)]
    out = []
    group_ids = [g["group"] for g in groups]
    for g in groups:
        if group_ids.count(g["group"]) > 1:
            out.append(f"{GROUPS_TSV}:{g['_line']}: group '{g['group']}' is listed twice")
        if not g["en"] or not g["ja"]:
            out.append(f"{GROUPS_TSV}:{g['_line']}: group '{g['group']}' has no en or ja title")
    seen: dict[str, int] = {}
    for r in rows:
        where = f"{TOOLS_TSV}:{r['_line']}"
        if r["tool"] in seen:
            out.append(f"{where}: {r['tool']} is already described on line {seen[r['tool']]}")
            continue
        seen[r["tool"]] = r["_line"]
        if r["tool"] not in golden:
            out.append(
                f"{where}: {r['tool']} is in no golden tools/list ({WS_GOLDEN}, {CP_GOLDEN});"
                " remove the row or fix the name"
            )
        if r["group"] not in group_ids:
            out.append(f"{where}: {r['tool']} names group '{r['group']}', which {GROUPS_TSV} does not list")
        for lang in ("en", "ja"):
            if not r[lang]:
                out.append(f"{where}: {r['tool']} has no {lang} description")
            elif "|" in r[lang]:
                out.append(f"{where}: {r['tool']}'s {lang} description contains '|', which breaks the table")
    for tool in sorted(set(golden) - set(seen)):
        out.append(
            f"{TOOLS_TSV}: {tool} is advertised by a server but has no description row;"
            f" add one, then run {GENERATOR}"
        )
    for g in groups:
        if not any(r["group"] == g["group"] for r in rows):
            out.append(f"{GROUPS_TSV}:{g['_line']}: group '{g['group']}' has no tools")
    return out


def _cell(keys: list[str], lang: str) -> str:
    lab = LABELS[lang]
    if not keys:
        return lab["none"]
    if keys == ["always"]:
        return lab["always"]
    text = lab["sep"].join(lab[k] for k in keys)
    return f"✓ {text}"


def _outside_cell(keys: list[str], lang: str) -> str:
    lab = LABELS[lang]
    if not keys:
        return lab["none"]
    scope, rest = keys[0], keys[1:]
    return "✓ " + lab["sep"].join([f"`{scope}`"] + [lab[k] for k in rest])


INTRO = {
    "en": """---
audience: "anyone looking up which Agent Fleet MCP tool does what, and where it is offered"
source_of_truth: "the tools/list goldens of the in-workspace af server and of the Control Plane's /mcp; this page is generated from them by scripts/mcp_catalogue.py"
---

# MCP tools

English | [日本語](README.ja.md)

<!-- Generated by scripts/mcp_catalogue.py. Do not edit: change scripts/mcp-catalogue/tools.tsv and run it. -->

Agent Fleet gives agents and assistants MCP tools for operating Agent Fleet itself.
Which of them a client sees depends on where it connects from.

| Column | Who uses it | How it connects |
|---|---|---|
| Session | the agent in a session | Built in, as the MCP server `af` |
| Assistant | an assistant chat in the Console | Built in, when the assistant's tools are "AF read" or "AF write" ([Ask an assistant](../../user/chat/assistant.md)) |
| Outside | a client outside Agent Fleet, such as claude.ai or Claude Code on your own computer | The Control Plane's `/mcp`, with a token or by signing in ([Drive your workspace from an external Claude](../../user/outside/external-claude.md)) |

How to read a cell:

- **Session**: ✓ alone means every session has it. A quoted name is the switch in
  ⚙ Settings → Agents that has to be on; "Agent memory" is the one in ⚙ Settings → Agent memory → Settings.
  "image studio" means only a session working in the image studio.
- **Assistant**: "AF read" means an assistant with either grant has it; "AF write" means only an assistant
  allowed to write.
- **with a browser**: not offered in a workspace that has no browser ([deployment targets](../deploy-targets.md)).
- **Outside**: `read` or `write` is the scope the token or connected app needs. "admin" means only tenant
  administrators and deployment administrators see it. "memory" means only when memory may be used from outside
  ([Use memory from an external agent](../../user/memory/external.md)); those tools are also all that
  `/mcp/memory` serves.
- **Required parameters** are the ones a call cannot leave out. The tool list a client receives describes
  every parameter.
""",
    "ja": """---
audience: "Agent Fleet の MCP ツールが何をし、どこで使えるかを調べたい人"
source_of_truth: "ワークスペース内の af サーバーと Control Plane の /mcp の tools/list のゴールデン。このページは scripts/mcp_catalogue.py がそれから生成する"
---

# MCP ツール

[English](README.md) | 日本語

<!-- Generated by scripts/mcp_catalogue.py. Do not edit: change scripts/mcp-catalogue/tools.tsv and run it. -->

Agent Fleet は、エージェントやアシスタントが Agent Fleet 自体を操作するための MCP ツールを用意しています。
どのツールが見えるかは、どこからつなぐかで変わります。

| 列 | 使う人 | つなぎ方 |
|---|---|---|
| セッション | セッションの中のエージェント | 自動でつながります（MCP サーバー名は `af`） |
| アシスタント | Console のアシスタントのチャット | アシスタントのツールが「AF 読み取り」か「AF 書き込み」のとき、自動でつながります（[アシスタントに質問する](../../user/chat/assistant.ja.md)） |
| 外部 | claude.ai や手元の PC の Claude Code など、Agent Fleet の外のクライアント | Control Plane の `/mcp` に、トークンかサインインでつなぎます（[外部の Claude からワークスペースを動かす](../../user/outside/external-claude.ja.md)） |

セルの読み方は次のとおりです。

- **セッション**: ✓ だけなら、どのセッションでも使えます。「」付きの名前は、⚙設定 →「エージェント」で
  オンにする必要のあるスイッチです。「エージェントメモリ」は ⚙設定 →「エージェントメモリ」→「設定」にあります。
  「画像スタジオ」は、画像スタジオで作業しているセッションだけで使えます。
- **アシスタント**: 「AF 読み取り」は、どちらの権限のアシスタントでも使えます。「AF 書き込み」は、書き込みを許可したアシスタントだけが使えます。
- **ブラウザあり**: ブラウザのないワークスペースでは使えません（[配備形態](../deploy-targets.ja.md)）。
- **外部**: `read`・`write` は、トークンや接続アプリに必要なスコープです。「管理者」はテナント管理者と配備管理者だけに見えます。
  「メモリ」は、外部からのメモリ利用が許されているときだけ使えます（[外部のエージェントからメモリを使う](../../user/memory/external.ja.md)）。
  `/mcp/memory` が提供するのも、これらのツールだけです。
- **必須の引数** は、呼び出しで省略できない引数です。すべての引数は、クライアントが受け取るツール一覧に書かれています。
""",
}

ROUTE_NAMES = {
    "en": {"session": "Session", "assistant": "Assistant", "outside": "Outside"},
    "ja": {"session": "セッション", "assistant": "アシスタント", "outside": "外部"},
}


def _params_cell(avail: dict, required: dict[str, tuple[str, ...]], lang: str) -> str:
    """One list when every route that offers the tool requires the same; else one per route."""
    fmt = lambda ps: ", ".join(f"`{p}`" for p in ps) or "—"  # noqa: E731
    offered = [r for r in ROUTES if avail.get(r)]
    lists = [required.get(r, ()) for r in offered]
    if len(set(lists)) <= 1:
        return fmt(lists[0] if lists else ())
    groups: dict[tuple[str, ...], list[str]] = {}
    for r, ps in zip(offered, lists):
        groups.setdefault(ps, []).append(ROUTE_NAMES[lang][r])
    sep = "、" if lang == "ja" else ", "
    return "; ".join(f"{sep.join(names)}: {fmt(ps)}" for ps, names in groups.items())


TABLE_HEAD = {
    "en": "| Tool | What it does | Session | Assistant | Outside | Required parameters |",
    "ja": "| ツール | できること | セッション | アシスタント | 外部 | 必須の引数 |",
}


def render(root: str, lang: str) -> str:
    """The page for one language. Raises CatalogueError when problems() is not empty."""
    probs = problems(root)
    if probs:
        raise CatalogueError("\n".join(probs))
    groups, rows = load_rows(root)
    avail = availability(root)
    required = required_by_route(root)
    out = [INTRO[lang]]
    for g in groups:
        out.append(f"## {g[lang]}\n")
        out.append(TABLE_HEAD[lang])
        out.append("|---|---|---|---|---|---|")
        for r in rows:
            if r["group"] != g["group"]:
                continue
            a = avail.get(r["tool"], {"session": [], "assistant": [], "outside": []})
            params = _params_cell(a, required.get(r["tool"], {}), lang)
            out.append(
                f"| `{r['tool']}` | {r[lang]} | {_cell(a['session'], lang)}"
                f" | {_cell(a['assistant'], lang)} | {_outside_cell(a['outside'], lang)} | {params} |"
            )
        out.append("")
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
    ap.add_argument("--check", action="store_true", help="write nothing; exit 1 if a page is stale")
    ap.add_argument("--root", default=ROOT, help=argparse.SUPPRESS)
    args = ap.parse_args()
    try:
        if args.check:
            stale = stale_pages(args.root)
            for rel in stale:
                print(f"{rel}: stale; run python3 {GENERATOR}")
            return 1 if stale else 0
        for lang, rel in PAGES.items():
            text = render(args.root, lang)
            path = os.path.join(args.root, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(text)
            print(f"wrote {rel}")
    except CatalogueError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
