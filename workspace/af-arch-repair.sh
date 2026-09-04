#!/usr/bin/env bash
# アーキ変更後に「利用者自身が ~/.local に入れたもの」を同じ版で入れ直す
# （docs/decisions/0068 決定 4 の続き・entrypoint の arch 自己修復から呼ばれる）。
#
# ## なぜこれが要るのか
#
# entrypoint の arch ブロックは**製品が入れたもの**だけを消して boot-install に入れ直させる。
# 残るのが利用者自身の導入物で、これも同じだけ壊れているのに、これまでは「壊れています」と
# 言うだけだった。しかもその通知はコンテナの stdout に出るので**利用者は読めない**。
# 結果として、症状（ModuleNotFoundError / Exec format error）だけが原因不明で現れていた。
#
# ## なぜ「入れ直す」が安全なのか —— python の major 変更とは事情が違う
#
# アーキ変更は**同じパッケージの同じ版の、別の wheel / 別のバイナリ**に置き換わるだけで、
# 解決結果は元と一致する。だから版を固定した再導入は元の状態を寸分違わず再現する。
# python の major が動いたときは違う（その版が新しい python 用に存在するとは限らず、
# 解決が黙ってずれる）。**両方が同時に動いたときはここでは何もしない**——呼び出し側が
# AF_REPAIR_PY=0 を渡す。
#
# ## 触らないもの
#
# - `~/repos` 配下（node_modules / target / .venv）。本数も所要時間も読めず、lockfile ごとに
#   コマンドが違い、何より**未コミットの作業がある場所**である。一覧にするだけ。
# - `~/.local/bin` に利用者が直接置いたバイナリ。出所が分からないので直しようがない。
#   壊れているものを名指しするところまで。
#
# 使い方: af-arch-repair <前のアーキ> <いまのアーキ>   （amd64 / arm64）
set -uo pipefail

WAS="${1:-}"; NOW="${2:-}"
[ -n "$NOW" ] || { echo "usage: af-arch-repair <was> <now>" >&2; exit 2; }
REPAIR_PY="${AF_REPAIR_PY:-1}"
STATE="$HOME/.local/share/agent-fleet"
NPM_MANIFEST="$STATE/arch-repair-npm"
say() { echo "[arch-repair] $*"; }
fail=0

# ---------------------------------------------------------------- pip --user
# 壊れているのは**拡張モジュールを持つ dist だけ**で、純 python のものはアーキが変わっても
# 無傷である（実測: この環境では 35 dist 中 8 個）。拡張のファイル名にはアーキが入っている
# （`_greenlet.cpython-311-x86_64-linux-gnu.so`）ので、RECORD を読めば壊れている dist を
# 厳密に列挙できる——推測ではなく、そのファイルがどのアーキ用かを見て判定している。
pip_broken() {
  python3 - "$NOW" <<'PY'
import os, sys, glob
mach = {"amd64": "x86_64", "arm64": "aarch64"}.get(sys.argv[1], "")
if not mach:
    raise SystemExit(0)
others = [m for m in ("x86_64", "aarch64") if m != mach]
out = []
for sp in glob.glob(os.path.join(os.path.expanduser("~"), ".local/lib/python*/site-packages")):
    for di in glob.glob(os.path.join(sp, "*.dist-info")):
        try:
            body = open(os.path.join(di, "RECORD"), encoding="utf-8", errors="replace").read()
        except OSError:
            continue
        if not any(("-" + m + "-linux") in body for m in others):
            continue
        # 表示名と版は METADATA が正（dist-info のディレクトリ名は `-`→`_` に正規化されて
        # いて、そのまま出すと利用者が PyPI で探せない名前になる）。
        name = ver = ""
        try:
            for line in open(os.path.join(di, "METADATA"), encoding="utf-8", errors="replace"):
                if line.startswith("Name: ") and not name: name = line[6:].strip()
                elif line.startswith("Version: ") and not ver: ver = line[9:].strip()
                elif not line.strip(): break
        except OSError:
            continue
        if name and ver:
            out.append(name + "==" + ver)
if out:
    print("\n".join(sorted(set(out))))
PY
}

