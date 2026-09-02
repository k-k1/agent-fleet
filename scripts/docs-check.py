#!/usr/bin/env python3
"""文書ツリーの構造検査。CI（.github/workflows/docs.yml）とローカルの両方で走る。

文書は読者で 3 つに分かれる（ADR 0064）。① プロダクト紹介＝ルートの README、
② 開発者向け＝`docs/`、③ 利用ガイド＝`guide/`。**`guide/` だけがコンテナへ配られる**
ので、ディレクトリの境界がそのまま配布の境界である。境界が崩れると読者の手元で
黙ってリンクが切れるので、規約は人間のレビューではなくここで機械検査する。

検査は 12 本:

  links      相対リンクの実在（アンカーは無視）
  anchors    #fragment が指す見出しが在る（Console の slug 規則で照合）
  closure    guide/ から外を指すリンクが無い＝配布物が自己完結している
  chapters   章番号がファイル名と一致し、相互参照のラベルとも一致する
  lang       二言語の閉包（en は .md へ、ja は .ja.md へ）と対訳の存在
  header     現役の棚の全ファイルに front matter（audience / source_of_truth / updated）
  vocab      利用者向けの棚に実装用語（AF_* / kind= / /api/）が漏れていない
  frozen     現役の棚から docs/log/（凍結アーカイブ）へリンクしていない
  ref        ref/ の表がコードの一次情報と一致し、かつ対訳と ✓ の立ち方が揃っている
  settings   設定タブの解説（member/12-settings）がタブの一覧（ref/settings）を覆っている
  features   機能カタログのメンバー向けの行が、利用者の棚（member/）の手順を指している
  knowledge  アシスタント知識が機能カタログのメンバー向けの行を覆っている
  notes      全コンテナへ配る運用ポリシーが、配られる棚だけを指している

ref は 3 段階で見る。(a) 軸の網羅: エージェントの列がセッション種別の定数を、
デプロイの行が runtime のプロファイルを覆っているか。(b) 行の一致: Caps() で
表現されている能力は**完全一致**（⊇ ではない——立っていない capability を ✓ に
するのが最悪の嘘）。(c) 対訳の一致: 表の ✓ の立ち方が en と ja で同じか。
対訳の存在だけ見ても「中身がずれた訳」は止まらず、能力表で片方だけ古いのは
表が 2 つあるのと同じ害になる。

`--strict` で warn を error に格上げする（移行中の棚を段階的に締めるため）。
"""

from __future__ import annotations

import argparse
import os
import re
import sys
import unicodedata
from dataclasses import dataclass, field

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# --- 2 つのツリー -------------------------------------------------------------
# 文書は読者で 3 つに分かれる（ADR 0064）。① プロダクト紹介はルートの README、
# 残る 2 つがここで検査するツリーである。
#
#   guide/ … ③ 利用ガイド。**コンテナへ配る唯一のツリー**で、全員が同じものを受け取る。
#   docs/  … ② 開発者向け。**誰にも配らない**（decisions / log / build / 規範）。
#
# 配布の境界がディレクトリの境界と一致していることが、この分割の全部である。
# guide/ から docs/ を指すリンクは、読者の手元では必ず切れる（check_closure）。
DOCS = os.path.join(ROOT, "docs")
GUIDE = os.path.join(ROOT, "guide")
TREES = (GUIDE, DOCS)

# --- 棚の分類 -----------------------------------------------------------------
# 現役 = 規範（header / lang / vocab / frozen / anchors）が全部かかる棚。
# 棚の名前は 2 ツリーを通して一意なので、どちらに在るかを言わなくても棚は決まる。
LIVING = ("member", "admin", "operate", "ref", "build")
# 利用者向け = 実装用語を書いてはいけない棚。operate/ は端末の前の読者向けなので
# コマンド・パス・変数を使ってよく、ref/ は「画面欄は Console・実装欄はコード」と
# 対応表そのものを載せる棚なので、どちらもここには入らない（CONVENTIONS §4）。
READER_FACING = ("member", "admin")
# guide/ ツリーの棚＝コンテナへ配られるもの。ロールでは切らない（ADR 0064）。
GUIDE_SHELVES = ("member", "admin", "operate", "ref")
# 二言語 = 英語が正（X.md）、日本語が併記（X.ja.md）。
# decisions/ は LIVING ではない（ADR は不変なので Updated: を持たない）が、二言語では
# ある——読者で切った棚と同じで、英語だけ読む人が決定の理由に届かないのは同じ欠損。
BILINGUAL = LIVING + ("decisions",)
# 日本語のみ = 二言語検査の対象外。log/ は凍結アーカイブ。
JA_ONLY_DIRS = ("log",)
JA_ONLY_FILES = (
    "docs/HANDOFF.md",
    "docs/CHANGELOG-handoff.md",
    "docs/roadmap.md",
)

# log/ への参照が許される現役ファイル。
FROZEN_REF_ALLOWLIST: set[str] = {
    "docs/log/README.md",
}

