#!/usr/bin/env python3
"""docs/ の構造検査。CI（.github/workflows/docs.yml）とローカルの両方で走る。

docs/ は「読者で切った棚」であり、その構造がそのまま配布の権限になる
（control-plane/workspace_docs.go の docsRolePrefixes）。棚の規約が崩れると
配布範囲が静かに変わるので、規約は人間のレビューではなくここで機械検査する。

検査は 10 本:

  links      相対リンクの実在（アンカーは無視）
  lang       二言語の閉包（en は .md へ、ja は .ja.md へ）と対訳の存在
  header     現役の棚の全ファイルに Audience / Source of truth / Updated
  vocab      利用者向けの棚に実装用語（AF_* / kind= / /api/）が漏れていない
  frozen     現役の棚から docs/log/（凍結アーカイブ）へリンクしていない
  ref        ref/ の表がコードの一次情報と一致し、かつ対訳と ✓ の立ち方が揃っている
  settings   設定タブの解説（use/12-settings）がタブの一覧（ref/settings）を覆っている
  features   機能カタログのメンバー向けの行が、利用者の棚（use/）の手順を指している
  knowledge  アシスタント知識が機能カタログのメンバー向けの行を覆っている
  notes      全コンテナへ配る運用ポリシーが実在する棚だけを指している

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
from dataclasses import dataclass, field

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOCS = os.path.join(ROOT, "docs")

# --- 棚の分類 -----------------------------------------------------------------
# 現役 = これから書く新体系。規範（header / lang / vocab / frozen）が全部かかる。
LIVING = ("use", "admin", "operate", "build", "ref")
# 利用者向け = 実装用語を書いてはいけない棚。
READER_FACING = ("use", "admin")
# 二言語 = 英語が正（X.md）、日本語が併記（X.ja.md）。
# decisions/ は LIVING ではない（ADR は不変なので Updated: を持たない）が、二言語では
# ある——読者で切った棚と同じで、英語だけ読む人が決定の理由に届かないのは同じ欠損。
BILINGUAL = LIVING + ("guide", "decisions")
# 日本語のみ = 二言語検査の対象外。log/ は凍結、dev/ と guide 以外の旧棚は移行待ち。
JA_ONLY_DIRS = ("dev", "log")
JA_ONLY_FILES = ("HANDOFF.md", "CHANGELOG-handoff.md", "roadmap.md")

# log/ への参照が許される現役ファイル。P4 までに空にする（plan の受け入れ条件）。
FROZEN_REF_ALLOWLIST: set[str] = {
    "log/README.md",
}

LINK_RE = re.compile(r"(?<!!)\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
HEADER_KEYS = ("Audience:", "Source of truth:", "Updated:")
UPDATED_RE = re.compile(r"^Updated:\s*(\d{4})-(\d{2})\s*$", re.M)

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
    return os.path.relpath(path, DOCS).replace(os.sep, "/")


def top(relpath: str) -> str:
    return relpath.split("/", 1)[0]


def is_ja(relpath: str) -> bool:
    return relpath.endswith(".ja.md")


def counterpart(relpath: str) -> str:
    """en <-> ja のファイル名を入れ替える。"""
    if is_ja(relpath):
        return relpath[: -len(".ja.md")] + ".md"
    return relpath[: -len(".md")] + ".ja.md"


def all_docs() -> list[str]:
    out = []
    for dirpath, dirnames, filenames in os.walk(DOCS):
        dirnames[:] = [d for d in dirnames if not d.startswith(".")]
        for name in filenames:
            if name.endswith(".md"):
                out.append(os.path.join(dirpath, name))
    return sorted(out)


def bilingual_scope(relpath: str) -> bool:
    if top(relpath) in JA_ONLY_DIRS or relpath in JA_ONLY_FILES:
        return False
    if "/" not in relpath:  # docs 直下: README / CONVENTIONS だけ二言語
        return relpath.split(".")[0] in ("README", "CONVENTIONS")
    return top(relpath) in BILINGUAL


# --- 検査 ---------------------------------------------------------------------


def check_links(files: list[str], f: Findings) -> None:
    for path in files:
        src = rel(path)
        body = strip_code(read(path))
        for m in LINK_RE.finditer(body):
            target = m.group(1)
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
            f.error(f"{src}: リンク切れ -> {m.group(1)}")


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
            target = m.group(1).split("#", 1)[0]
            if target.startswith(("http://", "https://", "mailto:")) or not target:
                continue
            if not target.endswith(".md"):
                continue
            dest = os.path.normpath(os.path.join(os.path.dirname(path), target))
            if not dest.startswith(DOCS):
                continue  # docs 外（deploy/ など）は言語を持たない
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


def check_header(files: list[str], f: Findings, strict: bool) -> None:
    for path in files:
        src = rel(path)
        if top(src) not in LIVING:
            continue
        head = "\n".join(read(path).splitlines()[:12])
        missing = [k for k in HEADER_KEYS if k not in head]
        if missing:
            f.error(f"{src}: 冒頭ヘッダが無い（{', '.join(missing)}）")
            continue
        if not UPDATED_RE.search(head):
            f.error(f"{src}: Updated: は YYYY-MM 形式で書く")


def check_vocab(files: list[str], f: Findings, strict: bool) -> None:
    for path in files:
        src = rel(path)
        if top(src) not in READER_FACING:
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
        if top(src) not in LIVING:
            continue
        if src in FROZEN_REF_ALLOWLIST:
            continue
        for m in LINK_RE.finditer(strip_code(read(path))):
            target = m.group(1)
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


def source_runtime_groups() -> list[set[str]]:
    """デプロイ形態を「別名の組」として返す。

    newRuntimeFactory の switch は 1 つのアダプタに複数の綴りを許している
    （local=docker / ecs=aws / native=wsl）。表がどの綴りを採っていても
    通したいので、組のどれか 1 つが在れば満たしたとみなす。組の一覧は
    その switch から、必須の集合は同じ関数の「want ...」エラー文から取る。
    """
    body = read(os.path.join(ROOT, "control-plane", "runtime.go"))
    start = body.find("func newRuntimeFactory(")
    if start < 0:
        return []
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
    cat = os.path.join(DOCS, "ref", "features.ja.md")
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
    ref = os.path.join(DOCS, "ref", "settings.ja.md")
    use_ja = os.path.join(DOCS, "use", "12-settings.ja.md")
    use_en = os.path.join(DOCS, "use", "12-settings.md")
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
    """機能カタログのメンバー向けの行が、利用者の棚（use/）の手順を指しているか。

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
        path = os.path.join(DOCS, "ref", name)
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
            if "../use/" not in details:
                f.error(
                    f"ref/{name}:「{feature}」の詳細が use/ を指していない"
                    f"（{details or '空'}）"
                    "——メンバー向けの行は、やり方が読める章を必ず 1 つ指すこと"
                )


