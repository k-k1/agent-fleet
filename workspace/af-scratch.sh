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

# scratch_path_ok / scratch_mkdir create a directory under $SCRATCH/artifacts/ and refuse (non-zero,
# nothing created) when a component is `.` / `..`, or an existing one is a symlink or not
# a directory. `mkdir -p` alone would follow a symlinked parent (artifacts/x -> /elsewhere)
# and create directories outside the scratch disk; a lexical prefix check does not catch
# that. The existing prefix is checked with builtins only and the missing tail made by
# `mkdir -p`: a component that does not exist yet cannot be a symlink. Keeping the check
# fork-free lets --repair batch readlink and mkdir for all links (a fork per component
# or per link measured ~11-13 ms a link on a loaded host).
scratch_path_ok() {
  local rest cur comp parts
  case "$1" in "$SCRATCH"/artifacts/*) ;; *) return 1 ;; esac
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

scratch_mkdir() {
  scratch_path_ok "$1" || return 1
  [ -d "$1" ] || mkdir -p "$1" 2>/dev/null || return 1
  [ -d "$1" ] && [ ! -L "$1" ]
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
# barrier that has the links resolve before the first build. Its cost is bounded by
# AF_SCRATCH_REPAIR_MAX (default 500) revivals, a readlink and a few mkdirs each
# (measured before dedup went linear: 400 links 12 s on a loaded host); past the cap it
# warns and leaves the rest for a manual `af-scratch --repair`.
if [ "$MODE" = "--repair" ]; then
  [ -f "$REGISTRY" ] || exit 0
  max="${AF_SCRATCH_REPAIR_MAX:-500}"
  case "$max" in ''|*[!0-9]*) max=500 ;; esac
  exec 9>>"$LOCK" || exit 0
  flock -w 30 9 || { echo "af-scratch: --repair: the registry is locked, skipping" >&2; exit 0; }
  kept="$REGISTRY.tmp.$$"
  awk '!seen[$0]++' "$REGISTRY" > "$kept" 2>/dev/null || { rm -f "$kept"; exit 0; }
  out="$kept.out"
  : > "$out"
  dangling=()
  skipped=0
  while IFS= read -r link || [ -n "$link" ]; do
    [ -n "$link" ] && [ -L "$link" ] || continue
    printf '%s\n' "$link" >> "$out"
    [ -e "$link" ] && continue
    if [ "${#dangling[@]}" -ge "$max" ]; then skipped=$((skipped + 1)); continue; fi
    dangling+=("$link")
  done < "$kept"
  if [ "${#dangling[@]}" -gt 0 ]; then
    # One readlink and one mkdir for all of them, NUL-delimited so no target can shift
    # the pairing. A link that vanished in between makes readlink fail for the batch;
    # the next start retries.
    targets=()
    mapfile -d '' -t targets < <(readlink -z -- "${dangling[@]}" 2>/dev/null || true)
    if [ "${#targets[@]}" = "${#dangling[@]}" ]; then
      todo=()
      for i in "${!dangling[@]}"; do
        t="${targets[$i]}"
        case "$t" in "$SCRATCH"/artifacts/*) ;; *) continue ;; esac
        if scratch_path_ok "$t"; then
          todo+=("$t")
        else
          echo "af-scratch: not recreating $t for ${dangling[$i]}: a path component is a symlink, \`..\` or not a directory" >&2
        fi
      done
      if [ "${#todo[@]}" -gt 0 ]; then
        mkdir -p -- "${todo[@]}" 2>/dev/null || true
        made=0
        for t in "${todo[@]}"; do [ -d "$t" ] && made=$((made + 1)); done
        echo "af-scratch: recreated $made of ${#todo[@]} link target(s) under $SCRATCH/artifacts (the scratch disk was emptied by a stop)"
      fi
    fi
  fi
  mv -f "$out" "$REGISTRY" 2>/dev/null || rm -f "$out"
  rm -f "$kept"
  if [ "$skipped" -gt 0 ]; then
    echo "af-scratch: --repair stopped at AF_SCRATCH_REPAIR_MAX=$max; $skipped dangling link(s) left, run \`af-scratch --repair\` to finish" >&2
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
    echo "af-scratch: $dest の途中にシンボリックリンクなどがあるため移しません。" >&2
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
