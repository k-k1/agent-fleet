#!/usr/bin/env bash
# af-scratch — move build output onto the task-local fast disk (ADR 0044 decision 3).
#
#   af-scratch target             # move ./target to /scratch and leave a symlink
#   af-scratch target build       # several at once
#   af-scratch --status           # what is relocated now
#   af-scratch --auto <dir>       # relocate ahead of time for the projects under dir (the Agent runs this on a new clone)
#   af-scratch --repair           # recreate the scratch side of links left dangling by a stop (the entrypoint runs this)
#   af-scratch --seed             # once: add links made before the registry existed (the entrypoint runs this)
#
# Why (measured, docs/log/63 §63.4): on ECS `~` is EFS (NFS), with a fixed ~14.5ms per
# file. A tree of tens of thousands of files is 9x+ slower there, and neither more
# parallelism nor more vCPUs helps.
#
# The price: **the contents are gone when the Workspace stops**, so only regenerable
# output belongs here — never tracked files or uncommitted work.
#
# node_modules cannot be relocated this way: npm (arborist `_createSparseTree`) replaces
# any symlink on the path from a package to the project root with a real directory, so
# the first `npm ci` / `npm install` puts the tree back on EFS — and `npm ci` empties the
# scratch side first. Measured with npm 10.9.9, even on a no-op `npm install`.
set -euo pipefail

MODE="${1:-}"
SCRATCH="${AF_WS_SCRATCH:-}"
if [ -z "$SCRATCH" ]; then
  # --auto は Agent が clone / worktree 作成のたびに best-effort で叩く。作業ディスクが
  # 無い構成（docker / native、または退避が無効なデプロイ）では黙って何もしないこと——
  # ここで失敗を返すと、逃がす動機が無いだけの正常な環境でログが埋まる。
  case "$MODE" in --auto|--repair|--seed) exit 0 ;; esac
  echo "af-scratch: この Workspace には作業ディスクがありません（AF_WS_SCRATCH 未設定）。" >&2
  echo "  ローカルディスクにホームが載っている構成では、逃がす意味がないので何もしません。" >&2
  exit 1
fi

# 逃がし先はワークツリー毎に分ける。同名の node_modules が複数プロジェクトにあっても
# 衝突しないよう、絶対パスをそのまま階層に写す。
dest_for() { printf '%s/artifacts%s\n' "$SCRATCH" "$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"; }

# --- Links that outlive the disk they point at ---------------------------------------
#
# The scratch disk is new and empty after every stop, but the links live in ~/repos and
# survive, so each one dangles until its target is recreated. Build tools do not do it:
# Gradle 8.14 fails with "Failed to create parent directory", and Java's File.mkdirs() /
# Files.createDirectories(), which Maven uses, refuse a dangling symlink (measured, #3987);
# Rust's create_dir_all() (cargo) does the same by reading of its code.
#
# The registry lists every link this script made, one absolute path per line, so the
# entrypoint can revive them without walking ~/repos. It lives in home (persistent), not
# on the scratch disk it describes. A path containing a newline is never recorded: one
# line per entry is what lets --repair dedupe with awk instead of a grep per entry.
# Every write holds $STATE_DIR/lock (flock), so a --seed in the background, an --auto
# from the Agent and a --repair rewrite cannot lose or duplicate each other's entries.
STATE_DIR="${AF_SCRATCH_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/af-scratch}"
REGISTRY="$STATE_DIR/links"
LOCK="$STATE_DIR/lock"
NL='
'

record_link() {
  case "$1" in
    *"$NL"*) echo "af-scratch: not recording a link whose path contains a newline; after a stop, run af-scratch on it by hand" >&2
             return 1 ;;
  esac
  mkdir -p "$STATE_DIR" 2>/dev/null || return 1
  (
    flock -w 30 9 || exit 1
    grep -qxF -- "$1" "$REGISTRY" 2>/dev/null && exit 0
    printf '%s\n' "$1" >> "$REGISTRY"
  ) 9>>"$LOCK" 2>/dev/null
}

