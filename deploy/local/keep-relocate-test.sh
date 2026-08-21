#!/usr/bin/env bash
# keep-relocate-test.sh — entrypoint.sh の「identity を AF_WS_KEEP へ逃がして symlink で戻す」
# ブロック（ADR 0045 決定 3-6）の回帰テスト。実イメージも AWS も要らない: home と keep を
# 一時ディレクトリで作り、ブロックだけを切り出して走らせる。
#
# 守りたい不変条件は 1 つだけ:
#
#   ★ ブロックを抜けた後、`mkdir -p "$HOME/.config/<何か>"` が必ず成功すること。
#
# これが破れたのが <prod-deployment> の golden 初号機だった: golden から作った home は種が張った
# symlink（~/.config -> $AF_WS_KEEP/.config）を丸ごと持ってくる一方、keep 側の EFS は新規
# ユーザーごとに空である。当時のブロックは「もう正しい symlink だ」と判断して early-continue し、
# **向き先を作る mkdir を飛ばしていた**。~/.config は宙に浮いたままになり、後段の
# `mkdir -p "$HOME/.config/opencode"` が `File exists` で落ち、`set -e` で entrypoint ごと死ぬ。
# 症状はタスクの無限再起動だけで、原因はどのログにも出ない。
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
ENTRY="$ROOT/workspace/entrypoint.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ブロックを切り出す。entrypoint.sh を作り替えて切り出しが空振りしたら、静かに
# 「全ケース成功」になってしまうので、中身を検分してから使う。
BLOCK="$WORK/keep-block.sh"
awk '/^if \[ -n "\$\{AF_WS_KEEP:-\}" \]/,/^fi$/' "$ENTRY" > "$BLOCK"
for needle in 'ln -sfn' 'AF_WS_KEEP' 'mkdir -p'; do
  grep -q -- "$needle" "$BLOCK" || {
    echo "FAIL: could not slice the keep block out of entrypoint.sh (missing $needle)" >&2
    echo "      entrypoint.sh was restructured — fix the awk range in this test." >&2
    exit 1
  }
done

fail() { echo "FAIL: $1" >&2; exit 1; }

# run_case <name> — home/keep を用意し終えた状態でブロックを実行し、不変条件を確かめる。
run_case() {
  local name="$1" home="$2" keep="$3"
  if ! HOME="$home" AF_WS_KEEP="$keep" bash -c '
        set -e
        source "$1"
        mkdir -p "$HOME/.config/opencode"
      ' _ "$BLOCK" 2>"$WORK/err.txt"; then
    echo "--- stderr ---" >&2; cat "$WORK/err.txt" >&2
    fail "$name: the block left ~/.config unusable"
  fi
  # ディレクトリ側は keep に実体があること。ファイル側（.gitconfig 等）は実体が無くてよい
  # ——「後から普通に書けば EFS 側にできる」ための dangling symlink がそもそもの設計。
  for rel in .config .ssh .claude .codex; do
    [ -d "$keep/$rel" ] || fail "$name: $keep/$rel was not created"
    [ -L "$home/$rel" ] || fail "$name: ~/$rel is not a symlink"
    [ "$(readlink "$home/$rel")" = "$keep/$rel" ] || fail "$name: ~/$rel points somewhere else"
  done
  echo "ok: $name"
}

# 1) golden から作った home（★ 回帰の本体）: symlink は正しいが keep 側は空。
G_HOME="$WORK/g/home"; G_KEEP="$WORK/g/keep"; mkdir -p "$G_HOME" "$G_KEEP"
for rel in .config .ssh .claude .codex .git-credentials .gitconfig .claude.json; do
  ln -s "$G_KEEP/$rel" "$G_HOME/$rel"
done
run_case "golden-seeded home, empty keep" "$G_HOME" "$G_KEEP"

# 2) まっさらな home: 実体が home 側にあり、keep へ移されて symlink に置き換わる。
F_HOME="$WORK/f/home"; F_KEEP="$WORK/f/keep"; mkdir -p "$F_HOME" "$F_KEEP"
mkdir -p "$F_HOME/.config/agent-fleet" "$F_HOME/.ssh"
echo "seeded" > "$F_HOME/.config/agent-fleet/marker"
run_case "fresh home, empty keep" "$F_HOME" "$F_KEEP"
[ -f "$F_KEEP/.config/agent-fleet/marker" ] || fail "fresh home: the real ~/.config was not relocated"

# 3) 2 回目（=通常の再起動）。何も壊さず、依然として使えること。
run_case "second boot (idempotent)" "$F_HOME" "$F_KEEP"
[ -f "$F_KEEP/.config/agent-fleet/marker" ] || fail "second boot: the relocated ~/.config was lost"

# 4) AF_WS_KEEP を注入しないランタイム（docker / native / Fargate）では丸ごと no-op。
N_HOME="$WORK/n/home"; mkdir -p "$N_HOME/.config"
echo "untouched" > "$N_HOME/.config/marker"
HOME="$N_HOME" bash -c 'set -e; unset AF_WS_KEEP; source "$1"; mkdir -p "$HOME/.config/opencode"' _ "$BLOCK"
[ -f "$N_HOME/.config/marker" ] || fail "no AF_WS_KEEP: ~/.config was touched anyway"
# `[ … ] && fail` は書かない: 判定が偽のときリストが 1 を返し、set -e の下では
# 「テストが通ったので落ちる」という一番たちの悪い形になる。
if [ -L "$N_HOME/.config" ]; then fail "no AF_WS_KEEP: ~/.config became a symlink"; fi
echo "ok: no AF_WS_KEEP (docker / native / Fargate) — no-op"

echo "PASS: keep relocation holds ~/.config usable on every path"