LINK_RE = re.compile(r"(?<!!)\[([^\]]*)\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
# front matter（--- で囲んだ YAML）。値は必ず二重引用符で書く——`Source of truth` の
# 値には「コマンドは deploy/ 配下のスクリプト、…」のようにコロンや読点が入る。
FM_RE = re.compile(r"^---\n(.*?)\n---\s*\n", re.S)
FM_KEY_RE = re.compile(r'^([a-z_]+):\s*"(.*)"\s*$', re.M)
FM_KEYS = ("audience", "source_of_truth", "updated")
UPDATED_RE = re.compile(r"^\d{4}-\d{2}$")

# 利用者向けの棚に出てはいけない実装用語。画面の名前で書くための歯止め。
VOCAB_BANNED = (
    (re.compile(r"\bAF_[A-Z][A-Z0-9_]+"), "env 変数名"),
    (re.compile(r"\bkind=[a-z]"), "内部の種別識別子"),
    (re.compile(r"(?<![\w/])/api/[a-z]"), "API パス"),
    (re.compile(r"(?<![\w/])/internal/[a-z]"), "内部 API パス"),
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
    """リポジトリ相対のパス（`guide/member/01-first-day.md`）。

    2 ツリーになったので、棚だけを返すと `README.md` がどちらのものか分からない。
    エラー文はそのまま `git` に渡せる形にしておく。
    """
    return os.path.relpath(path, ROOT).replace(os.sep, "/")


def tree(relpath: str) -> str:
    """`guide` か `docs`。配布されるかどうかがこれで決まる。"""
    return relpath.split("/", 1)[0]


def shelf(relpath: str) -> str:
    """棚の名前（`member` / `build` / `log` …）。ツリー直下のファイルは空文字。"""
    parts = relpath.split("/")
    return parts[1] if len(parts) > 2 else ""


def is_ja(relpath: str) -> bool:
    return relpath.endswith(".ja.md")


def counterpart(relpath: str) -> str:
    """en <-> ja のファイル名を入れ替える。"""
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
    if not s:  # ツリー直下: README / CONVENTIONS だけ二言語
        return os.path.basename(relpath).split(".")[0] in ("README", "CONVENTIONS")
    return s in BILINGUAL


# --- 検査 ---------------------------------------------------------------------


def check_links(files: list[str], f: Findings) -> None:
    for path in files:
        src = rel(path)
        body = strip_code(read(path))
        for m in LINK_RE.finditer(body):
            target = m.group(2)
            # 先頭 "/" はサイト絶対 URL（Console が返す open link の例示など）で、
            # リポジトリ内のパスではない。
            if target.startswith(("http://", "https://", "mailto:", "#", "/")):
                continue
            target = target.split("#", 1)[0]
            if not target:
                continue
            resolved = os.path.normpath(
                os.path.join(os.path.dirname(path), target)
            )
            # docs の外（../../deploy/... など）も同じ規則で実在を見る。
            if os.path.exists(resolved):
                continue
            f.error(f"{src}: リンク切れ -> {m.group(2)}")


# --- アンカー -----------------------------------------------------------------

INLINE_MARKUP = (
    (re.compile(r"\[([^\]]*)\]\([^)]*\)"), r"\1"),  # リンクは表示文字だけ残る
    (re.compile(r"`([^`]*)`"), r"\1"),
    (re.compile(r"\*\*([^*]*)\*\*"), r"\1"),
    (re.compile(r"\*([^*]*)\*"), r"\1"),
)
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*?)\s*$", re.M)


def heading_text(raw: str) -> str:
    """見出し行から、ブラウザが `textContent` で見るのと同じ文字列を作る。"""
    for pattern, repl in INLINE_MARKUP:
        raw = pattern.sub(repl, raw)
    return raw


def console_slug(text: str) -> str:
    """Console が見出しに振る id。**GitHub の規則ではない。**

    正は `console/src/lib/filemeta.ts` の `slug()`——小文字化して trim、
    文字・数字・空白・ハイフン以外を捨て、**連続する空白を 1 個の**ハイフンにする。

    GitHub（github-slugger）は空白 1 個につきハイフン 1 個なので、`—` や `/` のように
    **空白に挟まれた記号**を含む見出しで 2 つの規則は食い違う（`a — b` は Console で
    `a-b`、GitHub で `a--b`）。全角括弧はさらに逆で、Console は捨て GitHub は残す。

    どちらを正にするかは実測で決めた: リポジトリ全体で Console 規則でしか解決しない
    リンクが 52 本、GitHub 規則でしかないものが 10 本。**読者がガイドを開くのは
    Console** でもあり（`Source of truth` は Console）、多数派でもあるのでこちらを採る。
    """
    t = text.lower().strip()
    t = "".join(
        c for c in t if unicodedata.category(c)[0] in ("L", "N") or c in " -"
    )
    return re.sub(r"\s+", "-", t)


# github-slugger: 小文字化して trim、句読点類（`—` を含む  -⁯ と ASCII 記号）を
# 捨て、**空白 1 個につきハイフン 1 個**。`-` と `_` と全角括弧は残る。
GITHUB_PUNCT_RE = re.compile(
    "[ -⁯⸀-⹿\\\\'!\"#$%&()*+,./:;<=>?@\\[\\]^`{|}~]"
)


def github_slug(text: str) -> str:
    return re.sub(r"\s", "-", GITHUB_PUNCT_RE.sub("", text.lower().strip()))


def heading_slugs(path: str) -> set[str]:
    """`path` を描画する側の規則で作った、その文書の見出し id の集合。

    **規則は行き先のツリーで決まる。** `guide/` は読者がコンテナの Console で開く
    ものなので Console の `slug()`、`docs/` とリポジトリ直下（CONTRIBUTING.md など）は
    GitHub でしか読まれないので github-slugger。ここを一律にすると、
    `CONTRIBUTING.md#commits--prs` のような **GitHub では正しいアンカー**を
    「壊れている」と報告してしまう（実際にそうなった）。
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
    """`#fragment` が、その先のファイルに実在する見出しを指しているか。

    `check_links` はファイルの実在しか見ておらず、**アンカーは無視していた**。
    だから「ページは開くが、そこではない場所に飛ぶ」——読者から見れば切れたリンクと
    同じもの——が検査を素通りしていた。
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
                continue  # 実在しないファイルは check_links の担当
            if frag in heading_slugs(dest):
                continue
            who = "Console" if dest.startswith(GUIDE + os.sep) else "GitHub"
            f.error(
                f"{src}: 見出しの無いアンカー -> {target}"
                f"（{who} が振る id と一致していない）"
            )


# --- 配布物の閉包 -------------------------------------------------------------

# guide/ から外を指してよいリンク（プレフィックス -> 理由）。
# ⚠️ 理由つきで明示する。ここに足す前に「読者はコンテナの中でそこへ辿り着けるのか」を
# 確かめること——辿り着けないなら、それは例外ではなく直すべきリンクである。
_RUNBOOK_REASON = (
    "runbook は操作する対象の隣に置いてあり、リリースバンドルの中身そのもの。"
    "deploy/release/stage-docs.sh が配布時に operate/runbooks/ へ複製し、"
    "同時にこのリンクをそちらへ書き換えるので、GitHub では deploy/、"
    "コンテナでは runbooks/ と、両方で生きたリンクになる"
)
# ⚠️ 個別のパスで持つ。以前ここは `deploy/` というプレフィックスだった——書き換えの
# 対象は runbook 5 本だけなのに、`deploy/compose/.env.example` への 6 本まで一緒に
# 免除してしまい、**配布物の中では死んでいるリンクが緑のまま**だった。
# 例外は「なぜ届くのか」を 1 本ずつ説明できる形でしか持たない。
CLOSURE_EXEMPT: dict[str, str] = {
    "deploy/compose/README.md": _RUNBOOK_REASON,
    "deploy/native/README.md": _RUNBOOK_REASON,
    "deploy/local/README-wsl.md": _RUNBOOK_REASON,
    "deploy/aws/ecs/README.md": _RUNBOOK_REASON,
    "deploy/aws/ec2-single/README.md": _RUNBOOK_REASON,
}


def check_closure(files: list[str], f: Findings) -> None:
    """配布物（guide/）が自己完結しているか——外を指すリンクが 1 本も無いこと。

    これが利用者の「リンク切れが多い」の正体だった。`check_links` は**リポジトリ上の
    実在**しか見ないので、`guide/` から開発者向けの `docs/` を指すリンクは緑のまま
    通る。しかし読者が開くのはコンテナへ配られたツリーで、そこに `docs/` は無い。
    リポジトリでは在るのに読者の手元では必ず切れる、という一群がこうして残っていた。

    散文での言及は対象外。**リンクだけを見る**——「仕組みは開発者向けの資料にあります」
    と書くのは正しく、それをクリックできるようにするのが誤りである。
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
                f"{src}: 配布物の外を指している -> {target}（{out}）"
                "——コンテナへ配られるのは guide/ だけなので、読者の手元では切れる"
            )


# --- 章番号 -------------------------------------------------------------------

CHAPTER_FILE_RE = re.compile(r"^(\d{2})-")
# H1 の「NN.」と、本文の相互参照ラベルの「NN 章名」。どちらも同じ番号を指すべき。
H1_NUM_RE = re.compile(r"^#\s+(\d{1,2})\.\s")
LABEL_NUM_RE = re.compile(r"^(\d{1,2})[.\s]")


def chapter_of(relpath: str) -> str | None:
    m = CHAPTER_FILE_RE.match(os.path.basename(relpath))
    return m.group(1) if m else None


def check_chapters(files: list[str], f: Findings) -> None:
    """章番号が 1 つに揃っているか。

    番号付きのファイルには番号付きの H1 があり、他の章から「NN 章名」と呼ばれる。
    3 つが揃っていないと、読者は索引で「11 困ったとき」と読み、開いた先で
    「09. 困ったとき」を見ることになる——実際そうなっていて、しかも
    `09-collaboration` と `11-troubleshooting` が**両方 09 を名乗っていた**。
    番号は目次であって飾りではないので、ファイル名を正として機械で揃える。
    """
    numbers = {rel(p): chapter_of(rel(p)) for p in files}
    for path in files:
        src = rel(path)
        if shelf(src) not in LIVING:
            continue
        want = numbers[src]
        body = read(path)
        # front matter を挟むので、H1 は「最初の行」ではなく「最初の `# ` 行」。
        h1 = next((ln for ln in body.splitlines() if ln.startswith("# ")), "")
        got = H1_NUM_RE.match(h1)
        if want and not got:
            f.error(f"{src}: H1 に章番号が無い（「# {want}. …」で始めること）")
        elif want and got.group(1).zfill(2) != want:
            f.error(
                f"{src}: H1 の章番号がファイル名と違う"
                f"（H1={got.group(1)} / ファイル名={want}）"
            )
        elif not want and got:
            f.error(
                f"{src}: 番号の無いファイルに章番号が付いている（H1={got.group(1)}）"
            )
        # 相互参照のラベル「NN 章名」が、指す先の番号と一致しているか。
        for m in LINK_RE.finditer(strip_code(body)):
            label, target = m.group(1), m.group(2).split("#", 1)[0]
            lm = LABEL_NUM_RE.match(label.strip())
            if not lm or not target.endswith(".md"):
                continue
            dest = os.path.normpath(os.path.join(os.path.dirname(path), target))
            if not os.path.exists(dest):
                continue  # check_links の担当
            dest_num = chapter_of(rel(dest))
            if dest_num and lm.group(1).zfill(2) != dest_num:
                f.error(
                    f"{src}: 相互参照の章番号が違う -> [{label}]({target})"
                    f"（指し先は {dest_num}）"
                )


def check_lang(files: list[str], f: Findings) -> None:
    present = {rel(p) for p in files}
    for path in files:
        src = rel(path)
        if not bilingual_scope(src):
            continue
        mate = counterpart(src)
        if mate not in present:
            f.error(f"{src}: 対訳が無い（{mate} が必要）")
        body = strip_code(read(path))
        for m in LINK_RE.finditer(body):
            target = m.group(2).split("#", 1)[0]
            if target.startswith(("http://", "https://", "mailto:")) or not target:
                continue
            if not target.endswith(".md"):
                continue
            dest = os.path.normpath(os.path.join(os.path.dirname(path), target))
            if not any(dest.startswith(b + os.sep) for b in TREES):
                continue  # 2 ツリーの外（deploy/ など）は言語を持たない
            dest_rel = rel(dest)
            if not bilingual_scope(dest_rel):
                continue  # 日本語のみの棚へは両言語から同じターゲットを指す
            if dest_rel == mate:
                continue  # H1 直後の言語スイッチャ行。唯一の正当な言語またぎ
            if is_ja(src) != is_ja(dest_rel):
                want = counterpart(dest_rel)
                f.error(
                    f"{src}: 言語をまたぐリンク -> {target}（{want} を指すこと）"
                )


def front_matter(path: str) -> dict[str, str] | None:
    """先頭の YAML front matter を key -> value で返す。無ければ None。

    Console は front matter を本文と切り分けてメタデータ枠に描く
    （`console/src/features/viewer/MarkdownView.tsx` の `splitYamlFrontMatter`）ので、
    機械のための行が地の文に混ざらない。ここも文字列一致ではなく構造として読む。
    """
    m = FM_RE.match(read(path))
    if m is None:
        return None
    return dict(FM_KEY_RE.findall(m.group(1)))


def is_shelf_readme(relpath: str) -> bool:
    return os.path.basename(relpath) in ("README.md", "README.ja.md")


def check_header(files: list[str], f: Findings, strict: bool) -> None:
    """現役の棚の全ファイルに front matter が在るか。

    `source_of_truth` だけは**棚の README から継承してよい**。`guide/member/` の 16 枚と
    `guide/admin/` の 6 枚は値が一字句同じで、同じ一文が 22 回並んでいた——読者にとっては
    情報量ゼロの定型である。値が棚ごとに違う `guide/ref/`（10 枚すべて違う）や
    `guide/operate/` では、これは矛盾に出会った読者がどちらを信じるかを決める本物の
    情報なので、各ファイルに書く。
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
                f"{src}: 冒頭に front matter が無い"
                "（--- で囲んで audience / source_of_truth / updated）"
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
                continue  # 棚の README が代表して宣言している
            f.error(f"{src}: front matter に {key} が無い")
        if "updated" in fm and not UPDATED_RE.match(fm["updated"]):
            f.error(f"{src}: updated は YYYY-MM 形式で書く（いまは {fm['updated']!r}）")


def check_vocab(files: list[str], f: Findings, strict: bool) -> None:
    for path in files:
        src = rel(path)
        if shelf(src) not in READER_FACING:
            continue
        body = strip_code(read(path))
        for pattern, label in VOCAB_BANNED:
            hit = pattern.search(body)
            if hit:
                msg = f"{src}: 利用者向けの棚に{label}が出ている（{hit.group(0)}）"
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
                    f"{src}: 凍結アーカイブ log/ を参照している -> {target}"
                    "（事実は新しい文書へ転記すること）"
                )


