#!/usr/bin/env bash
# The entrypoint's oc_drop_v1 removes the opencode 1 package (opencode-ai) from ~/.local for good,
# so @opencode/cli can be linked there and nothing stale shadows the image's opencode on PATH. It
# also settles what an older image's interrupted package swap left in <prefix>/.af-oc-swap. Runs
# the real function against a stub `npm` that refuses, like the real one, to link a package's
# `opencode` bin while anything is at that path, a dangling link included (EEXIST).
#
#   deploy/local/opencode-drop-v1-test.sh
set -euo pipefail
# The failure cases make directories unwritable, which root ignores.
[ "$(id -u)" != 0 ] || { echo "SKIP: run as a regular user (root bypasses the permission bits)"; exit 0; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'chmod -R u+w "$WORK" 2>/dev/null; rm -rf "$WORK"' EXIT
fail() { echo "NG: $1"; exit 1; }

mkdir -p "$WORK/bin"
cat > "$WORK/bin/npm" <<'FAKE'
#!/usr/bin/env bash
# npm install -g --prefix <p> <spec>...
[ "$1" = install ] || exit 0
pfx="$4"; shift 4
for spec in "$@"; do
  pkg="${spec%@*}"
  case "$pkg" in opencode-ai|@opencode/cli)
    [ ! -e "$pfx/bin/opencode" ] && [ ! -L "$pfx/bin/opencode" ] || { echo "npm error EEXIST" >&2; exit 1; }
    mkdir -p "$pfx/lib/node_modules/$pkg/bin"
    echo "$pkg" > "$pfx/lib/node_modules/$pkg/marker"
    ln -s "../lib/node_modules/$pkg/bin/opencode.exe" "$pfx/bin/opencode" ;;
  esac
done
FAKE
chmod +x "$WORK/bin/npm"
export PATH="$WORK/bin:$PATH"

# The function and its call line, cut out of the entrypoint (a file that no longer has them fails
# the extraction).
sed -n '/^oc_drop_v1() {/,/^}$/p' "$ROOT/workspace/entrypoint.sh" > "$WORK/fn.sh"
[ -s "$WORK/fn.sh" ] || fail "workspace/entrypoint.sh: oc_drop_v1() was not found"
call="$(grep '^oc_drop_v1 "$HOME/.local" || ' "$ROOT/workspace/entrypoint.sh")" \
  || fail "workspace/entrypoint.sh: the unconditional, non-fatal oc_drop_v1 call was not found"

P="$WORK/p"
home_with() { # home_with <pkg>: a prefix whose opencode is <pkg>
  rm -rf "$P"; mkdir -p "$P/bin"
  npm install -g --prefix "$P" "$1@1.0.0"
}
drop() { (. "$WORK/fn.sh"; oc_drop_v1 "$P"); }
owner() { cat "$P/lib/node_modules/$(readlink "$P/bin/opencode" | sed 's|.*node_modules/\(.*\)/bin/.*|\1|')/marker"; }
aside() { # aside <pkg>: move <pkg> and its bin into .af-oc-swap, as the old swap did
  mkdir -p "$P/.af-oc-swap/pkg/$(dirname "$1")" "$P/.af-oc-swap/bin"
  mv "$P/lib/node_modules/$1" "$P/.af-oc-swap/pkg/$1"
  mv "$P/bin/opencode" "$P/.af-oc-swap/bin/opencode"
}

# Control: with opencode-ai linked the stub refuses @opencode/cli, so a pass below is the
# function's doing and not the stub's.
home_with opencode-ai
if npm install -g --prefix "$P" @opencode/cli@latest 2>/dev/null; then fail "control: the stub did not refuse the install over opencode-ai"; fi

home_with opencode-ai
drop || fail "oc_drop_v1 failed on a home with opencode-ai"
[ ! -e "$P/lib/node_modules/opencode-ai" ] || fail "opencode-ai is still installed"
[ ! -e "$P/bin/opencode" ] && [ ! -L "$P/bin/opencode" ] || fail "the opencode-ai bin is still linked"
npm install -g --prefix "$P" @opencode/cli@latest || fail "@opencode/cli could not be installed after the drop"
[ "$(owner)" = "@opencode/cli" ] || fail "after the drop and install the bin belongs to '$(owner)'"
echo "ok  opencode-ai removed, @opencode/cli installs"

home_with @opencode/cli
drop || fail "oc_drop_v1 failed on an opencode 2 home"
[ "$(owner)" = "@opencode/cli" ] || fail "an opencode 2 home was changed: bin belongs to '$(owner)'"
echo "ok  an opencode 2 home is left alone"

# A user's own `opencode` that is not opencode-ai's link stays.
home_with opencode-ai
rm "$P/bin/opencode"; printf '#!/bin/sh\n' > "$P/bin/opencode"
drop || fail "oc_drop_v1 failed"
[ -f "$P/bin/opencode" ] && [ ! -L "$P/bin/opencode" ] || fail "a file that is not opencode-ai's link was removed"
echo "ok  only opencode-ai's own links are removed"

