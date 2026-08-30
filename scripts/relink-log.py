#!/usr/bin/env python3
"""番号付き docs と history/ を docs/log/ へ凍結したときの、参照の一括張り替え。

一度きりの移行スクリプト（P0.6）。実行後は捨ててよい。

慎重にやる理由: `docs/NN` 形式の裸参照には**旧番号の残骸**が混ざっている
（`docs/09-portability.md` / `docs/11-phase1-plan.md` など、2026-07 の再編で
消えたファイルを今も指しているコメント）。素朴に `docs/[0-9]` を置換すると、
存在しないファイルを指す参照が「docs/log/ に在るように見える」参照へ化けて、
かえって嘘が増える。だから **docs/log/ に実在するものだけ**を置換する。
"""

from __future__ import annotations

import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LOG = os.path.join(ROOT, "docs", "log")

# docs/log/ に実在する番号 = 置換してよい番号。
PREFIXES = sorted(
    {
        m.group(1)
        for name in os.listdir(LOG)
        if (m := re.match(r"^(\d{2})-", name))
    }
)

# docs/NN（後ろに数字が続かない）で、その NN が log/ に実在するときだけ張り替える。
BARE = re.compile(r"docs/(\d{2})(?!\d)")


def rewrite(body: str) -> str:
    body = body.replace("docs/history/", "docs/log/")
    return BARE.sub(
        lambda m: (
            f"docs/log/{m.group(1)}" if m.group(1) in PREFIXES else m.group(0)
        ),
        body,
    )


def tracked_files() -> list[str]:
    out = subprocess.run(
        ["git", "ls-files", "-z"], cwd=ROOT, capture_output=True, check=True
    ).stdout
    return [p.decode() for p in out.split(b"\0") if p]


def main() -> int:
    changed = 0
    for relpath in tracked_files():
        # docs/ の中は相対リンクなので別処理（このスクリプトは扱わない）。
        if relpath.startswith("docs/"):
            continue
        path = os.path.join(ROOT, relpath)
        try:
            with open(path, encoding="utf-8") as fh:
                body = fh.read()
        except (UnicodeDecodeError, IsADirectoryError, FileNotFoundError):
            continue
        new = rewrite(body)
        if new != body:
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(new)
            changed += 1
    print(f"rewrote {changed} file(s) outside docs/")
    print(f"prefixes treated as live in log/: {len(PREFIXES)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
