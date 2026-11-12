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
#         af-arch-repair --due   (exit 0 when an automatic retry is waiting, 1 otherwise)
#
# ## Retries
#
# A reinstall that fails leaves its list behind (arch-repair-npm / -pip / -uv, one item per
# line), and every later start re-runs this script while a list is non-empty — the arch stamp
# is already current by then, so without these lists nothing would ever run again. The
# entrypoint asks `--due` and passes AF_ARCH_REPAIR_AUTO=change|rerun|retry; a run by hand
# passes none and leaves the counter alone.
#
# The retries are capped at MAX_RETRIES starts after the one that changed architecture: a
# package that cannot be installed (gone from the index, no wheel for this arch, no network
# at all) would otherwise add its timeouts to every start forever. A retry is counted when
# it STARTS, so a run killed every time (an OOM kill, a stop during the install) uses up the
# cap like a failing one. Once the cap is reached, one more start runs without installing
# anything: it writes the leftovers into arch-residue as a `pkgs:` line, so the member's
# notification names them (the only place they can read it), and only then records that it
# gave up. The lists stay; a run by hand still uses them.
set -uo pipefail

STATE="$HOME/.local/share/agent-fleet"
NPM_MANIFEST="$STATE/arch-repair-npm"
PIP_MANIFEST="$STATE/arch-repair-pip"
UV_MANIFEST="$STATE/arch-repair-uv"
# "<retries started> <architecture before the change> <gave up: 0|1>", one atomic file.
TRIES_FILE="$STATE/arch-repair-tries"
MAX_RETRIES=3

pending() { [ -s "$NPM_MANIFEST" ] || [ -s "$PIP_MANIFEST" ] || [ -s "$UV_MANIFEST" ]; }
T_STARTED=0; T_WAS=""; T_GAVE_UP=0
read_tries() {
  local n="" w="" g=""
  # 2>/dev/null goes first: it has to be in place before the redirection that can fail.
  read -r n w g 2>/dev/null < "$TRIES_FILE" || true
  case "$n" in '' | *[!0-9]*) T_STARTED=0 ;; *) T_STARTED="$n" ;; esac
  T_WAS="$w"
  [ "$g" = 1 ] && T_GAVE_UP=1 || T_GAVE_UP=0
}
write_tries() {
  { printf '%s %s %s\n' "$1" "$2" "$3" > "$TRIES_FILE.tmp.$$" && mv -f "$TRIES_FILE.tmp.$$" "$TRIES_FILE"; } 2>/dev/null \
    || rm -f "$TRIES_FILE.tmp.$$"
}

# Due while a list is pending and the give-up has not been recorded: below the cap that is a
# retry, at the cap it is the run that writes the notification.
if [ "${1:-}" = --due ]; then
  read_tries
  pending && [ "$T_GAVE_UP" = 0 ]
  exit
fi

WAS="${1:-}"; NOW="${2:-}"
[ -n "$NOW" ] || { echo "usage: af-arch-repair <was> <now>" >&2; exit 2; }
REPAIR_PY="${AF_REPAIR_PY:-1}"
MODE="${AF_ARCH_REPAIR_AUTO:-}"
say() { echo "[arch-repair] $*"; }
mkdir -p "$STATE" 2>/dev/null || true

# The start that changed architecture resets the counter. A retry, and a rerun of a change
# start that was killed before its pass finished (rerun: arch-repair-change still there),
# count themselves before doing anything, so the cap holds even when no run ever finishes.
# Past the cap a run installs nothing (INSTALL=0) and only reports. A retry start knows no
# "before" architecture, so it reads the one the change start recorded (messages only).
INSTALL=1
read_tries
case "$MODE" in
  change) T_STARTED=0; T_GAVE_UP=0; write_tries 0 "$WAS" 0 ;;
  retry | rerun)
    case "$WAS" in '' | '?') WAS="${T_WAS:-?}" ;; esac
    if [ "$T_STARTED" -lt "$MAX_RETRIES" ]; then
      T_STARTED=$((T_STARTED + 1))
      write_tries "$T_STARTED" "$WAS" 0
    else
      INSTALL=0
    fi
    ;;
esac
# A retry start only retries what failed before; the change start, its rerun and a run by
# hand look at everything.
want() { [ "$INSTALL" = 1 ] && { [ "$MODE" != retry ] || [ -s "$1" ]; }; }
# The lists are written before any install and an item leaves only after its own install
# succeeded, always through a temp file and mv: a run killed at any point leaves a list that
# still names everything not yet done, never a truncated one.
list_write() {
  local f="$1"; shift
  if [ "$#" -gt 0 ]; then
    if ! { printf '%s\n' "$@" > "$f.tmp.$$" && mv -f "$f.tmp.$$" "$f"; } 2>/dev/null; then
      rm -f "$f.tmp.$$"
    fi
  else
    rm -f "$f"
  fi
}
list_drop() {
  local f="$1" item="$2" rest=()
  [ -e "$f" ] || return 0
  mapfile -t rest < <(grep -vxF -- "$item" "$f")
  list_write "$f" ${rest[@]+"${rest[@]}"}
}