# An interrupted swap that moved @opencode/cli aside (the only opencode 2 there): put back.
home_with @opencode/cli
aside @opencode/cli
drop || fail "oc_drop_v1 failed on an aside @opencode/cli"
[ "$(owner)" = "@opencode/cli" ] || fail "the aside @opencode/cli was not restored"
[ ! -e "$P/.af-oc-swap" ] || fail "the backup directory was left behind"
echo "ok  an aside @opencode/cli is restored"

# An interrupted swap that moved opencode-ai aside: discarded, the installed opencode 2 untouched.
home_with opencode-ai
aside opencode-ai
npm install -g --prefix "$P" @opencode/cli@latest
drop || fail "oc_drop_v1 failed on an aside opencode-ai"
[ ! -e "$P/.af-oc-swap" ] || fail "the aside opencode-ai was kept"
[ "$(owner)" = "@opencode/cli" ] || fail "the installed @opencode/cli was changed: '$(owner)'"
echo "ok  an aside opencode-ai is discarded"

# An aside @opencode/cli while the prefix already has one: the prefix's copy is kept as it is.
home_with @opencode/cli
aside @opencode/cli
npm install -g --prefix "$P" @opencode/cli@latest
echo installed > "$P/lib/node_modules/@opencode/cli/marker2"
drop || fail "oc_drop_v1 failed with an aside copy and an installed one"
[ -f "$P/lib/node_modules/@opencode/cli/marker2" ] || fail "the installed @opencode/cli was replaced"
[ ! -e "$P/lib/node_modules/@opencode/cli/cli" ] || fail "the aside copy was moved into the installed one"
[ ! -e "$P/.af-oc-swap" ] || fail "the backup directory was left behind"
echo "ok  an installed @opencode/cli wins over an aside one"

# Half-restored: the package is back, its bin is still aside.
home_with @opencode/cli
mkdir -p "$P/.af-oc-swap/bin"
mv "$P/bin/opencode" "$P/.af-oc-swap/bin/opencode"
drop || fail "oc_drop_v1 failed on a half-restored swap"
[ "$(owner)" = "@opencode/cli" ] || fail "the aside bin of an installed @opencode/cli was not put back"
echo "ok  a half-restored swap gets its bin back"

# opencode-ai's package is gone but its link is left (an old swap stopped between its two moves):
# the dangling link must go, or npm refuses @opencode/cli on every start.
home_with opencode-ai
rm -rf "$P/lib/node_modules/opencode-ai"
if npm install -g --prefix "$P" @opencode/cli@latest 2>/dev/null; then fail "control: the stub did not refuse over a dangling link"; fi
drop || fail "oc_drop_v1 failed on a dangling opencode-ai link"
[ ! -L "$P/bin/opencode" ] || fail "the dangling opencode-ai link was left"
npm install -g --prefix "$P" @opencode/cli@latest || fail "@opencode/cli could not be installed after a dangling link"
echo "ok  a dangling opencode-ai link is removed"

# Restoring fails: the aside copy must survive for the next start. Once at the mkdir, once at the
# move itself (a file where the package goes: only the move fails, deleting the backup would not).
home_with @opencode/cli
aside @opencode/cli
rm -rf "$P/lib/node_modules/@opencode"
chmod a-w "$P/lib/node_modules"
if drop 2>/dev/null; then fail "oc_drop_v1 reported success although it could not create the target"; fi
chmod u+w "$P/lib/node_modules"
[ -d "$P/.af-oc-swap/pkg/@opencode/cli" ] || fail "the aside @opencode/cli was deleted although the target could not be created"
home_with @opencode/cli
aside @opencode/cli
rm -rf "$P/lib/node_modules/@opencode"
mkdir -p "$P/lib/node_modules/@opencode"; : > "$P/lib/node_modules/@opencode/cli"
if drop 2>/dev/null; then fail "oc_drop_v1 reported success although the move failed"; fi
[ -d "$P/.af-oc-swap/pkg/@opencode/cli" ] || fail "the aside @opencode/cli was deleted although it could not be moved back"
echo "ok  a failed restore keeps the aside copy"

# The entrypoint runs under `set -e`; a failure must cost a warning, not the start.
home_with opencode-ai
chmod a-w "$P/lib/node_modules"
out="$(HOME="$WORK/h" bash -ec ". '$WORK/fn.sh'; mkdir -p \"\$HOME\"; ln -s '$P' \"\$HOME/.local\"; $call; echo survived" 2>/dev/null)" \
  || { chmod u+w "$P/lib/node_modules"; fail "a failing oc_drop_v1 aborted a set -e script"; }
chmod u+w "$P/lib/node_modules"
grep -q '^\[entrypoint\] WARN: could not remove opencode 1' <<< "$out" || fail "no warning for a failed drop: $out"
grep -q '^survived$' <<< "$out" || fail "the script did not continue past the failed drop"
echo "ok  a failure is a warning under set -e"

echo "OK: oc_drop_v1 removes opencode 1 and settles an old swap"