# --- ref/ の軸をコードの一次情報と突き合わせる --------------------------------


def source_kinds() -> set[str]:
    path = os.path.join(
        ROOT, "workspace", "agent", "internal", "session", "session.go"
    )
    body = read(path)
    return set(re.findall(r'^\s*Kind\w+\s*=\s*"([a-z]+)"', body, re.M))


def source_caps() -> dict[str, set[str]]:
    """kind -> その Caps() が true にしているフィールド名の集合。

    workspace/agent/internal/agents/<kind>/ の Caps() メソッド本体だけを見る。
    ここが「この種別に何ができるか」の実装側の一次情報で、ref/agents.md の
    該当行はこれと一致していなければならない。
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
                # メソッド本体は最初の "\n}" まで。Caps は素の構造体リテラルを
                # 返すだけなので、これで十分かつ誤爆しない。
                end = body.find("\n}", m.end())
                blk = body[m.end() : end if end > 0 else len(body)]
                fields |= set(re.findall(r"(\w+)\s*:\s*true", blk))
        if fields:
            out[kind] = fields
    return out


def table_check_marks(path: str, row_label: str) -> set[str] | None:
    """表の行 row_label で ✓ が立っている列名の集合。行が無ければ None。"""
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


# ランタイム形態の switch が在るファイルと関数名。**置き場は移送で動く** ——
# ADR 0067 のエイリアス移送で control-plane/runtime.go の newRuntimeFactory は
# control-plane/internal/runtime/runtime.go の NewFactory へ移り、main 側に残ったのは
# runtime.NewFactory を呼ぶだけの薄い包みになった。新しい方から順に当たる。
RUNTIME_FACTORY_SOURCES = (
    (("control-plane", "internal", "runtime", "runtime.go"), "func NewFactory("),
    (("control-plane", "runtime.go"), "func newRuntimeFactory("),
)


def source_runtime_groups() -> list[set[str]]:
    """デプロイ形態を「別名の組」として返す。

    ランタイム工場の switch は 1 つのアダプタに複数の綴りを許している
    （local=docker / ecs=aws / native=wsl）。表がどの綴りを採っていても
    通したいので、組のどれか 1 つが在れば満たしたとみなす。組の一覧は
    その switch から、必須の集合は同じ関数の「want ...」エラー文から取る。

    見つからないときは**落とす**。ここで [] を返すと deploy-targets.md の検査だけが
    黙って消え、表が腐っても緑のままになる（移送でファイルが動いた時こそ壊れる検査
    なので、静かに無効化されるのが最も高くつく）。
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
            "docs-check: ランタイム形態の switch が見つからない。移送で置き場が"
            "変わったなら scripts/docs-check.py の RUNTIME_FACTORY_SOURCES に足すこと（探した先: "
            + ", ".join("/".join(p) + " の " + f for p, f in RUNTIME_FACTORY_SOURCES)
            + "）"
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
    # 必須集合に触れている組だけを検査対象にする。
    return [g for g in groups if g & required]