# ---------------------------------------------------------------- pip --user
# 壊れているのは**拡張モジュールを持つ dist だけ**で、純 python のものはアーキが変わっても
# 無傷である（実測: この環境では 35 dist 中 8 個）。拡張のファイル名にはアーキが入っている
# （`_greenlet.cpython-311-x86_64-linux-gnu.so`）ので、RECORD を読めば壊れている dist を
# 厳密に列挙できる——推測ではなく、そのファイルがどのアーキ用かを見て判定している。
#
# Every entry of a list left by an earlier failure is attempted again, whatever is on disk:
# a failed or killed install can leave a package that looks installed (METADATA without
# RECORD, a half-written tree), so only an install that exited 0 takes an entry off the list.
# A forced reinstall of the same version is idempotent.
pip_broken() {
  python3 - "$NOW" "$PIP_MANIFEST" <<'PY'
import os, sys, glob
mach = {"amd64": "x86_64", "arm64": "aarch64"}.get(sys.argv[1], "")
if not mach:
    raise SystemExit(0)
others = [m for m in ("x86_64", "aarch64") if m != mach]
out = []
for sp in glob.glob(os.path.join(os.path.expanduser("~"), ".local/lib/python*/site-packages")):
    for di in glob.glob(os.path.join(sp, "*.dist-info")):
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
        try:
            body = open(os.path.join(di, "RECORD"), encoding="utf-8", errors="replace").read()
        except OSError:
            continue
        if not any(("-" + m + "-linux") in body for m in others):
            continue
        if name and ver:
            out.append(name + "==" + ver)
try:
    for line in open(sys.argv[2], encoding="utf-8"):
        req = line.strip()
        if req:
            out.append(req)
except OSError:
    pass
if out:
    print("\n".join(sorted(set(out))))
PY
}

if [ "$INSTALL" != 1 ]; then
  :
elif [ "$REPAIR_PY" != 1 ]; then
  # The python major moved: the recorded versions may not exist for the new python, so an
  # earlier failure is not retried either.
  [ -s "$PIP_MANIFEST" ] && say "pip: Python のメジャー版が変わったので、前回失敗した入れ直しはやめます: $(tr '\n' ' ' < "$PIP_MANIFEST")"
  rm -f "$PIP_MANIFEST"
elif want "$PIP_MANIFEST"; then
  mapfile -t PKGS < <(pip_broken)
  list_write "$PIP_MANIFEST" ${PKGS[@]+"${PKGS[@]}"}
  if [ "${#PKGS[@]}" -gt 0 ]; then
    say "pip: ${#PKGS[@]} 件を同じ版で入れ直します: ${PKGS[*]}"
    # ⚠️ --no-deps は必須。壊れているのは拡張を持つ dist だけで、その依存（純 python）は
    #    正常に残っている。付けないと依存ツリー全体が再解決され、**アーキ変更のはずが
    #    バージョン更新になる**。
    # One package per install, so one that cannot be installed does not keep the others in
    # the list: the list has to name exactly what still fails.
    PIP_FAILED=()
    for pkg in "${PKGS[@]}"; do
      if pip install --user --force-reinstall --no-deps "$pkg"; then
        list_drop "$PIP_MANIFEST" "$pkg"
      else
        PIP_FAILED+=("$pkg")
      fi
    done
    if [ "${#PIP_FAILED[@]}" -eq 0 ]; then
      say "pip: 完了"
    else
      say "WARN: pip の入れ直しに失敗しました（ネットワーク？）: ${PIP_FAILED[*]}"
      say "WARN:   手で直す: pip install --user --force-reinstall --no-deps ${PIP_FAILED[*]}"
    fi
  fi
fi

