#!/usr/bin/env bash
# The release watcher's decisions, against stubbed npm / curl / gh.
#
#   deploy/local/cli-drift-stub-test.sh
#
# ## Why this exists
#
# `cli-drift-check.sh` used to exit 2 the moment any single release source could not be
# read, and `cli-release-watch.yml` turned that into a dead job. On 2026-09-09 claude
# 2.1.267 had been detected, rtk's GitHub Releases call failed, and all seven contract
# dispatches plus every state update were dropped — leaving `tested` at 2.1.265, which
# is indistinguishable from "upstream released nothing". It was found the next morning,
# by hand.
#
# What is pinned here is the DECISION on both sides of that: which rows survive one
# unreadable source, which kinds are held back, and what is left behind for the reader
# who later asks why `tested` stopped moving. The workflow itself cannot be executed
# here (no `act`), which is why that branch lives in `cli-release-edges.sh`.
#
# Cases 3, 7 and 10 are the positive controls. Case 3 proves exit 2 is still reachable
# at all, so "case 2 did not exit 2" means something. Case 7 re-runs case 2 against a
# copy of the checker patched back to the old all-or-nothing rule and requires it to
# reproduce the defect. Every other assertion here has the form "X is still there",
# which a check that stopped running would also satisfy.
#
# 🔴 Nothing under test pipes into `head`: under `pipefail` the writer dies of SIGPIPE
# and the run exits 141, which reads as a crash exactly when there was the most to
# report. The cursor fixture below is oversized on purpose for that reason.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
CHECK="$HERE/cli-drift-check.sh"
EDGES="$HERE/cli-release-edges.sh"
STATE="$HERE/cli-release-state.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"
PINS="$WORK/pins"
mkdir -p "$STUB" "$PINS"

# The checker resolves workspace/Dockerfile from its own location, so "in sync" means
# "the stub answered the pin that is really in the tree". Read the same name -> ARG
# mapping out of the checker's own TARGETS table: a row whose ARG was renamed then
# shows up as the ARG-not-found failure it is, instead of as a passing fixture.
while read -r name arg; do
  found="$(sed -n "s/^ARG $arg=//p" "$ROOT/workspace/Dockerfile")"
  printf '%s' "${found%%$'\n'*}" > "$PINS/$name"
done < <(sed -n 's/^  "\([a-z][a-z]*\)|\([A-Z_][A-Z_]*\)|.*/\1 \2/p' "$CHECK")

fail() {
  echo "NG: $1"
  for f in out err gh_out summary.md; do
    [ -s "$WORK/$f" ] || continue
    echo "--- $f ---"; cat "$WORK/$f"
  done
  exit 1
}

# --- the release-source stubs --------------------------------------------------------
#
# STUB_FAIL is a comma-separated list of row names whose source refuses to answer, which
# is what an unreachable registry, a rate-limited API and a 503 all look like from here:
# no output on stdout. Everything else answers the row's real pin, so a row is "in sync"
# unless a case names it in STUB_DRIFT.
cat > "$STUB/npm" <<'FAKE'
#!/usr/bin/env bash
# `npm view <pkg> version`
case "$2" in
  @anthropic-ai/claude-code) name=claude ;;
  opencode-ai)               name=opencode ;;
  @openai/codex)             name=codex ;;
  @github/copilot)           name=copilot ;;
  *) exit 1 ;;
esac
case ",${STUB_FAIL:-}," in *",$name,"*) exit 1 ;; esac
case ",${STUB_DRIFT:-}," in *",$name,"*) echo "9.9.9"; exit 0 ;; esac
cat "$STUB_PINS/$name"; echo
FAKE

# curl answers the agy / cursor / kiro / rtk rows. The cursor body is deliberately far
# bigger than a pipe buffer (64 KiB on Linux): that row used to be
# `curl | sed | head -1`, and a fixture that fits in the buffer never makes the writer
# block, so the SIGPIPE the split exists for cannot happen and the case passes either
# way — the same inert-control mistake dev-deploy-stub-test.sh case 6 documents.
cat > "$STUB/curl" <<'FAKE'
#!/usr/bin/env bash
url="${!#}"
case "$url" in
  *antigravity*) name=agy ;;
  *cursor.com*)  name=cursor ;;
  *kiro.dev*)    name=kiro ;;
  *rtk-ai/rtk*)  name=rtk ;;
  *) exit 22 ;;
esac
case ",${STUB_FAIL:-}," in *",$name,"*) exit 22 ;; esac
version="$(cat "$STUB_PINS/$name")"
# cursor's version is matched as `<digits and dots>-<hex>`, so its drift fixture has to
# keep that shape or the row reads as a fetch failure instead of as drift.
case ",${STUB_DRIFT:-}," in
  *",$name,"*) [ "$name" = cursor ] && version="9999.99.99-deadbee" || version="9.9.9" ;;