# scratch_path_ok accepts a path under $SCRATCH/artifacts/ only when no component is
# empty, `.`, `..` or a newline, and no existing component is a symlink or a
# non-directory. `mkdir -p` alone would follow a symlinked parent (artifacts/x ->
# /elsewhere) and create directories outside the scratch disk; a lexical prefix check
# does not catch that. It uses builtins only, so --repair can check every target without
# a fork and create them all with one `mkdir -p` (a fork per component or per link
# measured ~11-13 ms a link on a loaded host). A component that does not exist yet
# cannot be a symlink.
scratch_path_ok() {
  local rest cur comp parts
  # A newline would end the `read` below early and hide the components after it (a
  # later `..` included), so such a target is refused before it is split.
  case "$1" in *"$NL"*) return 1 ;; "$SCRATCH"/artifacts/*) ;; *) return 1 ;; esac
  rest="${1#"$SCRATCH"/artifacts/}"
  IFS=/ read -r -a parts <<< "$rest"
  for comp in "${parts[@]}"; do
    case "$comp" in ''|.|..) return 1 ;; esac
  done
  cur="$SCRATCH/artifacts"
  if [ -L "$cur" ]; then return 1; fi
  [ -e "$cur" ] || return 0
  [ -d "$cur" ] || return 1
  for comp in "${parts[@]}"; do
    cur="$cur/$comp"
    if [ -L "$cur" ]; then return 1; fi
    if [ -d "$cur" ]; then continue; fi
    [ -e "$cur" ] && return 1
    return 0
  done
}

# The check and the mkdir are not atomic: a component swapped for a symlink in between
# is followed. That gains nothing, since everything here runs as the workspace user and
# whoever can swap a component can already create directories wherever that user can
# write; the guard is against stale or foreign links, not an adversary. scratch_mkdir
# narrows the window by creating one component at a time and re-checking each one;
# --repair's batched `mkdir -p` keeps the wider window to stay one fork.
scratch_mkdir() {
  local rest cur comp parts
  scratch_path_ok "$1" || return 1
  rest="${1#"$SCRATCH"/artifacts/}"
  IFS=/ read -r -a parts <<< "$rest"
  cur="$SCRATCH/artifacts"
  for comp in "" "${parts[@]}"; do
    [ -n "$comp" ] && cur="$cur/$comp"
    if [ -L "$cur" ]; then return 1; fi
    [ -d "$cur" ] && continue
    mkdir "$cur" 2>/dev/null || true
    if [ -L "$cur" ] || [ ! -d "$cur" ]; then return 1; fi
  done
}

# revive_link succeeds only for a link into $SCRATCH/artifacts/, the only place this
# script ever links to, and recreates its target when it dangles. A link pointing
# anywhere else is the user's (a share into the parent clone, say) and is left alone.
revive_link() {
  local link="$1" t
  [ -L "$link" ] || return 1
  t="$(readlink "$link")" || return 1
  case "$t" in "$SCRATCH"/artifacts/*) ;; *) return 1 ;; esac
  [ -e "$link" ] && return 0
  if ! scratch_mkdir "$t"; then
    echo "af-scratch: not recreating $t for $link: a path component is a symlink, \`..\` or not a directory" >&2
    return 1
  fi
  echo "af-scratch: recreated $t for $link (the scratch disk was emptied by a stop)"
}

# --repair is what the entrypoint runs at every start, in the foreground: it is the
# barrier that has the links resolve before the first build. Its cost is bounded twice:
# it looks at no more than AF_SCRATCH_REPAIR_SCAN (default 5000) registry lines, an lstat
# each (the rest are kept, with a warning), and it recreates no more than
# AF_SCRATCH_REPAIR_MAX (default 500) targets. Only a dangling link whose target passed
# scratch_path_ok counts toward that cap, so foreign or refused links cannot starve
# valid ones. Entries whose link is gone or points outside $SCRATCH/artifacts/ are
# dropped, so the registry shrinks back as worktrees go.
if [ "$MODE" = "--repair" ]; then
  [ -f "$REGISTRY" ] || exit 0
  num() { case "$1" in ''|*[!0-9]*) echo "$2" ;; *) echo "$1" ;; esac; }
  max="$(num "${AF_SCRATCH_REPAIR_MAX:-}" 500)"
  scan="$(num "${AF_SCRATCH_REPAIR_SCAN:-}" 5000)"
  exec 9>>"$LOCK" || exit 0
  flock -w 30 9 || { echo "af-scratch: --repair: the registry is locked, skipping" >&2; exit 0; }
  kept="$REGISTRY.tmp.$$"
  out="$kept.out"
  trap 'rm -f "$kept" "$out"' EXIT
  awk '!seen[$0]++' "$REGISTRY" > "$kept" 2>/dev/null || exit 0
  : > "$out"
  dangling=()
  unscanned=0
  n=0
  while IFS= read -r link || [ -n "$link" ]; do
    n=$((n + 1))
    if [ "$n" -gt "$scan" ]; then
      printf '%s\n' "$link" >> "$out"
      unscanned=$((unscanned + 1))
      continue
    fi
    case "$link" in /*) ;; *) continue ;; esac
    [ -L "$link" ] || continue
    if [ -e "$link" ]; then
      printf '%s\n' "$link" >> "$out"
    else
      dangling+=("$link")
    fi
  done < "$kept"
  skipped=0
  failed=0
  if [ "${#dangling[@]}" -gt 0 ]; then
    # find prints path and target as NUL-separated pairs: a target with a newline stays
    # one field. xargs -0 feeds the paths in chunks under ARG_MAX (one argv of 650 long
    # paths, ~2.2 MB, failed with E2BIG). Any failure, a link that vanished in between
    # included, leaves the registry as it was: a rewrite from a partial answer would
    # drop entries for good.
    pairs="$kept.pairs"
    trap 'rm -f "$kept" "$out" "$pairs"' EXIT
    printf '%s\0' "${dangling[@]}" |
      xargs -0 sh -c 'exec find "$@" -maxdepth 0 -type l -printf "%p\\0%l\\0"' find > "$pairs" 2>/dev/null || failed=1
    todo=()
    while IFS= read -r -d '' link && IFS= read -r -d '' t; do
      case "$t" in "$SCRATCH"/artifacts/*) ;; *) continue ;; esac  # not ours any more
      printf '%s\n' "$link" >> "$out"
      if ! scratch_path_ok "$t"; then
        printf "af-scratch: not recreating %q for %s: a path component is a symlink, \`..\`, a newline or not a directory\n" "$t" "$link" >&2
        continue
      fi
      if [ "${#todo[@]}" -ge "$max" ]; then skipped=$((skipped + 1)); continue; fi
      todo+=("$t")
    done < "$pairs"
    if [ "${#todo[@]}" -gt 0 ]; then
      printf '%s\0' "${todo[@]}" | xargs -0 mkdir -p -- 2>/dev/null || true
      made=0
      for t in "${todo[@]}"; do [ -d "$t" ] && made=$((made + 1)); done
      echo "af-scratch: recreated $made of ${#todo[@]} link target(s) under $SCRATCH/artifacts (the scratch disk was emptied by a stop)"
    fi
  fi
  if [ "$failed" = 1 ]; then
    echo "af-scratch: --repair could not read every link (find failed); the registry is left as it was, the next start retries" >&2
  else
    mv -f "$out" "$REGISTRY" 2>/dev/null || echo "af-scratch: --repair could not rewrite $REGISTRY; left as it was" >&2
  fi
  if [ "$skipped" -gt 0 ]; then
    echo "af-scratch: --repair stopped at AF_SCRATCH_REPAIR_MAX=$max; $skipped dangling link(s) left, run \`af-scratch --repair\` to finish" >&2
  fi
  if [ "$unscanned" -gt 0 ]; then
    echo "af-scratch: --repair looked at the first AF_SCRATCH_REPAIR_SCAN=$scan registry entries only; $unscanned left unchecked, run \`af-scratch --repair\` with a larger AF_SCRATCH_REPAIR_SCAN" >&2
  fi
  exit 0
fi

# --seed finds the links made before the registry existed, once per home: a scan bounded
# to what --auto creates (target/build at most AF_WS_SCRATCH_AUTO_DEPTH levels below a
# clone in ~/repos). The marker is written only when the walk and every record
# succeeded; otherwise the next start tries again, because a marker over a failed scan
# would leave those links dangling for good.
if [ "$MODE" = "--seed" ]; then
  repos="${AF_SCRATCH_REPOS:-$HOME/repos}"
  [ ! -e "$STATE_DIR/seeded" ] && [ -d "$repos" ] || exit 0
  mkdir -p "$STATE_DIR" 2>/dev/null || exit 0
  found="$STATE_DIR/seed.$$"
  # The entrypoint's timeout sends TERM; without the second trap bash would skip EXIT.
  trap 'rm -f "$found"' EXIT
  trap 'exit 143' TERM INT
  ok=1
  find "$repos" -mindepth 2 -maxdepth "$(( ${AF_WS_SCRATCH_AUTO_DEPTH:-3} + 1 ))" \
    \( -name .git -o -name node_modules -o -name .venv -o -name dist \) -prune -o \
    -type l \( -name target -o -name build \) -print > "$found" 2>/dev/null || ok=0
  while IFS= read -r link || [ -n "$link" ]; do
    [ -L "$link" ] || continue
    case "$(readlink "$link")" in "$SCRATCH"/artifacts/*) ;; *) continue ;; esac
    record_link "$link" || ok=0
    revive_link "$link" || true
  done < "$found"
  rm -f "$found"
  if [ "$ok" = 1 ]; then
    : > "$STATE_DIR/seeded" 2>/dev/null || true
  else
    echo "af-scratch: --seed did not finish (a directory could not be read or a link not recorded); the next start retries" >&2
  fi
  exit 0
fi

if [ "${1:-}" = "--status" ]; then
  base="$SCRATCH/artifacts"
  [ -d "$base" ] || { echo "逃がしているものはありません。"; exit 0; }
  find "$base" -mindepth 1 -maxdepth 6 -type d -exec test -e '{}' ';' -print 2>/dev/null |
    while read -r d; do
      orig="${d#"$base"}"
      if [ -L "$orig" ]; then printf '%-60s %s\n' "$orig" "$(du -sh "$d" 2>/dev/null | cut -f1)"; fi
    done
  exit 0
fi

# --- --auto: look at the working copy and relocate the output it is about to produce ---
#
# Linking while the directory does not exist yet means the first build already writes
# to /scratch; relocating afterwards re-reads the whole tree off EFS to move it.
#
# Only target/ and build/ are pre-created. node_modules is not (see the header), and
# neither is .venv: `python3 -m venv .venv` refuses a symlink ("Unable to create
# directory", Python 3.13), so a pre-created link breaks the stock command.
#
# 安全側の規則:
#   - Already a symlink: leave it (it may be the user's share into the parent clone), except
#     that a dangling link into $SCRATCH/artifacts is revived (see revive_link)
#   - 実体があり、**git が無視していない** → 触らない（追跡物を動かすことは絶対にしない）
#   - 実体があり、git が無視している → 移して symlink に置き換える
#   - 実体が無い → 空の逃がし先を作って symlink を張る（この経路が本命）
#
# Side effect: `[ -d build ] || …` style scripts see the empty link as done.
# AF_WS_SCRATCH_AUTO=0 turns the step off.
auto_relocate() {
  target="$1"
  if [ -L "$target" ]; then
      if revive_link "$target"; then record_link "$target" || true; fi
      return 0
  fi
  if [ -e "$target" ]; then
    if ! git -C "$(dirname "$target")" check-ignore -q "$target" 2>/dev/null; then return 0; fi
  fi
  dest="$(dest_for "$target")"
  scratch_mkdir "$(dirname "$dest")" || return 0
  if [ -L "$dest" ]; then return 0; fi
  if [ -e "$target" ]; then
    rm -rf "$dest"
    mv "$target" "$dest" 2>/dev/null || return 0
  else
    scratch_mkdir "$dest" || return 0
  fi
  ln -s "$dest" "$target" 2>/dev/null || return 0
  record_link "${dest#"$SCRATCH/artifacts"}" || true
  echo "af-scratch: $target -> $dest（Workspace 停止で消えます）"
}

if [ "$MODE" = "--auto" ]; then
  [ "${AF_WS_SCRATCH_AUTO:-1}" = "0" ] && exit 0
  root="${2:-$PWD}"
  [ -d "$root" ] || exit 0
  root="$(cd "$root" && pwd)"
  depth="${AF_WS_SCRATCH_AUTO_DEPTH:-3}"
  # マーカーの在り処＝生成物の在り処。モノレポのために深さを見る（既定 3 階層）。
  # 生成物ディレクトリ自身と .git には降りない（node_modules の中の package.json を拾わない）。
  find "$root" -maxdepth "$depth" \
    \( -name .git -o -name node_modules -o -name .venv -o -name target -o -name build -o -name dist \) -prune -o \
    -type f \( -name Cargo.toml -o -name pom.xml \
               -o -name build.gradle -o -name build.gradle.kts \) -print 2>/dev/null |
    while read -r marker; do
      d="$(dirname "$marker")"
      case "$(basename "$marker")" in
        Cargo.toml|pom.xml)         arts="target" ;;
        build.gradle|build.gradle.kts) arts="build" ;;
        *)                          arts="" ;;
      esac
      for a in $arts; do auto_relocate "$d/$a"; done
    done || true
  exit 0
fi

[ $# -gt 0 ] || { sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 1; }

status=0
for target in "$@"; do
  case "$(basename "$target")" in
    node_modules)
      echo "af-scratch: node_modules は逃がせません。npm がリンクを実体ディレクトリに置き換えて EFS へ戻し、npm ci は逃がし先を空にします。" >&2
      status=1
      continue ;;
    .venv)
      echo "af-scratch: 注意: .venv へのリンクは uv venv / uv sync なら使えますが、python3 -m venv .venv は失敗します。" >&2 ;;
  esac
  if [ -L "$target" ]; then
    if [ ! -e "$target" ] && revive_link "$target"; then
      record_link "$(cd "$(dirname "$target")" && pwd)/$(basename "$target")" || true
      continue
    fi
    echo "af-scratch: $target は既に symlink です（-> $(readlink "$target")）。何もしません。"
    continue
  fi
  dest="$(dest_for "$target")"
  if ! scratch_mkdir "$(dirname "$dest")" || [ -L "$dest" ]; then
    echo "af-scratch: $dest には改行や .. が含まれるか、途中にシンボリックリンクなどがあるため移しません。" >&2
    status=1
    continue
  fi
  if [ -e "$target" ]; then
    # EFS から読んで書き戻すので、ここは 1 回だけ遅い。次回以降は最初から scratch 側。
    echo "af-scratch: $target を作業ディスクへ移しています…"
    rm -rf "$dest"
    mv "$target" "$dest"
  else
    scratch_mkdir "$dest"
  fi
  ln -s "$dest" "$target"
  record_link "${dest#"$SCRATCH/artifacts"}" || true
  echo "af-scratch: $target -> $dest（Workspace 停止で消えます）"
done
exit "$status"
