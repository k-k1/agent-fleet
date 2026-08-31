#!/usr/bin/env bash
# Stage the docs tree that gets shipped: the shelves on the allowlist, plus the
# runbooks copied in where a workspace can read them.
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
#   1. docs/<shelf> for each entry in docs/.distinclude — the reader-cut shelves. The
#      decision records and the frozen work journals are not on that list and so are
#      never shipped.
#   2. deploy/*/README.md -> <dest>/operate/runbooks/<name>.md — the actual command
#      procedures. They stay where they are in the repository, next to the scripts and
#      templates they operate, and they are part of the release bundle itself. What
#      they were NOT was reachable from inside a workspace, which is the one place an
#      operator reads them under pressure. So: copied, not moved.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

DEST="${1:?usage: stage-docs.sh <dest-dir>}"
mkdir -p "$DEST"

# --- 1. the shelves -----------------------------------------------------------
SHELVES=()
while IFS= read -r line || [ -n "$line" ]; do
  line="${line%%#*}"
  line="${line#"${line%%[![:space:]]*}"}"; line="${line%"${line##*[![:space:]]}"}"
  [ -n "$line" ] || continue
  # A listed shelf that does not exist is a mistake in .distinclude, not something to
  # ship silently short — tar would fail anyway; say why first.
  [ -d "$ROOT/docs/$line" ] || {
    echo "ERROR: docs/.distinclude lists '$line', which is not a directory" >&2
    exit 1
  }
  SHELVES+=("$line")
done < "$ROOT/docs/.distinclude"
tar -C "$ROOT/docs" -cf - "${SHELVES[@]}" | tar -C "$DEST" -xf -

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

echo "==> staged docs: shelves=${SHELVES[*]} + ${#RUNBOOKS[@]} runbooks -> $DEST"