esac
case "$name" in
  agy|kiro) printf '{"version":"%s"}\n' "$version" ;;
  rtk)      printf '{"tag_name":"v%s"}\n' "$version" ;;
  cursor)
    # The real install script is a long shell file with the version buried in a URL.
    seq 1 4000 | sed 's/^/# leading padding line /'
    echo "  url=\"https://downloads.cursor.com/versions/${version}/linux\""
    seq 1 4000 | sed 's/^/# trailing padding line /' ;;
esac
FAKE
chmod +x "$STUB"/npm "$STUB"/curl
export PATH="$STUB:$PATH" STUB_PINS="$PINS"

# --- cli-drift-check.sh --------------------------------------------------------------

SCRIPT_UNDER_TEST="$CHECK"
rc=0
# `set +e` around the run and `$?` taken directly, never through a pipe: the exit code
# IS what is under test, and `| tee` would hand back tee's 0 for every case here.
run_check() { # run_check [<row>]
  : > "$WORK/out"; : > "$WORK/err"; : > "$WORK/gh_out"; : > "$WORK/summary.md"
  set +e
  GITHUB_OUTPUT="$WORK/gh_out" GITHUB_STEP_SUMMARY="$WORK/summary.md" \
    "$SCRIPT_UNDER_TEST" "$@" > "$WORK/out" 2> "$WORK/err"
  rc=$?
  set -e
}

out_has()   { grep -qE -- "$1" "$WORK/gh_out" || fail "GITHUB_OUTPUT missing: $1"; }
out_hasnt() { ! grep -qE -- "$1" "$WORK/gh_out" || fail "GITHUB_OUTPUT must not contain: $1"; }
in_sync()   { grep -qxF -- "latest_$1=$(cat "$PINS/$1")" "$WORK/gh_out" || fail "GITHUB_OUTPUT missing: latest_$1=<its pin>"; }
err_has()   { grep -qF -- "$1" "$WORK/err" || fail "stderr missing: $1"; }
says()      { grep -qF -- "$1" "$WORK/out" || fail "not printed: $1"; }
saysnt()    { if grep -qF -- "$1" "$WORK/out"; then fail "must not be printed: $1"; fi; }
sum_has()   { grep -qF -- "$1" "$WORK/summary.md" || fail "summary missing: $1"; }
sum_hasnt() { if grep -qF -- "$1" "$WORK/summary.md"; then fail "summary must not contain: $1"; fi; }
code_is()   { [ "$rc" = "$1" ] || fail "expected exit $1, got $rc"; }

# If the real script grows a row, this test must grow with it rather than quietly
# exercise fewer rows than it claims.
ROWS="$(grep -cE '^  "[a-z]+\|' "$CHECK")"
[ "$ROWS" = 8 ] || fail "cli-drift-check.sh has $ROWS target rows, the stubs answer 8"

echo "== case 1: nothing fails and every pin matches -> exit 0, no failed rows =="
STUB_FAIL="" STUB_DRIFT="" run_check
code_is 0
for name in claude opencode codex copilot agy cursor kiro rtk; do
  in_sync "$name"
done
out_has '^failed=$'
sum_has "in sync"
# The `?` column appears only for a row that could not be read, so its absence here is
# what makes its presence in the next cases mean something.
saysnt " ? "

echo "== case 2: rtk alone fails -> the other seven still answer =="
# The 2026-09-09 shape exactly: a real edge on claude, rtk's source down.
STUB_FAIL="rtk" STUB_DRIFT="claude" run_check
code_is 1   # drift on claude is reportable news, not an error
out_has '^failed=rtk$'
out_has '^latest_claude=9\.9\.9$'
out_has '^latest_rtk=$'
out_hasnt '^latest_rtk=.'
for name in opencode codex copilot agy cursor kiro; do
  in_sync "$name"
done
err_has "cli-drift-check: warning: rtk:"
sum_has "could not be read"

echo "== case 3: every source fails -> exit 2 (the control for case 2) =="
STUB_FAIL="claude,opencode,codex,copilot,agy,cursor,kiro,rtk" STUB_DRIFT="" run_check
code_is 2
out_has '^failed=claude,opencode,codex,copilot,agy,cursor,kiro,rtk$'
err_has "No release source could be read."

echo "== case 4: two rows fail and a third drifts -> still exit 1, both named =="
STUB_FAIL="kiro,rtk" STUB_DRIFT="codex" run_check
code_is 1
out_has '^failed=kiro,rtk$'
out_has '^latest_codex=9\.9\.9$'
err_has "cli-drift-check: warning: kiro:"
err_has "cli-drift-check: warning: rtk:"

