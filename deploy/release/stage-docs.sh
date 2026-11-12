#!/usr/bin/env bash
# Stage the docs tree that gets shipped: the user guide, plus the runbooks copied in
# where a workspace can read them.
#
#   deploy/release/stage-docs.sh <dest-dir>
#
# Two consumers, one implementation: control-plane/Dockerfile runs it in its `docs`
# stage and bakes the result into the CP image, and deploy/release/build.sh drops it
# into the native tar.
# They used to carry a copy of the allowlist loop each, which is exactly how the two
# drift apart.
#
# What is staged:
#
#   1. guide/ — the whole tree, and only that tree (ADR 0064). The developer
#      documentation lives in docs/ and is never shipped, so "what does a customer
#      receive" is answered by a directory name rather than by an allowlist file that
#      somebody has to remember to update. That allowlist (docs/.distinclude) is gone.
#   2. deploy/*/README.md -> <dest>/operate/runbooks/<name>.md — the actual command
#      procedures. They stay where they are in the repository, next to the scripts and
#      templates they operate, and they are part of the release bundle itself. What
#      they were NOT was reachable from inside a workspace, which is the one place an
#      operator reads them under pressure. So: copied, not moved.
#   3. The links are rewritten in the staged copy, both ways. A guide page's link to a
#      runbook (`../../deploy/compose/README.md`, at any depth) becomes the path to
#      `operate/runbooks/compose.md`; this is the reason check_closure grants those READMEs
#      their documented exemptions. A runbook is an index of guide pages, and its links
#      (`../../guide/operate/compose/install.md`) become paths into the staged guide; a link
#      to another runbook becomes that runbook's copy, and a link to anything else under
#      deploy/ (a script, a template), which is not shipped, becomes its GitHub URL. A
#      relative link in a staged runbook that still lands nowhere fails the build.
#   4. deploy/release/notes -> <dest>/ref/releases/ — the release history: SUMMARY*.md
#      (the one-line index) and the notes of every version in index.tsv. They stay in
#      deploy/release/notes because that is also where the dist repo's release bodies
#      and CHANGELOG are rendered from; copying them here is what lets the Console's user
#      guide and the builtin assistants answer "what changed, and since when" from
#      inside a workspace. Only ledger versions are copied: notes of versions that were
#      prepared and never published are still in the directory, and the presence of a
#      file is not what says a version shipped.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

DEST="${1:?usage: stage-docs.sh <dest-dir>}"
mkdir -p "$DEST"

# --- 1. the guide -------------------------------------------------------------
[ -d "$ROOT/guide" ] || {
  echo "ERROR: guide/ is missing — there is nothing to ship" >&2
  exit 1
}
tar -C "$ROOT/guide" -cf - . | tar -C "$DEST" -xf -

# --- 2. the runbooks ----------------------------------------------------------
# The map is explicit rather than a glob: which files are runbooks is an editorial
# decision, and a stray README appearing under deploy/ should not silently become
# documentation that ships to customers. control-plane/Dockerfile's `docs` stage copies
# each of these by name, so a new entry has to be added there too.
RUNBOOKS=(
  "compose:deploy/compose/README.md"
  "native:deploy/native/README.md"
  "wsl:deploy/local/README-wsl.md"
  "aws-ecs:deploy/aws/ecs/README.md"
  "aws-ec2-single:deploy/aws/ec2-single/README.md"
  "kubernetes:deploy/kubernetes/README.md"
)
REPO_URL="https://github.com/k-k1/agent-fleet"
RB="$DEST/operate/runbooks"
mkdir -p "$RB"

# Escape a string for use as a literal in a sed pattern / replacement delimited by '|'.
sed_lit() { printf '%s' "$1" | sed -e 's/[]\\|$*.^[&]/\\&/g'; }