def check_notes(f: Findings) -> None:
    """全コンテナへ配る運用ポリシーが、実在する棚だけを指しているか。

    `workspace/workspace-notes.md` はイメージに焼かれ、**すべてのエージェントが
    起動時に読む**。docs/ を並べ替えたときここが取り残されると、1 か所の腐りが
    全コンテナの全セッションを同時に誤誘導する——しかも読み手は指示に従うだけなので、
    誰も異常だと気づかない（実際 P4 の棚の付け替えで `dev/93-…` が残っていた）。

    見るのは 2 つだけ:

    (1) 名指しした棚のファイルが実在すること。
    (2) 保証されない棚（member の mount は use/ と ref/ だけ）を指すなら、
        「無いかもしれない」と書いてあること。書いていなければ、その一文は
        member のコンテナでは実行不能な指示になる。

    ⚠️ 本文の重複そのものは検査しない——それは意図された重複である（同ファイルに
    理由を書いた）。検査するのは**指し先が生きているか**だけ。
    """
    notes = os.path.join(ROOT, "workspace", "workspace-notes.md")
    if not os.path.exists(notes):
        return
    guaranteed = ("use", "ref")  # docsRolePrefixes の default（member）
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
            shelf, name = m.group(1), m.group(2)
            if not os.path.exists(os.path.join(DOCS, shelf, name)):
                f.error(
                    f"workspace-notes.md:{lineno}: 無い棚のファイルを指している"
                    f" -> {shelf}/{name}"
                    "（全エージェントが読む指示なので、腐ると全員が誤誘導される）"
                )
                continue
            if shelf not in guaranteed and "may be absent" not in block.lower():
                f.error(
                    f"workspace-notes.md:{lineno}: {shelf}/ は member の mount に無い。"
                    "同じ段落に「may be absent」と断るか、use/ か ref/ を指すこと"
                )


def check_ref_parity(f: Findings) -> None:
    """ref/ の表は、英語版と日本語版で ✓ の立ち方が一致していること。

    対訳の存在は lang 検査が見るが、それだけでは**中身がずれた訳**を止められない。
    能力表で片方だけ古いのは、表が 2 つあるのと同じ害になる。
    """
    refdir = os.path.join(DOCS, "ref")
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
    agents = os.path.join(DOCS, "ref", "agents.md")
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
        path = os.path.join(DOCS, "ref", name)
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

    targets = os.path.join(DOCS, "ref", "deploy-targets.md")
    if os.path.exists(targets):
        rows = {c.strip("`*") for c in table_first_column(targets)}
        for group in source_runtime_groups():
            if not (group & rows):
                f.error(
                    "ref/deploy-targets.md: コードにある形態が表に無い -> "
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
            "（links,lang,header,vocab,frozen,ref,settings,features,knowledge,notes）"
        ),
    )
    args = ap.parse_args()

    want = set(filter(None, args.only.split(",")))
    run = lambda name: not want or name in want  # noqa: E731

    files = all_docs()
    f = Findings()
    if run("links"):
        check_links(files, f)
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