def table_columns(path: str) -> set[str]:
    """先頭の表のヘッダ行からセル（1列目を除く）を取り出す。"""
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
    """Console の設定タブのラベル（キー -> 表示文字列）。

    利用者が探すのは画面に出ている名前なので、ref/settings.md の行は
    **Console のラベルそのもの**でなければならない。タブが増えたら行が増える。
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
    """ファイル内の全表を「印だけ」に潰した形。行の順序も保つ。

    セルは ✓ / — / それ以外（散文）の 3 値にする。訳文の言い回しは無視して、
    **どこに ✓ が立っているか**だけを比べるための正規化。
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
    """アシスタント知識が機能カタログのメンバー向けの行を覆っているか。

    `workspace/agent/knowledge/af-usage.md` は `docs/use/` の要約で、//go:embed で
    エージェントのバイナリに焼かれる。生成物にはできない——エージェントのイメージの
    ビルド文脈は `workspace/agent/` だけなので `docs/` が見えないし、あれは散文を
    連結したものではなく「読ませる順」に畳んだプロンプトである。

    手写しである以上は必ず遅れるので、遅れたことが分かるようにする。突き合わせるのは
    ref/features.ja.md の**メンバー向けの行**（管理者・運用者向けの行は、読者が違うので
    このアシスタントの守備範囲ではない）。

    ⚠️ 検査できるのは「その機能について書くと決めた記録が在るか」までで、書いた中身が
    正しいかではない。台帳が「覆った」と言っているだけの可能性は消せない——消せるのは
    「カタログに増えた行を誰も見なかった」の方だけである。
    """
    cat = os.path.join(GUIDE, "ref", "features.ja.md")
    doc = os.path.join(ROOT, "workspace", "agent", "knowledge", "af-usage.md")
    led = os.path.join(ROOT, "workspace", "agent", "knowledge", "af-usage.coverage.tsv")
    if not (os.path.exists(cat) and os.path.exists(doc) and os.path.exists(led)):
        return

    # カタログのメンバー向けの行（「誰が」列が「メンバー」で始まるもの）。
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
            f.error(f"af-usage.coverage.tsv:{i}: タブ区切りでない -> {line!r}")
            continue
        name, where = line.split("\t", 1)
        ledger[name.strip()] = where.strip()

    sections = set(re.findall(r"^##\s+(\d+)\.", read(doc), re.M))

    for name in wanted:
        if name not in ledger:
            f.error(
                f"af-usage.coverage.tsv: 機能カタログの行が台帳に無い -> 「{name}」"
                "（アシスタント知識に書くか、書かない理由を `-` で添えること）"
            )
            continue
        where = ledger[name]
        if where.startswith("-"):
            if not where[1:].strip():
                f.error(
                    f"af-usage.coverage.tsv:「{name}」を対象外にするなら理由を書く"
                )
        elif where not in sections:
            f.error(
                f"af-usage.coverage.tsv:「{name}」が af-usage.md に無い章を指している"
                f" -> {where}"
            )

    for name in ledger:
        if name not in wanted:
            f.error(
                f"af-usage.coverage.tsv: 機能カタログに無い行が残っている -> 「{name}」"
                "（カタログ側で消えたか名前が変わった）"
            )