# Rewrite the relative links of one staged runbook copy (see 3. above).
rewrite_runbook_copy() {  # <staged file> <source path relative to ROOT>
  local f="$1" srcdir t path frag resolved new e name src
  srcdir="$(dirname "$2")"
  while IFS= read -r t; do
    case "$t" in http://*|https://*|mailto:*|\#*|/*) continue ;; esac
    path="${t%%#*}"; frag=""
    [ "$path" = "$t" ] || frag="#${t#*#}"
    resolved="$(realpath -m --relative-to="$ROOT" "$ROOT/$srcdir/$path")"
    new=""
    case "$resolved" in
      guide/*) new="$(realpath -m --relative-to="$RB" "$DEST/${resolved#guide/}")" ;;
      *)
        for e in "${RUNBOOKS[@]}"; do
          name="${e%%:*}"; src="${e##*:}"
          [ "$resolved" = "$src" ] && new="$name.md"
        done
        [ -n "$new" ] || new="$REPO_URL/blob/main/$resolved" ;;
    esac
    sed -i "s|]($(sed_lit "$t"))|]($(sed_lit "$new$frag"))|g" "$f"
  done < <(grep -oE '\]\([^) ]+\)' "$f" | sed -E 's/^\]\((.*)\)$/\1/' | sort -u)
}

for entry in "${RUNBOOKS[@]}"; do
  name="${entry%%:*}"; src="${entry##*:}"
  [ -f "$ROOT/$src" ] || {
    echo "ERROR: runbook '$src' is missing (stage-docs.sh needs updating)" >&2
    exit 1
  }
  cp "$ROOT/$src" "$RB/$name.md"
  rewrite_runbook_copy "$RB/$name.md" "$src"
done

cat > "$DEST/operate/runbooks/README.md" <<'EOF'
# Runbooks

The index of each deployment target's procedure pages, copied here so it is readable from
inside a workspace.

| File | Target | Source in the repository |
|---|---|---|
| [compose.md](compose.md) | compose, the on-prem default | `deploy/compose/README.md` |
| [native.md](native.md) | containerless (no Docker) | `deploy/native/README.md` |
| [wsl.md](wsl.md) | a personal WSL2 machine | `deploy/local/README-wsl.md` |
| [aws-ecs.md](aws-ecs.md) | ecs / ecs-ec2 | `deploy/aws/ecs/README.md` |
| [aws-ec2-single.md](aws-ec2-single.md) | compose on a single EC2 VM | `deploy/aws/ec2-single/README.md` |
| [kubernetes.md](kubernetes.md) | kubernetes (GKE first) | `deploy/kubernetes/README.md` |

These are **copies**, made when the release is built. The procedures themselves are the
pages of [operate/](../README.md).
EOF

# --- 3. point the guide at the staged runbooks --------------------------------
# In the repository the links read `../../deploy/<x>/README.md` (one more `../` per
# directory level), which is correct on GitHub. In a container the same files are at
# operate/runbooks/, so rewrite them here, at every depth of the guide.
rewrite_runbook_links() {  # <staged guide file>
  local f="$1" prefix e name src args=()
  prefix="$(realpath -m --relative-to="$(dirname "$f")" "$RB")"
  for e in "${RUNBOOKS[@]}"; do
    name="${e%%:*}"; src="${e##*:}"
    args+=(-e "s#\(\.\./\)\+$(sed_lit "$src")#${prefix}/${name}.md#g")
  done
  sed -i "${args[@]}" "$f"
}
while IFS= read -r -d "" f; do
  rewrite_runbook_links "$f"
done < <(find "$DEST" -path "$RB" -prune -o -name "*.md" -print0)

# Every relative link in a staged runbook must land inside the staged tree.
dead=0
for f in "$RB"/*.md; do
  while IFS= read -r target; do
    case "$target" in http://*|https://*|mailto:*|\#*) continue ;; esac
    [ -e "$RB/${target%%#*}" ] || {
      echo "ERROR: ${f#"$DEST"/} links to '$target', which is not in the staged docs" >&2
      dead=1
    }
  done < <(grep -oE '\]\([^) ]+\)' "$f" | sed -E 's/^\]\((.*)\)$/\1/')
done
[ "$dead" = 0 ] || exit 1

# The same for the release history (step 4): the repository links reach it through
# deploy/release/notes, the staged tree has it at ref/releases. Every depth, like above.
while IFS= read -r -d "" f; do
  prefix="$(realpath -m --relative-to="$(dirname "$f")" "$DEST/ref/releases")"
  sed -i -e "s#\(\.\./\)\+deploy/release/notes/#${prefix}/#g" "$f"
done < <(find "$DEST" -path "$RB" -prune -o -name "*.md" -print0)

# --- 4. the release history ---------------------------------------------------
NOTES="$ROOT/deploy/release/notes"
REL="$DEST/ref/releases"
mkdir -p "$REL"
cp "$NOTES/SUMMARY.md" "$NOTES/SUMMARY.ja.md" "$REL/"
n=0
while IFS=$'\t' read -r v _date _commit; do
  case "$v" in ''|\#*) continue ;; esac
  [ -f "$NOTES/$v.md" ] || {
    echo "ERROR: index.tsv lists $v but $NOTES/$v.md is missing" >&2
    exit 1
  }
  cp "$NOTES/$v.md" "$REL/"
  [ -f "$NOTES/$v.ja.md" ] && cp "$NOTES/$v.ja.md" "$REL/"
  n=$((n + 1))
done < "$NOTES/index.tsv"
# Every relative link in the copy must land inside it. A note that links to README.md
# or index.tsv (maintainer files that are not copied) would be dead for the reader,
# and nothing else checks the staged tree.
dead=0
for f in "$REL"/*.md; do
  while IFS= read -r target; do
    case "$target" in http://*|https://*|mailto:*|\#*) continue ;; esac
    [ -e "$REL/${target%%#*}" ] || {
      echo "ERROR: ${f#"$DEST"/} links to '$target', which is not in the staged release history" >&2
      dead=1
    }
  done < <(grep -oE '\]\([^) ]+\)' "$f" | sed -E 's/^\]\((.*)\)$/\1/')
done
[ "$dead" = 0 ] || exit 1

echo "==> staged docs: guide/ + ${#RUNBOOKS[@]} runbooks + $n release notes -> $DEST"
