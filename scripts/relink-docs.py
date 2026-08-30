#!/usr/bin/env python3
"""docs/ 内の相対リンクを、log/ 凍結後の位置へ張り替える。

一度きりの移行スクリプト（P0.6）。実行後は捨ててよい。

壊れ方は 4 種類ある:

  1. 番号付き docs が docs/ 直下 → docs/log/ へ落ちた。
     これを指す側（decisions/ dev/ guide/ 直下の md）は `log/` を挟む必要がある。
  2. 番号付き docs 自身が 1 階層深くなった。
     その中から dev/ decisions/ guide/ を指すリンクは `../` が 1 つ足りない。
  3. history/ が log/ へ改称された。log/ の中では兄弟になったので `history/` が余計。
  4. reference/ の転送スタブを削除した。inbound は dev/ の移設先へ向け直す。

推測はしない: 候補を順に試し、**実在するものだけ**を採用する。どれも実在しなければ
書き換えず、検査器のエラーとして残す（人間が見る）。
"""

from __future__ import annotations

import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOCS = os.path.join(ROOT, "docs")

LINK_RE = re.compile(r"(?<!!)(\[[^\]]*\]\()([^)\s]+?)((?:#[^)\s]*)?\))")

# 廃止した reference/ の移設先（旧 docs/README.md の読み替え表）。
REFERENCE_MAP = {
    "requirements.md": "dev/01-architecture.md",
    "architecture.md": "dev/01-architecture.md",
    "api-agent.md": "dev/05-api-contracts.md",
    "portability.md": "dev/09-deploy.md",
    "aws.md": "dev/09-deploy.md",
    "security.md": "dev/07-security.md",
    "auth.md": "dev/07-security.md",
    "preview.md": "dev/05-api-contracts.md",
    "internal-git-provider.md": "dev/91-internal-git.md",
    "notification-center.md": "log/notification-center.md",
}


def candidates(target: str) -> list[str]:
    """試す順に候補を返す。先に来たものほど「素直な」解釈。"""
    out = [target]
    # (1) 参照先が log/ へ落ちた: 最後の ../ 群の直後に log/ を挟む
    m = re.match(r"^((?:\.\./)*)(.*)$", target)
    ups, rest = m.group(1), m.group(2)
    out.append(f"{ups}log/{rest}")
    # (2) 参照元が 1 階層深くなった
    out.append(f"../{target}")
    # (3) history/ は log/ の中で兄弟になった
    if "history/" in target:
        out.append(target.replace("history/", ""))
        out.append(target.replace("history/", "log/"))
        out.append(f"../{target.replace('history/', '')}")
    # (4) reference/ の移設
    m = re.search(r"reference/([a-z-]+\.md)$", target)
    if m and m.group(1) in REFERENCE_MAP:
        dest = REFERENCE_MAP[m.group(1)]
        out.append(f"{ups}{dest}")
        out.append(f"{ups}../{dest}")
        out.append(f"../{ups}{dest}")
    return out


def fix_file(path: str) -> int:
    with open(path, encoding="utf-8") as fh:
        body = fh.read()
    here = os.path.dirname(path)
    fixed = 0

    def repl(m: re.Match[str]) -> str:
        nonlocal fixed
        head, target, tail = m.group(1), m.group(2), m.group(3)
        if target.startswith(("http://", "https://", "mailto:", "#", "/")):
            return m.group(0)
        if os.path.exists(os.path.normpath(os.path.join(here, target))):
            return m.group(0)
        for cand in candidates(target)[1:]:
            if os.path.exists(os.path.normpath(os.path.join(here, cand))):
                fixed += 1
                return f"{head}{cand}{tail}"
        return m.group(0)

    new = LINK_RE.sub(repl, body)
    if new != body:
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(new)
    return fixed


def main() -> int:
    total = files = 0
    for dirpath, dirnames, filenames in os.walk(DOCS):
        dirnames[:] = [d for d in dirnames if not d.startswith(".")]
        for name in sorted(filenames):
            if not name.endswith(".md"):
                continue
            n = fix_file(os.path.join(dirpath, name))
            if n:
                files += 1
                total += n
    print(f"fixed {total} link(s) in {files} file(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