def table_first_column_under(path: str, heading: str) -> list[str]:
    """`## <heading>` の節の中にある表の 1 列目（ヘッダ行を除く・出現順）。

    ref/settings.md には表が 3 つある（個人設定 / テナント設定 / 配備の変数）ので、
    ファイル全体から拾うと**テナント設定のタブまで use/ に要求してしまう**。
    あちらは admin/ の担当で、読者が違う。
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
        if not header_seen:  # 表の見出し行
            header_seen = True
            continue
        if cell:
            rows.append(cell)
    return rows


def heading3_under(path: str, groups: tuple[str, ...]) -> list[str]:
    """グループ見出し（`## <groups のどれか>`）の下にある `###` を出現順に返す。

    グループの外の `###`（「いつ反映されるか」など）はタブではないので拾わない。
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


# ref/settings.ja.md の個人設定タブのうち、use/12-settings.ja.md に節を持たなくてよいもの
# （タブ名 -> 理由）。⚠️ 除外は必ず理由つきで明示する。理由なしで足せる除外リストは、
# 「落ちたから消す」で埋まって素通りする検査に戻る。いまは 1 件も無い。
USE_SETTINGS_EXEMPT: dict[str, str] = {}

USE_SETTINGS_GROUPS_JA = ("個人設定", "接続", "ワークスペース")
USE_SETTINGS_GROUPS_EN = ("Personal", "Connections", "Workspace")