echo "== case 5: one row fails, nothing drifts -> exit 0, and the failure is still said =="
# The quiet direction, and the reason `failed=` exists at all: the run is green, so
# nothing else distinguishes "eight in sync" from "seven in sync, one never read".
STUB_FAIL="cursor" STUB_DRIFT="" run_check
code_is 0
out_has '^failed=cursor$'
out_has '^latest_cursor=$'
says "Could not read: cursor"

echo "== case 6: a single named row -- reads clean, or is 'every row failed' =="
STUB_FAIL="rtk" STUB_DRIFT="" run_check claude
code_is 0
in_sync claude
STUB_FAIL="claude" STUB_DRIFT="" run_check claude
code_is 2

echo "== case 7: POSITIVE CONTROL -- the old all-or-nothing rule still fails case 2 =="
# Patch the new rule back to "any row failed -> exit 2" and require the defect to come
# back. Without this, a checker that had stopped reading STUB_FAIL entirely would pass
# every assertion above.
sed 's/\[ "${#failed\[@\]}" -eq "\$rows" \]/[ "${#failed[@]}" -gt 0 ]/' \
  "$CHECK" > "$WORK/old-check.sh"
chmod +x "$WORK/old-check.sh"
if cmp -s "$CHECK" "$WORK/old-check.sh"; then
  fail "the control patch matched nothing: the exit-2 rule in cli-drift-check.sh was reworded, so this control is inert"
fi
SCRIPT_UNDER_TEST="$WORK/old-check.sh"
STUB_FAIL="rtk" STUB_DRIFT="claude" run_check
[ "$rc" = 2 ] || fail "the patched copy did not reproduce the old behaviour (exit $rc, expected 2)"
SCRIPT_UNDER_TEST="$CHECK"

# --- cli-release-edges.sh ------------------------------------------------------------
#
# A pure decision: environment in, GITHUB_OUTPUT and the job summary out. No stubs, so
# what is left is exactly the branch that matters — which kinds get a dispatch.

# Seven kinds, all in sync, no failures. Cases override single entries on top.
edges_base=(
  LATEST_CLAUDE=2.1.265   TESTED_CLAUDE=2.1.265
  LATEST_CODEX=0.145.0    TESTED_CODEX=0.145.0
  LATEST_OPENCODE=1.0.0   TESTED_OPENCODE=1.0.0
  LATEST_COPILOT=1.0.0    TESTED_COPILOT=1.0.0
  LATEST_AGY=1.0.0        TESTED_AGY=1.0.0
  LATEST_CURSOR=1.0.0     TESTED_CURSOR=1.0.0
  LATEST_KIRO=0.1.0       TESTED_KIRO=0.1.0
  FAILED=
)
run_edges() { # run_edges [VAR=value ...]
  : > "$WORK/out"; : > "$WORK/err"; : > "$WORK/gh_out"; : > "$WORK/summary.md"
  set +e
  env GITHUB_OUTPUT="$WORK/gh_out" GITHUB_STEP_SUMMARY="$WORK/summary.md" \
    NOW=2026-09-11T05:30:12Z "${edges_base[@]}" "$@" "$EDGES" > "$WORK/out" 2> "$WORK/err"
  rc=$?
  set -e
  [ "$rc" = 0 ] || fail "cli-release-edges.sh exited $rc"
}

echo "== case 8: an edge on claude while rtk is unreadable -> claude is dispatched =="
run_edges FAILED=rtk LATEST_CLAUDE=2.1.267
out_has '^claude=true$'
out_has '^count=1$'
out_has '^skipped=$'          # rtk is no dispatch kind: nothing was held back for it
out_has '^watcher_failed=rtk$'
out_hasnt '^watcher_ok='      # a run with an unreadable row is not a clean run
out_has '^watcher_at=2026-09-11T05:30:12Z$'
sum_has 'not a dispatch target'
sum_has '`claude=2.1.267`'

echo "== case 9: the unreadable row IS a dispatch kind -> only that kind is held back =="
run_edges FAILED=kiro LATEST_CLAUDE=2.1.267 LATEST_KIRO=
out_has '^claude=true$'
out_has '^count=1$'
out_has '^skipped=kiro$'
out_hasnt '^kiro=true$'
out_has '^watcher_failed=kiro$'
sum_has 'its `seen` / `tested` update are skipped'

echo "== case 10: POSITIVE CONTROL -- an unknown latest is never an edge =="
# The trap the old inline loop walked into: `"" != "0.1.0"` is true, so an unread kiro
# was an "edge" and would have been dispatched with an empty version and then recorded
# as handled. The row is deliberately NOT in FAILED here, so only the empty-latest
# guard can catch it — remove that guard and this case goes red on its own.
run_edges LATEST_KIRO=
out_has '^count=0$'
out_hasnt '^kiro=true$'
out_has '^skipped=kiro$'