# ---------------------------------------------------------------- uv tools
# uv の tool は 1 本ずつが独立した venv なので、壊れ方は pip と同じ。⚠️ `uv tool upgrade` は
# 使わない——あれは版を上げる操作で、ここでやりたいのは「同じ版で入れ直す」である。
# Every listed tool is attempted again, whether or not its environment is there or looks
# fixed: a failed install can leave a directory that passes the check below.
UV_TOOLS="$HOME/.local/share/uv/tools"
uv_foreign() {
  find "$1" -name "*-linux-gnu.so" 2>/dev/null \
    | grep -qv -- "-$([ "$NOW" = arm64 ] && echo aarch64 || echo x86_64)-linux-gnu.so"
}
if want "$UV_MANIFEST"; then
  UV_TODO=()
  for d in "$UV_TOOLS"/*; do
    [ -d "$d" ] || continue
    tool="$(basename "$d")"
    # その venv に他アーキの拡張が居るときだけ入れ直す（純 python の tool は無傷）。
    uv_foreign "$d" || continue
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
    UV_TODO+=("$tool${ver:+==$ver}")
  done
  if [ -s "$UV_MANIFEST" ]; then
    while IFS= read -r req; do
      [ -n "$req" ] && UV_TODO+=("$req")
    done < "$UV_MANIFEST"
  fi
  mapfile -t UV_TODO < <(printf '%s\n' ${UV_TODO[@]+"${UV_TODO[@]}"} | grep -v '^$' | sort -u)
  list_write "$UV_MANIFEST" ${UV_TODO[@]+"${UV_TODO[@]}"}
  for req in ${UV_TODO[@]+"${UV_TODO[@]}"}; do
    say "uv tool: $req を入れ直します"
    if command -v uv >/dev/null 2>&1 && uv tool install --force --reinstall "$req"; then
      list_drop "$UV_MANIFEST" "$req"
    else
      say "WARN: uv tool $req の入れ直しに失敗しました。手で直す: uv tool install --force --reinstall $req"
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
# Every entry is installed again until its own install exits 0; what is on disk proves
# nothing, since an install killed half-way can leave a package.json behind.
if [ "$INSTALL" = 1 ] && [ -s "$NPM_MANIFEST" ]; then
  mapfile -t GLOBALS < <(grep -v '^[[:space:]]*$' "$NPM_MANIFEST")
  list_write "$NPM_MANIFEST" ${GLOBALS[@]+"${GLOBALS[@]}"}
  if [ "${#GLOBALS[@]}" -gt 0 ]; then
    say "npm -g: ${#GLOBALS[@]} 件を同じ版で入れ直します: ${GLOBALS[*]}"
    NPM_FAILED=()
    for spec in "${GLOBALS[@]}"; do
      if npm install -g --prefix "$HOME/.local" "$spec"; then
        list_drop "$NPM_MANIFEST" "$spec"
      else
        NPM_FAILED+=("$spec")
      fi
    done
    if [ "${#NPM_FAILED[@]}" -eq 0 ]; then
      say "npm -g: 完了"
    else
      say "WARN: npm -g の入れ直しに失敗しました: ${NPM_FAILED[*]}"
      say "WARN:   手で直す: npm install -g --prefix ~/.local ${NPM_FAILED[*]}"
    fi
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

# What is still to be reinstalled, and whether a later start will try again. Read from the
# lists rather than tracked on the way, so the summary and the notification cannot disagree
# with what the next start actually does.
left=()
for kind in pip uv npm; do
  case "$kind" in pip) f="$PIP_MANIFEST" ;; uv) f="$UV_MANIFEST" ;; npm) f="$NPM_MANIFEST" ;; esac
  [ -s "$f" ] || continue
  while IFS= read -r l; do [ -n "${l// /}" ] && left+=("$kind:$l"); done < "$f"
done
# A change start (or its rerun) that got here no longer has to be redone; past the cap a
# rerun gives up on that too.
case "$MODE" in change | rerun) rm -f "$STATE/arch-repair-change" ;; esac
retries_left=$(( MAX_RETRIES - T_STARTED ))
# Only leftovers no start will retry go into the notification: a transient failure that the
# next start fixes should not reach the member at all.
gave_up=""
[ "${#left[@]}" -gt 0 ] && [ "$retries_left" -le 0 ] && gave_up="${left[*]}"

# 検出結果を「いま壊れている集合」として書き出す。通知はこの内容のハッシュを event_id に
# するので、集合が変わらない限り増えず、直せば集合から消えて自然に鳴り止む
# （ADR 0068 決定 4 の続き・出来事は 1 回・残骸は状態）。
# Written whole through a temp file, and before the give-up is recorded below: a run killed
# in between is due again and writes it again, so the notification is never skipped.
RESIDUE="$STATE/arch-residue"
{ [ -n "$stale" ] && printf 'repos: %s\n' "$stale"
  [ -n "$orphans" ] && printf 'bin:%s\n' "$orphans"
  [ -n "$gave_up" ] && printf 'pkgs: %s\n' "$gave_up"
  true
} > "$RESIDUE.tmp.$$" 2>/dev/null || true
if [ -s "$RESIDUE.tmp.$$" ]; then mv -f "$RESIDUE.tmp.$$" "$RESIDUE" 2>/dev/null || rm -f "$RESIDUE.tmp.$$"
else rm -f "$RESIDUE.tmp.$$" "$RESIDUE"; fi

# The counter is written last.
if [ "${#left[@]}" -eq 0 ]; then
  rm -f "$TRIES_FILE"
elif [ -n "$gave_up" ]; then
  write_tries "$T_STARTED" "$WAS" 1
fi

if [ "${#left[@]}" -eq 0 ]; then
  say "完了"
elif [ "$retries_left" -gt 0 ]; then
  say "入れ直せなかったものが残っています: ${left[*]}（次の起動で自動でもう一度試します。自動で試すのはあと $retries_left 回です）"
else
  say "入れ直せなかったものが残っています: ${left[*]}（自動ではもう試しません。上の WARN: の行のコマンドか、af-arch-repair ${WAS:-<was>} $NOW を実行してください）"
fi
exit 0
