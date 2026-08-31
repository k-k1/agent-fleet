#!/usr/bin/env bash
# Stage the docs tree that gets shipped: the user guide, plus the runbooks copied in
# where a workspace can read them.
#
#   deploy/release/stage-docs.sh <dest-dir>
#
# Two consumers, one implementation: deploy/compose/release.sh bakes the result into
# the CP image (DOCS_SRC), and deploy/release/build.sh drops it into the native tar.
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
#      check_closure grants deploy/ its single documented exemption.
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
# documentation that ships to customers.
RUNBOOKS=(
  "compose:deploy/compose/README.md"
  "native:deploy/native/README.md"
  "wsl:deploy/local/README-wsl.md"
  "aws-ecs:deploy/aws/ecs/README.md"
  "aws-ec2-single:deploy/aws/ec2-single/README.md"
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

These are **copies**. Edit them at the source path above, next to the scripts and
templates they operate — that adjacency is what keeps them true. The copy is made when
the release is built.

The shelf that explains what each step decides, and what to watch for, is
[operate/](../README.md).
EOF

# --- 3. point the guide at the staged runbooks --------------------------------
# In the repository the links read `../../deploy/<x>/README.md`, which is correct on
# GitHub. In a container the same files are at operate/runbooks/, so rewrite them here.
rewrite_runbook_links() {  # <dir> <prefix>
  local dir="$1" prefix="$2" f
  [ -d "$dir" ] || return 0
  while IFS= read -r -d "" f; do
    sed -i \
      -e "s#\.\./\.\./deploy/compose/README\.md#${prefix}compose.md#g" \
      -e "s#\.\./\.\./deploy/native/README\.md#${prefix}native.md#g" \
      -e "s#\.\./\.\./deploy/local/README-wsl\.md#${prefix}wsl.md#g" \
      -e "s#\.\./\.\./deploy/aws/ecs/README\.md#${prefix}aws-ecs.md#g" \
      -e "s#\.\./\.\./deploy/aws/ec2-single/README\.md#${prefix}aws-ec2-single.md#g" \
      "$f"
  done < <(find "$dir" -maxdepth 1 -name "*.md" -print0)
}
rewrite_runbook_links "$DEST/operate" "runbooks/"
rewrite_runbook_links "$DEST/ref" "../operate/runbooks/"

echo "==> staged docs: guide/ + ${#RUNBOOKS[@]} runbooks -> $DEST"