def check_use_settings(f: Findings) -> None:
    """設定タブの解説（use/12-settings）が、タブの一覧（ref/settings）を覆っているか。

    ref/settings の行は check_ref が Console のラベルと突き合わせているので、**画面が
    増えれば ref/ には必ず行が増える**。しかし use/ を見る検査が無かったため、ref/ が
    正しいまま use/ だけ穴が開いても全部緑のままだった（実際、課題管理とクラウド費用の
    2 タブが**両言語とも**欠けていて、しかも ref/features が「意味は 12 設定」と
    その空席を指していた）。ここはその 1 段を足す。

    突き合わせるのは**日本語版どうし**。英語の ref/settings.md の行は Console の英語
    ラベル（Keyboard / Read aloud …）で、use/12-settings.md の節見出し（Keys / Speech …）
    とは一字一句同じではなく、名前で突き合わせられるのは ja 側だけである。英語版は
    **節の数が対訳と同じか**で見る——片方の言語にだけ節を足した、はこれで捕まる。

    ⚠️ 見るのは ref/settings.ja.md の**個人設定の表だけ**（`table_first_column_under`）。
    """
    ref = os.path.join(GUIDE, "ref", "settings.ja.md")
    use_ja = os.path.join(GUIDE, "member", "12-settings.ja.md")
    use_en = os.path.join(GUIDE, "member", "12-settings.md")
    if not all(os.path.exists(p) for p in (ref, use_ja, use_en)):
        return

    tabs = table_first_column_under(ref, "個人設定")
    if not tabs:
        f.error("ref/settings.ja.md: 個人設定の表が読めない（見出しか表の形が変わった）")
        return
    sections = heading3_under(use_ja, USE_SETTINGS_GROUPS_JA)

    for tab in tabs:
        if tab in USE_SETTINGS_EXEMPT:
            continue
        if tab not in sections:
            f.error(
                f"use/12-settings.ja.md: 設定タブの節が無い -> 「### {tab}」"
                "（ref/settings.ja.md に在るタブは、両言語に節を書くこと）"
            )
    for name in sections:
        if name not in tabs:
            f.error(
                f"use/12-settings.ja.md: タブに無い節が残っている -> 「### {name}」"
                "（Console でタブが消えたか改名された）"
            )

    en = heading3_under(use_en, USE_SETTINGS_GROUPS_EN)
    if len(en) != len(sections):
        f.error(
            "use/12-settings.md: タブの節の数が対訳と違う"
            f"（en={len(en)} / ja={len(sections)}）"
        )