if [ "$REPAIR_PY" = 1 ]; then
  mapfile -t PKGS < <(pip_broken)
  if [ "${#PKGS[@]}" -gt 0 ]; then
    say "pip: ${#PKGS[@]} 件を同じ版で入れ直します: ${PKGS[*]}"
    # ⚠️ --no-deps は必須。壊れているのは拡張を持つ dist だけで、その依存（純 python）は
    #    正常に残っている。付けないと依存ツリー全体が再解決され、**アーキ変更のはずが
    #    バージョン更新になる**。
    if pip install --user --force-reinstall --no-deps "${PKGS[@]}"; then
      say "pip: 完了"
    else
      say "WARN: pip の入れ直しに失敗しました（ネットワーク？）。次の起動でもう一度試します"
      say "WARN:   手で直す: pip install --user --force-reinstall --no-deps ${PKGS[*]}"
      fail=1
    fi
  fi
fi

# ---------------------------------------------------------------- uv tools
# uv の tool は 1 本ずつが独立した venv なので、壊れ方は pip と同じ。⚠️ `uv tool upgrade` は
# 使わない——あれは版を上げる操作で、ここでやりたいのは「同じ版で入れ直す」である。
if command -v uv >/dev/null 2>&1 && [ -d "$HOME/.local/share/uv/tools" ]; then
  for d in "$HOME"/.local/share/uv/tools/*; do
    [ -d "$d" ] || continue
    tool="$(basename "$d")"
    # その venv に他アーキの拡張が居るときだけ入れ直す（純 python の tool は無傷）。
    if ! find "$d" -name "*-linux-gnu.so" 2>/dev/null \
        | grep -qv -- "-$([ "$NOW" = arm64 ] && echo aarch64 || echo x86_64)-linux-gnu.so"; then
      continue
    fi
    ver="$(python3 - "$d" "$tool" <<'PY'
import os, sys, glob
d, tool = sys.argv[1], sys.argv[2].replace("-", "_").lower()
for di in glob.glob(os.path.join(d, "lib/python*/site-packages/*.dist-info")):
    base = os.path.basename(di)[: -len(".dist-info")]
    name, _, ver = base.rpartition("-")
    if name.replace("-", "_").lower() == tool:
        print(ver)
        break
PY
)"
    req="$tool${ver:+==$ver}"
    say "uv tool: $req を入れ直します"
    if ! uv tool install --force --reinstall "$req"; then
      say "WARN: uv tool $req の入れ直しに失敗しました。手で直す: uv tool install --force --reinstall $req"
      fail=1
    fi
  done
fi

# ---------------------------------------------------------------- npm -g
# 🔴 ここが一番黙って壊れていた場所。製品の CLI と利用者自身のものが同じ
# `~/.local/lib/node_modules` に同居していて、arch ブロックはディレクトリごと消す。
# native addon が壊れている以上それは正しいが、boot-install が入れ直すのは**製品の 4 本
# だけ**なので、利用者が `npm i -g` で入れたものは黙って消えていた——entrypoint 自身が
# 掲げている「自分で入れたツールは消さない」に反する唯一の場所だった。
# 消す前に entrypoint が控えた一覧をここで戻す。
if [ -s "$NPM_MANIFEST" ]; then
  mapfile -t GLOBALS < <(grep -v '^[[:space:]]*$' "$NPM_MANIFEST")
  if [ "${#GLOBALS[@]}" -gt 0 ]; then
    say "npm -g: ${#GLOBALS[@]} 件を同じ版で入れ直します: ${GLOBALS[*]}"
    if npm install -g --prefix "$HOME/.local" "${GLOBALS[@]}"; then
      say "npm -g: 完了"
      rm -f "$NPM_MANIFEST"
    else
      say "WARN: npm -g の入れ直しに失敗しました。次の起動でもう一度試します"
      say "WARN:   手で直す: npm install -g --prefix ~/.local ${GLOBALS[*]}"
      fail=1
    fi
  else
    rm -f "$NPM_MANIFEST"
  fi
fi

# ------------------------------------------------- 直せないものは名指しで伝える
# ⚠️ ここは「直す」のではなく「どれが壊れているか」を出す。~/repos は未コミットの作業が
# ある場所なので触らない。~/.local/bin の自前バイナリは出所が分からないので戻せない。
orphans=""
for f in "$HOME"/.local/bin/*; do
  [ -f "$f" ] && [ -x "$f" ] || continue
  head -c 4 "$f" 2>/dev/null | grep -q $'\x7fELF' || continue   # ELF だけ見る
  m="$(readelf -h "$f" 2>/dev/null | awk -F: '/Machine/{print $2}' | xargs)"
  case "$NOW:$m" in
    amd64:*X86-64* | arm64:*AArch64*) ;;
    *:"") ;;
    *) orphans="$orphans $(basename "$f")" ;;
  esac
done
if [ -n "$orphans" ]; then
  say "⚠️ 自分で ~/.local/bin に置いたバイナリが $WAS 用のままです（戻し方が分からないので触っていません）:"
  say "⚠️  $(echo $orphans)"
fi

# ⚠️ 「存在するか」ではなく「**中の成果物がどのアーキ用か**」を見る。存在で判定すると
# (a) 入れ直した後も出続け（＝直しても消えない通知になる）、(b) native addon を持たない
# 純 JS の node_modules を誤って壊れている扱いにする。pip と同じく、アーキはファイルに
# 書いてあるのだから読めばよい。**1 リポジトリにつき 1 ファイルで打ち切る**（-quit）ので、
# 巨大な node_modules でも走査は一瞬で終わる。
stale="$(AF_NOW="$NOW" python3 - "$HOME/repos" <<'PY'
import os, subprocess, sys
root = sys.argv[1]
want = {"amd64": "x86-64", "arm64": "aarch64"}.get(os.environ.get("AF_NOW", ""), "")
if not want or not os.path.isdir(root):
    raise SystemExit(0)

def foreign(path, patterns):
    """そのディレクトリに他アーキのネイティブ成果物があるか（最初の 1 件で打ち切る）。"""
    for pat in patterns:
        try:
            out = subprocess.run(["find", path, "-name", pat, "-type", "f", "-print", "-quit"],
                                 capture_output=True, text=True, timeout=20).stdout.strip()
        except Exception:
            return False
        if not out:
            continue
        try:
            hdr = subprocess.run(["readelf", "-h", out], capture_output=True, text=True,
                                 timeout=10).stdout
        except Exception:
            return False
        for line in hdr.splitlines():
            if "Machine:" in line:
                return want not in line.lower()
    return False

hits = []
for repo in sorted(os.listdir(root)):
    for sub, pats in (("node_modules", ["*.node"]), ("target", ["*.so"]), (".venv", ["*.so"])):
        d = os.path.join(root, repo, sub)
        if os.path.isdir(d) and foreign(d, pats):
            hits.append(repo + "/" + sub)
if hits:
    print(" ".join(hits))
PY
)"
if [ -n "$stale" ]; then
  say "⚠️ ~/repos のビルド生成物が $WAS 用のままです（~/repos は触っていません）:"
  say "⚠️  $stale"
  say "⚠️  直す: node_modules → npm ci ／ target → cargo clean && cargo build ／ .venv → 作り直し"
fi

# 検出結果を「いま壊れている集合」として書き出す。通知はこの内容のハッシュを event_id に
# するので、集合が変わらない限り増えず、直せば集合から消えて自然に鳴り止む
# （ADR 0068 決定 4 の続き・出来事は 1 回・残骸は状態）。
{ [ -n "$stale" ] && printf 'repos: %s\n' "$stale"
  [ -n "$orphans" ] && printf 'bin:%s\n' "$orphans"
  true
} > "$STATE/arch-residue" 2>/dev/null || true
[ -s "$STATE/arch-residue" ] || rm -f "$STATE/arch-residue"

[ "$fail" = 0 ] && say "完了" || say "一部やり直しが残っています（次の起動で再試行します）"
exit 0