echo "== case 11: a clean run records the watcher as alive and says nothing else =="
run_edges
out_has '^count=0$'
out_has '^watcher_ok=2026-09-11T05:30:12Z$'
out_has '^watcher_failed=none$'
sum_has "No public version changed"
sum_hasnt "could not be read"

# --- cli-release-state.sh, watcher namespace -----------------------------------------
#
# The watcher block is a read-modify-write of the issue BODY, not another comment: it is
# written on every single run, and an append would bury the `tested` markers the whole
# mechanism reads back. A stub `gh` backed by two files stands in for the issue.
cat > "$STUB/gh" <<'FAKE'
#!/usr/bin/env bash
echo "gh $*" >> "$STUB_LOG"
case "$*" in
  *"issue list"*)
    [ -s "$STUB_ISSUE/body" ] && echo 7 ;;
  *"issue create"*)
    # --body is the argument after the flag; find it positionally.
    prev=""; for a in "$@"; do [ "$prev" = "--body" ] && printf '%s\n' "$a" > "$STUB_ISSUE/body"; prev="$a"; done
    echo "https://github.com/k-k1/agent-fleet/issues/7" ;;
  *"issue view"*"body,comments"*)
    cat "$STUB_ISSUE/body" "$STUB_ISSUE/comments" ;;
  *"issue view"*)
    cat "$STUB_ISSUE/body" ;;
  *"issue edit"*)
    prev=""; for a in "$@"; do [ "$prev" = "--body-file" ] && cp "$a" "$STUB_ISSUE/body"; prev="$a"; done ;;
  *"issue comment"*)
    prev=""; for a in "$@"; do [ "$prev" = "--body" ] && printf '%s\n' "$a" >> "$STUB_ISSUE/comments"; prev="$a"; done ;;
esac
exit 0
FAKE
chmod +x "$STUB/gh"
export STUB_ISSUE="$WORK/issue" STUB_LOG="$WORK/gh.log"
mkdir -p "$STUB_ISSUE"
: > "$STUB_ISSUE/body"; : > "$STUB_ISSUE/comments"; : > "$STUB_LOG"

state_is() { # state_is <namespace> <item> <expected>
  local got; got="$("$STATE" get "$1" "$2")"
  [ "$got" = "$3" ] || fail "state $1 $2: expected '$3', got '$got'"
}

echo "== case 12: watcher values round-trip through the issue body =="
"$STATE" ensure watcher ok
"$STATE" set watcher ok 2026-09-11T05:30:12Z
"$STATE" set watcher failed rtk
"$STATE" set watcher at 2026-09-11T05:30:12Z
state_is watcher ok 2026-09-11T05:30:12Z
state_is watcher failed rtk
# A timestamp carries `:` and a failed list carries `,`. The pre-existing version
# pattern rejects both, and that rejection would only ever surface on the day a source
# actually failed — i.e. never in a green run.
"$STATE" set watcher failed kiro,rtk
state_is watcher failed kiro,rtk
# Visible to a human reading the issue, not just to sed: an HTML comment answers
# nobody who opens the issue to ask why `tested` stopped moving.
grep -qF '### Watcher' "$STUB_ISSUE/body" || fail "the watcher block is not visible in the issue body"

echo "== case 13: the watcher block is rewritten, never appended =="
"$STATE" set watcher ok 2026-09-12T05:30:09Z
state_is watcher ok 2026-09-12T05:30:09Z
n="$(grep -c 'cli-release-state watcher ok=' "$STUB_ISSUE/body")"
[ "$n" = 1 ] || fail "the body carries $n 'watcher ok' lines; the block must be rewritten in place"
if grep -q 'cli-release-state watcher' "$STUB_ISSUE/comments"; then
  fail "the watcher state was appended as a comment; daily comments push the tested markers out of reach"
fi

echo "== case 14: tested/seen still append as comments, and survive the rewrites =="
"$STATE" set tested claude 2.1.267
"$STATE" set watcher at 2026-09-12T05:30:09Z
state_is tested claude 2.1.267
grep -qF '<!-- cli-release-state tested claude=2.1.267 -->' "$STUB_ISSUE/comments" ||
  fail "tested must still be an append-only comment"
# Positive control for the round-trip itself: a value that was never written must read
# back empty, or `state_is` would pass against a getter that echoes its own argument.
state_is tested codex ""

echo "== case 15: a value with a space is rejected rather than silently truncated =="
# The marker is read back with a `[^ ]*` match, so a space would store a value the
# getter can never return in full.
set +e
"$STATE" set watcher failed "rtk kiro" > "$WORK/out" 2> "$WORK/err"
rc=$?
set -e
[ "$rc" = 2 ] || fail "expected exit 2 for a watcher value containing a space, got $rc"
err_has "invalid value"

echo "OK: one unreadable release source no longer stops the watcher"