# 機能カタログのメンバー向けの行のうち、詳細列が use/ を指さなくてよいもの
# （機能名 -> 理由）。⚠️ 理由つきで明示する。ここに足す前に「読者は本当に手順へ
# 辿り着けるのか」を確かめること——`use/` を指していないのに例外にした行が、
# まさに課題管理が 1 章も持たないまま緑だった形である。
FEATURES_EXEMPT: dict[str, str] = {}

# 詳細列の見出し（ロケール -> (ファイル名, 見出し語, メンバーを表す語)）。
FEATURES_FILES = (
    ("features.ja.md", "詳細", "メンバー"),
    ("features.md", "Details", "member"),
)


def check_features(f: Findings) -> None:
    """機能カタログのメンバー向けの行が、利用者の棚（member/）の手順を指しているか。

    `ref/features` は「在るか・誰が使えるか」の索引で、**どうやるかはリンク先**だと
    自分で宣言している。だから詳細列が能力表（`agents.md` / `repos.md`）しか指して
    いない行は、読者にとっては行き止まりである——実際、作業項目の受信箱の行は
    `repos.md`（どのプロバイダが何を出すか）だけを指しており、**機能そのものを書いた
    章が use/ に 1 つも無いまま**カタログは埋まって見えていた。

    見るのは**メンバー向けの行だけ**。管理者・運用者の行は読者が違い、行き先は
    `admin/` `operate/` `ref/` になる。詳細列を持たない表（自分の設定）は、節の
    前文が棚を指す形なので対象外——表の見出しに「詳細」が無いことで自動的に外れる。

    ⚠️ 両言語それぞれを見る。`check_ref_parity` が見ているのは表の ✓ の形だけなので、
    **片方の言語の行き先だけが古い**のはそこでは止まらない。
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
            if details_head in cells:  # 表の見出し行
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
                    f"guide/ref/{name}:「{feature}」の詳細が member/ を指していない"
                    f"（{details or '空'}）"
                    "——メンバー向けの行は、やり方が読める章を必ず 1 つ指すこと"
                )


def check_notes(f: Findings) -> None:
    """全コンテナへ配る運用ポリシーが、コンテナに実在する棚だけを指しているか。

    `workspace/workspace-notes.md` はイメージに焼かれ、**すべてのエージェントが
    起動時に読む**。ツリーを並べ替えたときここが取り残されると、1 か所の腐りが
    全コンテナの全セッションを同時に誤誘導する——しかも読み手は指示に従うだけなので、
    誰も異常だと気づかない（実際 P4 の棚の付け替えで `dev/93-…` が残っていた）。

    規則は 1 つ: **名指しするなら `guide/` の棚**。コンテナへ配られるのはそのツリーだけで、
    `docs/`（開発者向け）は誰のコンテナにも無い。

    ⚠️ かつてここには「保証されない棚を指すなら同じ段落に『may be absent』と断れ」という
    規則があった。mount が**役割別**で、member は use/ と ref/ しか受け取らなかったからである。
    ロール別配布をやめた（ADR 0064）ので、断り書きで逃げる余地も必要も無くなった——
    指せるか指せないかの 2 つに 1 つで、指せないものは書き換える。
    """
    notes = os.path.join(ROOT, "workspace", "workspace-notes.md")
    if not os.path.exists(notes):
        return
    shelves = LIVING + ("decisions", "log")
    ref_re = re.compile(
        r"`(" + "|".join(shelves) + r")/([A-Za-z0-9._-]+\.md)`"
    )
    # 断り書きは**段落**の中で探す。行で探すと、折り返しただけで落ちる
    # ——「同じ行に書け」は書式の制約であって、意味の制約ではない。
    lines = read(notes).splitlines()
    blocks: list[tuple[int, str]] = []  # (先頭行番号, 段落)
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
                    f"workspace-notes.md:{lineno}: 配られない棚を指している"
                    f" -> {name}/{fname}"
                    "（コンテナに在るのは guide/ だけ。guide/ の棚を指すこと）"
                )
                continue
            if not os.path.exists(os.path.join(GUIDE, name, fname)):
                f.error(
                    f"workspace-notes.md:{lineno}: 無い棚のファイルを指している"
                    f" -> {name}/{fname}"
                    "（全エージェントが読む指示なので、腐ると全員が誤誘導される）"
                )


def check_ref_parity(f: Findings) -> None:
    """ref/ の表は、英語版と日本語版で ✓ の立ち方が一致していること。

    対訳の存在は lang 検査が見るが、それだけでは**中身がずれた訳**を止められない。
    能力表で片方だけ古いのは、表が 2 つあるのと同じ害になる。
    """
    refdir = os.path.join(GUIDE, "ref")
    if not os.path.isdir(refdir):
        return
    for name in sorted(os.listdir(refdir)):
        if not name.endswith(".md") or name.endswith(".ja.md"):
            continue
        ja = os.path.join(refdir, name[: -len(".md")] + ".ja.md")
        if not os.path.exists(ja):
            continue  # lang 検査が別途報告する
        en_shape = table_mark_shape(os.path.join(refdir, name))
        ja_shape = table_mark_shape(ja)
        if len(en_shape) != len(ja_shape):
            f.error(
                f"ref/{name}: 表の行数が対訳と違う"
                f"（en={len(en_shape)} / ja={len(ja_shape)}）"
            )
            continue
        for i, (a, b) in enumerate(zip(en_shape, ja_shape)):
            if a != b:
                f.error(
                    f"ref/{name}: 表の {i + 1} 行目で ✓ の立ち方が対訳と違う"
                    f"（en={''.join(a)} / ja={''.join(b)}）"
                )
                break


def check_ref(f: Findings) -> None:
    agents = os.path.join(GUIDE, "ref", "agents.md")
    if os.path.exists(agents):
        cols = {c.strip("`*") for c in table_columns(agents)}
        missing = source_kinds() - cols
        if missing:
            f.error(
                "ref/agents.md: コードにある種別が表に無い -> "
                + ", ".join(sorted(missing))
            )
    # Caps() で表現されている行は、実装と 1:1 で一致していなければならない
    # （⊇ ではなく完全一致 — 立っていない capability を ✓ にするのが最悪の嘘）。
    if os.path.exists(agents):
        caps = source_caps()
        for row, field in (
            ("Copy the conversation into a new session", "CanFork"),
            ("Fork from a past message", "CanForkAt"),
            ("Choosing to skip permission prompts", "PermissionChoice"),
        ):
            marked = table_check_marks(agents, row)
            if marked is None:
                f.error(f"ref/agents.md: 行が見つからない -> 「{row}」")
                continue
            want = {k for k, fields in caps.items() if field in fields}
            if marked != want:
                f.error(
                    f"ref/agents.md「{row}」が実装の {field} と食い違う"
                    f"（表={sorted(marked)} / コード={sorted(want)}）"
                )

    # 設定タブは Console のラベルが正。増えた画面が黙って未記載にならないよう、
    # 両言語それぞれを自分のロケールのラベルと突き合わせる。
    for locale, name in (("en", "settings.md"), ("ja", "settings.ja.md")):
        path = os.path.join(GUIDE, "ref", name)
        tabs = source_setting_tabs(locale)
        if not os.path.exists(path) or not tabs:
            continue
        rows = table_first_column(path)
        missing = sorted({v for v in tabs.values()} - rows)
        if missing:
            f.error(
                f"ref/{name}: Console にある設定タブが表に無い -> "
                + ", ".join(missing)
            )

    # ⚠️ 両言語をまわす。英語版だけを見ていた頃は、`.ja.md` の表を壊しても緑だった
    # （10 行上の設定タブ検査は最初からループしている——同じ形に揃えただけ）。
    for name in ("deploy-targets.md", "deploy-targets.ja.md"):
        targets = os.path.join(GUIDE, "ref", name)
        if not os.path.exists(targets):
            continue
        rows = {c.strip("`*") for c in table_first_column(targets)}
        for group in source_runtime_groups():
            if not (group & rows):
                f.error(
                    f"ref/{name}: コードにある形態が表に無い -> "
                    + "|".join(sorted(group))
                )


# --- helpers ------------------------------------------------------------------

FENCE_RE = re.compile(r"```.*?```", re.S)
INLINE_RE = re.compile(r"`[^`\n]*`")


def strip_code(body: str) -> str:
    """語彙検査はコードブロックとインラインコードを見ない。

    利用者向けの文章でも、設定ファイルの実例や env の一覧を「引用として」
    載せることはある。禁じたいのは地の文で実装用語を使うことなので、
    コードとして明示された部分は対象外にする。
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
        "--strict", action="store_true", help="warn を error に格上げする"
    )
    ap.add_argument(
        "--only",
        default="",
        help=(
            "検査名をカンマ区切りで指定"
            "（links,anchors,closure,chapters,lang,header,vocab,frozen,"
            "ref,settings,features,knowledge,notes）"
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
