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
#   3. The links to those runbooks are rewritten in the staged copy. In the repository
#      `guide/operate/…` points at `../../deploy/compose/README.md`, which is right on
#      GitHub and absent in a container; here it becomes `runbooks/compose.md`, which is
#      right in a container. One source, correct in both places — and the reason
#      check_closure grants deploy/ its documented exemptions.
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
mkdir -p "$DEST/operate/runbooks"
for entry in "${RUNBOOKS[@]}"; do
  name="${entry%%:*}"; src="${entry##*:}"
  [ -f "$ROOT/$src" ] || {
    echo "ERROR: runbook '$src' is missing (stage-docs.sh needs updating)" >&2
    exit 1
  }
  cp "$ROOT/$src" "$DEST/operate/runbooks/$name.md"
done

cat > "$DEST/operate/runbooks/README.md" <<'EOF'
# Runbooks

The command procedures, copied here so they are readable from inside a workspace.

| File | Target | Source in the repository |
|---|---|---|
| [compose.md](compose.md) | compose, the on-prem default | `deploy/compose/README.md` |
| [native.md](native.md) | containerless (no Docker) | `deploy/native/README.md` |
| [wsl.md](wsl.md) | a personal WSL2 machine | `deploy/local/README-wsl.md` |
| [aws-ecs.md](aws-ecs.md) | ecs / ecs-ec2 | `deploy/aws/ecs/README.md` |
| [aws-ec2-single.md](aws-ec2-single.md) | compose on a single EC2 VM | `deploy/aws/ec2-single/README.md` |
| [kubernetes.md](kubernetes.md) | kubernetes (GKE first) | `deploy/kubernetes/README.md` |

These are **copies**. Edit them at the source path above, next to the scripts and
templates they operate — that adjacency is what keeps them true. The copy is made when
the release is built.

The shelf that explains what each step decides, and what to watch for, is
[operate/](../README.md).
EOF

# --- 3. point the guide at the staged runbooks and release history ------------
# In the repository the links read `../../deploy/<x>/README.md` and
# `../../deploy/release/notes/…`, which are correct on GitHub. In a container the same
# files are at operate/runbooks/ and ref/releases/, so rewrite them here.
#
# Every page is rewritten by its own depth, never by a list of directories: a fixed list
# is how the topic directories (operate/keys/, user/start/, …) kept their dead links in
# every container while the repository and GitHub looked fine. Only links that climb
# exactly to the repository root are touched, anchored on "](", so a deeper link is never
# half-rewritten. operate/runbooks/ holds verbatim copies whose links are relative to
# deploy/ and is left alone.
while IFS= read -r -d "" f; do
  rel="${f#"$DEST"/}"
  slashes="${rel//[^\/]/}"
  depth=${#slashes}
  up=""   # from this page to the staged root
  for ((i = 0; i < depth; i++)); do up="../$up"; done
  repo="${up//./\\.}\\.\\./"   # one more level reaches the repository root, regex-escaped
  sed -i \
    -e "s#](${repo}deploy/compose/README\\.md#](${up}operate/runbooks/compose.md#g" \
    -e "s#](${repo}deploy/native/README\\.md#](${up}operate/runbooks/native.md#g" \
    -e "s#](${repo}deploy/local/README-wsl\\.md#](${up}operate/runbooks/wsl.md#g" \
    -e "s#](${repo}deploy/aws/ecs/README\\.md#](${up}operate/runbooks/aws-ecs.md#g" \
    -e "s#](${repo}deploy/aws/ec2-single/README\\.md#](${up}operate/runbooks/aws-ec2-single.md#g" \
    -e "s#](${repo}deploy/kubernetes/README\\.md#](${up}operate/runbooks/kubernetes.md#g" \
    -e "s#](${repo}deploy/release/notes/#](${up}ref/releases/#g" \
    "$f"
done < <(find "$DEST" -path "$DEST/operate/runbooks" -prune -o -name "*.md" -print0)

# A relative link that still leaves the staged tree for deploy/ is dead in every
# container. Fail here rather than ship it: a runbook added to the guide without a row
# above lands in this check, not in a reader's "file not found".
if leftovers="$(grep -rnE '\]\((\.\./)+deploy/' --include='*.md' --exclude-dir=runbooks "$DEST")"; then
  echo "ERROR: staged guide pages still link into deploy/ (add the target to stage-docs.sh step 3):" >&2
  echo "$leftovers" >&2
  exit 1
fi

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
