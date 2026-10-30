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
  # @opencode/cli is the opencode row's package once the pin is 2.x. Under a 1.x pin it is
  # the informational "2.x line" instead: STUB_OC2 asks for that reading (and STUB_FAIL
  # opencode2 refuses it); without STUB_OC2 it answers as the row does, from the pin file,
  # with STUB_DRIFT / STUB_FAIL naming `opencode` and STUB_OCCLI overriding the answer.
  @opencode/cli)             echo "$2" >> "$STUB_NPM_LOG"
                             if [ -n "${STUB_OC2:-}" ]; then
                               case ",${STUB_FAIL:-}," in *",opencode2,"*) exit 1 ;; esac
                               echo "$STUB_OC2"; exit 0
                             fi
                             [ -z "${STUB_OCCLI:-}" ] || { echo "$STUB_OCCLI"; exit 0; }
                             case ",${STUB_FAIL:-}," in *",opencode,"*) exit 1 ;; esac
                             case ",${STUB_DRIFT:-}," in *",opencode,"*) echo "9.9.9"; exit 0 ;; esac
                             cat "$STUB_PINS/opencode"; echo; exit 0 ;;
  *) exit 1 ;;
esac
echo "$2" >> "$STUB_NPM_LOG"
[ "$name" != opencode ] || [ -z "${STUB_OCAI:-}" ] || { echo "$STUB_OCAI"; exit 0; }
case ",${STUB_FAIL:-}," in *",$name,"*) exit 1 ;; esac
case ",${STUB_DRIFT:-}," in *",$name,"*) echo "9.9.9"; exit 0 ;; esac
cat "$STUB_PINS/$name"; echo
FAKE

# curl answers the agy / cursor / kiro / muse / rtk rows. The cursor body is deliberately far
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
  *muse-code/channels*) name=muse ;;
  *rtk-ai/rtk*)  name=rtk ;;
  *) exit 22 ;;
esac
case ",${STUB_FAIL:-}," in *",$name,"*) exit 22 ;; esac
version="$(cat "$STUB_PINS/$name")"
# cursor's version is matched as `<digits and dots>-<hex>`, so its drift fixture has to
# keep that shape or the row reads as a fetch failure instead of as drift.
case ",${STUB_DRIFT:-}," in
  *",$name,"*)
    case "$name" in
      cursor) version="9999.99.99-deadbee" ;;
      # muse's pin IS a build id (1.3.0-R3401.1). A bare-semver drift fixture would pass
      # against a row that silently dropped the suffix, which is the exact defect the muse
      # row exists to keep out (install_muse.go's museVerRe).
      muse)   version="9.9.9-R9999.9" ;;
      *)      version="9.9.9" ;;
    esac ;;
esac
case "$name" in
  agy|kiro|muse) printf '{"version":"%s"}\n' "$version" ;;
  rtk)      printf '{"tag_name":"v%s"}\n' "$version" ;;
  cursor)
    # The real install script is a long shell file with the version buried in a URL.
    seq 1 4000 | sed 's/^/# leading padding line /'
    echo "  url=\"https://downloads.cursor.com/versions/${version}/linux\""
    seq 1 4000 | sed 's/^/# trailing padding line /' ;;
esac
FAKE
chmod +x "$STUB"/npm "$STUB"/curl
export PATH="$STUB:$PATH" STUB_PINS="$PINS" STUB_NPM_LOG="$WORK/npm.log"
: > "$WORK/npm.log"

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
[ "$ROWS" = 9 ] || fail "cli-drift-check.sh has $ROWS target rows, the stubs answer 9"

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

echo "== case 2: rtk alone fails -> every other row still answers =="
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
STUB_FAIL="claude,opencode,codex,copilot,agy,cursor,kiro,muse,rtk" STUB_DRIFT="" run_check
code_is 2
out_has '^failed=claude,opencode,codex,copilot,agy,cursor,kiro,muse,rtk$'
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
# nothing else distinguishes "all in sync" from "all but one in sync, one never read".
STUB_FAIL="cursor" STUB_DRIFT="" run_check
code_is 0
out_has '^failed=cursor$'
out_has '^latest_cursor=$'
says "Could not read: cursor"

# The checker resolves workspace/Dockerfile from its own location. The real tree pins 2.x
# (since the cut-over), so the 1.x cases run a copy of the tree whose pin is a 1.x one, which
# is also what a rollback re-pin looks like.
TREE1="$WORK/tree1"
mkdir -p "$TREE1/deploy/local" "$TREE1/workspace"
cp "$CHECK" "$TREE1/deploy/local/cli-drift-check.sh"
sed 's/^ARG OPENCODE_VERSION=.*/ARG OPENCODE_VERSION=1.19.0/' "$ROOT/workspace/Dockerfile" > "$TREE1/workspace/Dockerfile"
cp "$PINS/opencode" "$WORK/opencode.pin.keep"
use_pin1() { printf '1.19.0' > "$PINS/opencode"; SCRIPT_UNDER_TEST="$TREE1/deploy/local/cli-drift-check.sh"; }
use_pin2() { cp "$WORK/opencode.pin.keep" "$PINS/opencode"; SCRIPT_UNDER_TEST="$CHECK"; }

echo "== case 5b: while the pin is 1.x the 2.x line is reported, and only reported =="
use_pin1
STUB_FAIL="" STUB_DRIFT="" STUB_OC2=2.0.29 run_check
code_is 0                         # a new major is not drift
says "opencode2"
says "new major, not tracked as drift"
out_has '^latest_opencode2=2\.0\.29$'
out_has '^failed=$'
sum_has "2.x line"
# It is not a row: a registry that does not answer for it is skipped without a word, and
# `failed=` stays empty (case 5's rule would otherwise have to name it).
STUB_FAIL="opencode2" STUB_DRIFT="" STUB_OC2=2.0.29 run_check
code_is 0
out_has '^failed=$'
out_hasnt '^latest_opencode2='
saysnt "opencode2"
# ...and with every real row failing it is not a row that saves the run from exit 2.
STUB_FAIL="claude,opencode,codex,copilot,agy,cursor,kiro,muse,rtk" STUB_DRIFT="" STUB_OC2=2.0.29 run_check
code_is 2
# A single-row check of another CLI does not print it.
STUB_FAIL="" STUB_DRIFT="" STUB_OC2=2.0.29 run_check claude
saysnt "opencode2"
use_pin2

echo "== case 5c: the opencode row follows the package of the pin's major =="
# Positive control first -- the 1.x tree must ask for opencode-ai.
use_pin1
: > "$WORK/npm.log"
STUB_FAIL="" STUB_DRIFT="" run_check opencode
grep -qxF "opencode-ai" "$WORK/npm.log" || fail "1.x pin: opencode-ai was not asked for"
use_pin2
: > "$WORK/npm.log"
STUB_FAIL="" STUB_DRIFT="" run_check opencode
code_is 0
grep -qxF "@opencode/cli" "$WORK/npm.log" || fail "2.x pin: @opencode/cli was not asked for"
grep -qxF "opencode-ai" "$WORK/npm.log" && fail "2.x pin: opencode-ai must not be asked for" || true
out_has "^pin_opencode=$(cat "$PINS/opencode" | sed 's/\./\\./g')\$"
in_sync opencode
saysnt "opencode2"                # already on 2.x: nothing to announce
STUB_FAIL="" STUB_DRIFT="" STUB_OCCLI=2.99.0 run_check opencode
code_is 1
out_has '^latest_opencode=2\.99\.0$'
says "DRIFT"

echo "== case 5d: the pin's package publishing another major is flagged, not offered as a plain bump =="
use_pin1
STUB_FAIL="" STUB_DRIFT="" STUB_OCAI=2.0.0 run_check opencode
code_is 1
out_has '^latest_opencode=2\.0\.0$'
says "MAJOR"
sum_has "other major"
STUB_FAIL="" STUB_DRIFT="" STUB_OCAI=1.99.0 run_check opencode
code_is 1
says "DRIFT"                      # same major: an ordinary edge
saysnt "MAJOR"
use_pin2
# The same two directions on the 2.x pin the tree has now.
STUB_FAIL="" STUB_DRIFT="" STUB_OCCLI=3.0.0 run_check opencode
code_is 1
out_has '^latest_opencode=3\.0\.0$'
says "MAJOR"
STUB_FAIL="" STUB_DRIFT="" STUB_OCCLI=2.99.0 run_check opencode
code_is 1
says "DRIFT"
saysnt "MAJOR"

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

# Every dispatch kind, all in sync, no failures. Cases override single entries on top.
# It must stay in step with cli-release-edges.sh's KINDS default: a kind missing here has no
# LATEST_*, which the decision reads as "unknown" and holds back — so it would sit in
# `skipped=` in every case and quietly never exercise its own branch.
edges_base=(
  LATEST_CLAUDE=2.1.265   TESTED_CLAUDE=2.1.265
  LATEST_CODEX=0.145.0    TESTED_CODEX=0.145.0
  LATEST_OPENCODE=1.0.0   TESTED_OPENCODE=1.0.0
  LATEST_COPILOT=1.0.0    TESTED_COPILOT=1.0.0
  LATEST_AGY=1.0.0        TESTED_AGY=1.0.0
  LATEST_CURSOR=1.0.0     TESTED_CURSOR=1.0.0
  LATEST_KIRO=0.1.0       TESTED_KIRO=0.1.0
  # muse's versions are build ids, not semver — keep the fixture in that shape.
  LATEST_MUSE=1.3.0-R3401.1 TESTED_MUSE=1.3.0-R3401.1
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
sum_has 'its `tested` update are skipped'

echo "== case 9b: a muse build-id edge is a dispatch like any other =="
# muse's versions carry a build id (1.3.0-R3401.1 -> 1.3.0-R3402.1), and the decision compares
# them as opaque strings. A comparison that normalised to semver would see 1.3.0 both sides and
# miss the release entirely — which is the same defect the drift row's fixture guards.
run_edges LATEST_MUSE=1.3.0-R3402.1
out_has '^muse=true$'
out_has '^count=1$'
out_has '^skipped=$'
sum_has '`muse=1.3.0-R3402.1`'

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

echo "== case 11b: a red marker for latest holds the dispatch back, and only that kind =="
# The brake: at a 2-hour cadence a version whose contract is red would otherwise be
# re-dispatched ~12 times a day. codex has a real edge here, so it must still go out.
run_edges RED_CLAUDE=2.1.267 LATEST_CLAUDE=2.1.267 LATEST_CODEX=0.146.0
out_hasnt '^claude=true$'
out_has '^codex=true$'
out_has '^count=1$'
out_has '^red=claude=2.1.267$'
sum_has "contract already failed"

echo "== case 11c: POSITIVE CONTROL -- without the marker the same edge dispatches =="
run_edges LATEST_CLAUDE=2.1.267
out_has '^claude=true$'
out_has '^red=$'

echo "== case 11d: a new latest clears the brake =="
run_edges RED_CLAUDE=2.1.267 LATEST_CLAUDE=2.1.268
out_has '^claude=true$'
out_has '^red=$'

echo "== case 11e: tested == latest wins over a stale red marker =="
# A manual dispatch that passed records tested; the marker must not make the kind look held.
run_edges RED_CLAUDE=2.1.267 LATEST_CLAUDE=2.1.267 TESTED_CLAUDE=2.1.267
out_hasnt '^claude=true$'
out_has '^red=$'
out_has '^count=0$'

echo "== case 11f: cursor and kiro keep their behaviour: a red marker does not hold them =="
run_edges RED_CURSOR=1.1.0 LATEST_CURSOR=1.1.0 RED_KIRO=0.2.0 LATEST_KIRO=0.2.0
out_has '^cursor=true$'
out_has '^kiro=true$'
out_has '^count=2$'
out_has '^red=$'

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

# --- cli-contract-report.sh ----------------------------------------------------------
#
# The state issue (7) and one update issue per CLI (100, 101, … as they are created). Each
# issue is a directory holding title, body, state and comments; the stub picks the CLI's
# issue out of `issue list --label cli-drift` by the title prefix the script's --jq asks for.
REPORT="$HERE/cli-contract-report.sh"
export STUB_ISSUES="$WORK/issues" STUB_RUNS="$WORK/runs" STUB_PRS="$WORK/prs.json"
mkdir -p "$STUB_ISSUES" "$STUB_RUNS"
ln -s "$STUB_ISSUE" "$STUB_ISSUES/7"
echo '[]' > "$STUB_PRS"
cat > "$STUB/gh" <<'FAKE'
#!/usr/bin/env bash
echo "gh $*" >> "$STUB_LOG"
arg_after() { local want="$1"; shift; local prev="" a; for a in "$@"; do [ "$prev" = "$want" ] && { printf '%s' "$a"; return; }; prev="$a"; done; }
I="$STUB_ISSUES"
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label cli-drift"*)
        kind="$(printf '%s' "$*" | sed -n 's/.*startswith("\([a-z0-9-]*\) CLI: ").*/\1/p')"
        # The search index has not caught up with a new issue yet: the kind has none.
        [ ! -e "$STUB_RUNS/nolist-$kind" ] || exit 0
        if [ -e "$STUB_RUNS/race-$kind" ]; then
          # Another writer opens the kind's issue just after this lookup answered "none".
          rm "$STUB_RUNS/race-$kind"
          n=100; while [ -e "$I/$n" ]; do n=$((n + 1)); done
          mkdir "$I/$n"; printf '%s CLI: opened elsewhere' "$kind" > "$I/$n/title"; echo open > "$I/$n/state"
          : > "$I/$n/comments"; echo 'placeholder' > "$I/$n/body"
          exit 0
        fi
        for d in "$I"/1[0-9][0-9]; do
          [ -d "$d" ] && [ "$(cat "$d/state")" = open ] || continue
          case "$(cat "$d/title")" in "$kind CLI: "*) basename "$d"; break ;; esac
        done ;;
      *) [ -s "$I/7/body" ] && echo 7 ;;
    esac ;;
  "issue create")
    title="$(arg_after --title "$@")"
    if [ "$title" = "CLI release watcher state" ]; then
      printf '%s\n' "$(arg_after --body "$@")" > "$I/7/body"; echo "https://github.com/k-k1/agent-fleet/issues/7"; exit 0
    fi
    n=100; while [ -e "$I/$n" ]; do n=$((n + 1)); done
    mkdir "$I/$n"; printf '%s' "$title" > "$I/$n/title"; echo open > "$I/$n/state"; : > "$I/$n/comments"
    cp "$(arg_after --body-file "$@")" "$I/$n/body"
    echo "https://github.com/k-k1/agent-fleet/issues/$n" ;;
  "issue view")
    case "$*" in
      *"body,comments"*) cat "$I/$3/body" "$I/$3/comments" 2>/dev/null ;;
      *"--json title"*)  cat "$I/$3/title"; echo ;;
      *) cat "$I/$3/body" ;;
    esac ;;
  "issue edit")
    if [ -e "$STUB_RUNS/clobber" ]; then
      # A concurrent writer holding a stale copy overwrites this edit with the old body.
      rm -f "$STUB_RUNS/clobber"
    else
      t="$(arg_after --title "$@")"; [ -z "$t" ] || printf '%s' "$t" > "$I/$3/title"
      f="$(arg_after --body-file "$@")"; [ -z "$f" ] || cp "$f" "$I/$3/body"
    fi ;;
  "issue comment")
    printf '%s\n' "$(arg_after --body "$@")" >> "$I/$3/comments" ;;
  "issue close")
    echo closed > "$I/$3/state"; printf '%s\n' "$(arg_after --comment "$@")" >> "$I/$3/comments" ;;
  "pr list")
    cat "$STUB_PRS" ;;
  "api repos/k-k1/agent-fleet/actions/runs/555/jobs?per_page=100")
    cat "$STUB_RUNS/steps" ;;
  "run list")
    cat "$STUB_RUNS/$(arg_after --workflow "$@" | sed 's/\.yml$//').json" ;;
esac
exit 0
FAKE
chmod +x "$STUB/gh"
# Every report case below runs without the follow-up sync; the sync cases further down
# drive it directly.
export SYNC=true
: > "$STUB_ISSUE/comments"
issue_of()  { "$HERE/cli-update-issues.sh" number "$1"; }
body_of()   { local n; n="$(issue_of "$1")"; [ -n "$n" ] || fail "no open issue for $1"; cat "$STUB_ISSUES/$n/body"; }
title_of()  { local n; n="$(issue_of "$1")"; [ -n "$n" ] || fail "no open issue for $1"; cat "$STUB_ISSUES/$n/title"; }
has()       { body_of "$1" | grep -qF -- "$2" || fail "$1 issue missing: $2"; }
hasnt()     { local n; n="$(issue_of "$1")"; [ -z "$n" ] && return 0; if grep -qF -- "$2" "$STUB_ISSUES/$n/body"; then fail "$1 issue must not contain: $2"; fi; }
no_issue()  { [ -z "$(issue_of "$1")" ] || fail "$1 must have no open issue, has #$(issue_of "$1"): $(title_of "$1")"; }
export GITHUB_SERVER_URL=https://github.com GITHUB_REPOSITORY=k-k1/agent-fleet GITHUB_RUN_ID=555

# What the jobs API says, already reduced by the --jq in the script: one failed step name per
# line, with a duplicate (a matrix repeats the same step name per leg).
printf '%s\n' 'real TUI pane drift contract' 'real models catalog drift contract' 'real TUI pane drift contract' > "$STUB_RUNS/steps"

echo "== case 16: a red contract with no issue creates that CLI's issue with the section =="
"$REPORT" failure codex 0.146.0
has codex 'Red contracts'
has codex '`codex` `0.146.0`'
has codex 'failing step: `real TUI pane drift contract`, `real models catalog drift contract`'
has codex 'actions/runs/555'
[ "$(title_of codex)" = "codex CLI: contract failed" ] || fail "unexpected title: $(title_of codex)"
n="$(body_of codex | grep -o 'real TUI pane drift contract' | wc -l)"
[ "$n" = 1 ] || fail "a repeated step name must be listed once, got $n"
state_is red codex 0.146.0

echo "== case 16b: the list lookup stays empty after creation; the number comes from the create URL =="
touch "$STUB_RUNS/nolist-agy"
"$REPORT" failure agy 1.0.0 || fail "failure must survive a lagging issue lookup (exit $?)"
rm "$STUB_RUNS/nolist-agy"
has agy '`agy` `1.0.0`'
state_is red agy 1.0.0
echo closed > "$STUB_ISSUES/$(issue_of agy)/state"   # later cases expect no open agy issue

echo "== case 17: another CLI gets its own issue; the same kind replaces its own row =="
"$REPORT" failure claude 2.1.270
: > "$STUB_RUNS/steps"
"$REPORT" failure codex 0.146.0
[ "$(issue_of claude)" != "$(issue_of codex)" ] || fail "claude and codex share an issue"
n="$(body_of codex | grep -c '<!-- cli-contract-failure ')"
[ "$n" = 1 ] || fail "expected 1 failure row in the codex issue, got $n"
has codex 'failing step not captured'   # codex's replaced row
body_of codex | grep -F '<!-- cli-contract-failure codex -->' | grep -qF 'real TUI pane' && fail "codex kept its old row's steps"
hasnt codex '`claude`'
has claude '`claude` `2.1.270`'
state_is red claude 2.1.270

echo "== case 18: cursor is reported but gets no red marker =="
"$REPORT" failure cursor 1.2.0
has cursor '`cursor` `1.2.0`'
state_is red cursor ""

echo "== case 18b: an advisory caps finding writes a row but never a red marker; success leaves it =="
"$REPORT" advisory copilot 1.4.0 'caps drift probes'
has copilot '<!-- cli-contract-caps copilot -->'
has copilot 'advisory, no gate'
state_is red copilot ""
"$REPORT" success copilot
has copilot '<!-- cli-contract-caps copilot -->'
LATEST_COPILOT=1.5.0 "$REPORT" prune
hasnt copilot '<!-- cli-contract-caps copilot -->'
"$REPORT" advisory copilot 1.5.0
has copilot '`copilot` `1.5.0`'
"$REPORT" advisory-clear copilot
hasnt copilot '<!-- cli-contract-caps copilot -->'
state_is red copilot ""
echo closed > "$STUB_ISSUES/$(issue_of copilot)/state"   # later cases expect no open copilot issue

echo "== case 19: a body rewrite keeps the section (carry) =="
printf 'fresh next step\n' > "$WORK/newbody.md"
"$REPORT" carry "$(issue_of claude)" "$WORK/newbody.md"
grep -qF 'fresh next step' "$WORK/newbody.md" || fail "carry dropped the new body"
grep -qF '`claude` `2.1.270`' "$WORK/newbody.md" || fail "carry dropped the section"
"$REPORT" active claude || fail "active must be true while rows exist"

echo "== case 20: success removes only its own row; prune drops a superseded version =="
"$REPORT" success cursor
hasnt cursor '`cursor` `1.2.0`'
LATEST_CLAUDE=2.1.271 LATEST_CODEX=0.146.0 "$REPORT" prune
hasnt claude '`claude` `2.1.270`'
has codex '`codex` `0.146.0`'
LATEST_CODEX= "$REPORT" prune   # an unread latest keeps the row
has codex '`codex` `0.146.0`'
"$REPORT" success codex
hasnt codex 'Red contracts'
if "$REPORT" active codex; then fail "active must be false once the section is empty"; fi

echo "== case 21: N=2 red runs in a row flags the workflow in its CLI's issue; a newer green clears it =="
NOWISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
OLDISO="$(date -u -d '30 days ago' +%Y-%m-%dT%H:%M:%SZ)"
cat > "$STUB_RUNS/codex-contract.json" <<J
[{"conclusion":"failure","url":"https://x/run/3","createdAt":"$NOWISO"},{"conclusion":"failure","url":"https://x/run/2","createdAt":"$NOWISO"},{"conclusion":"success","url":"https://x/run/1","createdAt":"$NOWISO"}]
J
"$REPORT" streak codex-contract 2
has codex '`codex-contract` failed its last 2 runs'
has codex 'https://x/run/3'
cat > "$STUB_RUNS/codex-contract.json" <<J
[{"conclusion":"failure","url":"https://x/run/4","createdAt":"$NOWISO"},{"conclusion":"success","url":"https://x/run/3","createdAt":"$NOWISO"},{"conclusion":"failure","url":"https://x/run/2","createdAt":"$NOWISO"}]
J
"$REPORT" streak codex-contract 2
hasnt codex 'Red contracts'

echo "== case 22: fewer than N finished runs is not a streak; claude-tui-contract maps to claude =="
echo '[{"conclusion":"failure","url":"https://x/run/9","createdAt":"'"$NOWISO"'"}]' > "$STUB_RUNS/claude-tui-contract.json"
"$REPORT" streak claude-tui-contract 2
hasnt claude 'claude-tui-contract'
echo '[{"conclusion":"failure","url":"https://x/run/9","createdAt":"'"$NOWISO"'"},{"conclusion":"failure","url":"https://x/run/8","createdAt":"'"$NOWISO"'"}]' > "$STUB_RUNS/claude-tui-contract.json"
"$REPORT" streak claude-tui-contract 2
has claude '`claude-tui-contract` failed its last 2 runs'   # control: the mapping is live
echo '[{"conclusion":"success","url":"https://x/run/10","createdAt":"'"$NOWISO"'"}]' > "$STUB_RUNS/claude-tui-contract.json"
"$REPORT" streak claude-tui-contract 2

echo "== case 22b: a red streak whose newest run is 30 days old is history, and is dropped =="
cat > "$STUB_RUNS/agy-contract.json" <<J
[{"conclusion":"failure","url":"https://x/run/8","createdAt":"$NOWISO"},{"conclusion":"failure","url":"https://x/run/7","createdAt":"$NOWISO"}]
J
"$REPORT" streak agy-contract 2
has agy '`agy-contract` failed its last 2 runs'   # control: fresh -> flagged
[ "$(title_of agy)" = "agy CLI: contract fails on main" ] || fail "unexpected streak title: $(title_of agy)"
cat > "$STUB_RUNS/agy-contract.json" <<J
[{"conclusion":"failure","url":"https://x/run/8","createdAt":"$OLDISO"},{"conclusion":"failure","url":"https://x/run/7","createdAt":"$OLDISO"}]
J
"$REPORT" streak agy-contract 2
hasnt agy 'agy-contract'

echo "== case 23: a write overwritten by a stale concurrent writer is re-applied =="
"$REPORT" failure kiro 0.3.0
cat > "$STUB_RUNS/kiro-contract.json" <<J
[{"conclusion":"failure","url":"https://x/run/6","createdAt":"$NOWISO"},{"conclusion":"failure","url":"https://x/run/5","createdAt":"$NOWISO"}]
J
touch "$STUB_RUNS/clobber"
"$REPORT" streak kiro-contract 2
[ ! -e "$STUB_RUNS/clobber" ] || fail "the clobber hook never fired; the case proves nothing"
has kiro '`kiro-contract` failed its last 2 runs'
has kiro '`kiro` `0.3.0`'

echo "== case 23b: a failure whose version is unknown never opens an issue, but lands in an open one =="
"$REPORT" failure copilot unknown
no_issue copilot
"$REPORT" failure copilot 1.0.97      # control: a real version does open it
"$REPORT" failure copilot unknown
has copilot '`copilot` `unknown`'
"$REPORT" success copilot

echo "== case 24: success clears the red marker as well as the row =="
"$REPORT" failure opencode 1.2.0
state_is red opencode 1.2.0
"$REPORT" success opencode
state_is red opencode none
hasnt opencode '`opencode` `1.2.0`'

echo "== case 24b: a fragments row is its own row: no red marker, kept by success, replaced, pruned, cleared =="
red_before="$("$STATE" get red claude)"
"$REPORT" fragments claude 2.1.300 'run /login' 'Esc to cancel'
has claude '`claude` `2.1.300` — upstream message fragments gone from the package: `run /login`, `Esc to cancel`'
has claude '<!-- cli-contract-fragments claude -->'
state_is red claude "$red_before"
"$REPORT" failure claude 2.1.300
"$REPORT" success claude
has claude '<!-- cli-contract-fragments claude -->'   # a passing contract does not vouch for wording
hasnt claude '<!-- cli-contract-failure claude -->'
"$REPORT" fragments claude 2.1.300 'Enter to confirm'
n="$(body_of claude | grep -c '<!-- cli-contract-fragments ')"
[ "$n" = 1 ] || fail "expected 1 fragments row, got $n"
hasnt claude 'run /login'
has claude '`Enter to confirm`'
"$REPORT" fragments-clear agy   # clearing where there is nothing to clear is not an error
LATEST_CLAUDE=2.1.301 "$REPORT" prune
hasnt claude 'cli-contract-fragments'
"$REPORT" fragments claude 2.1.301 'Enter to confirm'
"$REPORT" fragments-clear claude
hasnt claude 'cli-contract-fragments'
if "$REPORT" fragments claude 2.1.301 'a`b' 2>/dev/null; then fail "a backtick in a fragment must be refused"; fi
if "$REPORT" fragments claude 2.1.301 2>/dev/null; then fail "fragments without any fragment must be refused"; fi

# --- cli-update-issues.sh ------------------------------------------------------------
#
# The per-CLI issues themselves. The release sources and the `tested` state are stubbed
# as files; the red-contract rows are the real ones from cli-contract-report.sh.
SYNC_SH="$HERE/cli-update-issues.sh"
# Start from no open update issue: close whatever the report cases left behind.
for d in "$STUB_ISSUES"/1[0-9][0-9]; do echo closed > "$d/state"; done
export STUB_LATEST="$WORK/latest.out" STUB_TESTED="$WORK/tested"
mkdir -p "$STUB_TESTED"
cat > "$STUB/drift-check" <<'FAKE'
#!/usr/bin/env bash
cat "$STUB_LATEST" >> "$GITHUB_OUTPUT"
exit 1
FAKE
cat > "$STUB/release-state" <<'FAKE'
#!/usr/bin/env bash
case "$1" in
  get)      cat "$STUB_TESTED/$2-$3" 2>/dev/null || true ;;
  evidence) echo "https://x/run/tested-$3" ;;
esac
FAKE
chmod +x "$STUB/drift-check" "$STUB/release-state"
export DRIFT_CHECK="$STUB/drift-check" RELEASE_STATE="$STUB/release-state"
# latest <kind>=<pin>:<latest> ... ; failed=<csv>
releases() {
  : > "$STUB_LATEST"
  local kv
  for kv in "$@"; do
    case "$kv" in
      failed=*) printf '%s\n' "$kv" >> "$STUB_LATEST" ;;
      *) printf 'pin_%s=%s\nlatest_%s=%s\n' "${kv%%=*}" "$(sed 's/=.*//; s/:.*//' <<< "${kv#*=}")" "${kv%%=*}" "${kv##*:}" >> "$STUB_LATEST" ;;
    esac
  done
}
tested() { printf '%s' "$2" > "$STUB_TESTED/tested-$1"; }
sync_ok() { "$SYNC_SH" sync "$@" > "$WORK/out" 2> "$WORK/err" || fail "sync $* exited non-zero"; }
title_is() { [ "$(title_of "$1")" = "$2" ] || fail "$1 title: expected '$2', got '$(title_of "$1")'"; }

echo "== case 25: kiro behind with an old tested -> an issue whose title and command are the next step =="
releases kiro=2.29.1:2.30.0
tested kiro 2.29.1
sync_ok kiro
title_is kiro "kiro CLI: run the contract on 2.30.0"
has kiro 'gh workflow run kiro-contract.yml --ref main -f cli_version=latest'
has kiro '<!-- cli-update kind=kiro -->'
kiro_num="$(issue_of kiro)"

echo "== case 26: an unattended kind behind its latest opens nothing; without its secret it does =="
releases claude=2.1.300:2.1.301
sync_ok claude
no_issue claude
CRED_CLAUDE=false sync_ok claude   # positive control: the same drift, now a person's job
title_is claude "claude CLI: verify 2.1.301 (no CI credential)"
has claude 'E2E_CLAUDE_OAUTH_TOKEN'
has claude 'reaches the TUI'

echo "== case 27: once latest passes, the same issue says to merge the bump PR it links =="
releases kiro=2.29.1:2.30.0
tested kiro 2.30.0
cat > "$STUB_PRS" <<'J'
[{"number":2086,"body":"| CLI | Pin | Evidence | Checksums |\n|---|---|---|---|\n| kiro | `2.29.1` → `2.30.0` | x | y |\n"}]
J
sync_ok kiro
[ "$(issue_of kiro)" = "$kiro_num" ] || fail "the kiro issue was replaced instead of updated"
title_is kiro "kiro CLI: merge the pin bump to 2.30.0"
has kiro 'merge #2086'
has kiro 'https://x/run/tested-kiro'

echo "== case 28: a PR that carries another version is not linked =="
echo '[{"number":2087,"body":"| kiro | `2.29.1` → `2.29.9` | x | y |"}]' > "$STUB_PRS"
sync_ok kiro
hasnt kiro '#2087'
has kiro 'opens the PR that bumps the pin'
echo '[]' > "$STUB_PRS"

echo "== case 29: a kind that passed but has no issue gets none (the PR is the whole interaction) =="
releases codex=0.162.1:0.163.0
tested codex 0.163.0
sync_ok codex
no_issue codex

echo "== case 30: rtk has no contract -> a hand bump =="
releases rtk=0.51.0:0.52.0
sync_ok rtk
title_is rtk "rtk CLI: bump the pin to 0.52.0"
has rtk 'RTK_VERSION'

echo "== case 31: a red row for latest names the fix, keeps the section, and the dispatch fits the kind =="
releases muse=1.4.4:1.4.7 codex=0.162.1:0.163.1
tested muse 1.4.4
tested codex 0.162.1
"$REPORT" failure muse 1.4.7
"$REPORT" failure codex 0.163.1
sync_ok muse codex
title_is muse "muse CLI: contract failed on 1.4.7"
has muse 'gh workflow run muse-contract.yml --ref main -f version=latest'
has muse '`muse` `1.4.7`'
has muse 'Red contracts'
title_is codex "codex CLI: contract failed on 0.163.1"
has codex 'gh workflow run codex-contract.yml --ref main'
hasnt codex 'cli_version'

echo "== case 32: a newer release replaces the red one on an unattended kind -> the issue closes =="
codex_num="$(issue_of codex)"
releases codex=0.162.1:0.163.2
sync_ok codex
no_issue codex
[ "$(cat "$STUB_ISSUES/$codex_num/state")" = closed ] || fail "codex issue not closed"
grep -qF 'Nothing left to do here' "$STUB_ISSUES/$codex_num/comments" || fail "closed without saying why"

echo "== case 33: the pin caught up -> the issue closes; an unreadable source leaves an issue alone =="
releases rtk=0.52.0:0.52.0 kiro=2.29.1:2.30.0 failed=kiro
sync_ok rtk kiro
no_issue rtk
[ "$(issue_of kiro)" = "$kiro_num" ] || fail "an unreadable kiro source touched its issue"
grep -qF 'kiro: release source unreadable' "$WORK/out" || fail "the unreadable row was not reported"
releases kiro=2.30.0:2.30.0
sync_ok kiro
no_issue kiro

echo "== case 33b: a red marker with no row (recorded before the issue existed) still opens it =="
releases muse=1.4.4:1.4.8
printf '1.4.8' > "$STUB_TESTED/red-muse"
for d in "$STUB_ISSUES"/1[0-9][0-9]; do echo closed > "$d/state"; done
sync_ok muse
title_is muse "muse CLI: contract failed on 1.4.8"
"$REPORT" failure muse unknown    # replaces the row in the open issue; the marker must still count
sync_ok muse
title_is muse "muse CLI: contract failed on 1.4.8"
rm "$STUB_TESTED/red-muse"
for d in "$STUB_ISSUES"/1[0-9][0-9]; do echo closed > "$d/state"; done
sync_ok muse   # control: the same release without the marker is the watcher's to run
no_issue muse

echo "== case 33c: a writer that opened the issue between lookup and create is reused, not doubled =="
for d in "$STUB_ISSUES"/1[0-9][0-9]; do echo closed > "$d/state"; done
releases rtk=0.51.0:0.52.0
touch "$STUB_RUNS/race-rtk"
sync_ok rtk
[ ! -e "$STUB_RUNS/race-rtk" ] || fail "the race hook never fired; the case proves nothing"
n=0; for d in "$STUB_ISSUES"/1[0-9][0-9]; do
  [ "$(cat "$d/state")" = open ] && case "$(cat "$d/title")" in "rtk CLI: "*) n=$((n + 1)) ;; esac
done
[ "$n" = 1 ] || fail "expected 1 open rtk issue, got $n"
title_is rtk "rtk CLI: bump the pin to 0.52.0"

echo "== case 33d: a sync with nothing new does not edit the issue =="
grep -q "^gh issue edit $(issue_of rtk) " "$STUB_LOG" || fail "control: case 33c never edited the issue"
: > "$STUB_LOG"
sync_ok rtk
grep -qF 'unchanged' "$WORK/out" || fail "the unchanged issue was not reported as such"
if grep -q '^gh issue edit' "$STUB_LOG"; then fail "an unchanged issue was edited"; fi

echo "== case 34: a streak keeps the issue open even when the pin is current =="
releases agy=1.3.4:1.3.4
cat > "$STUB_RUNS/agy-contract.json" <<J
[{"conclusion":"failure","url":"https://x/run/8","createdAt":"$NOWISO"},{"conclusion":"failure","url":"https://x/run/7","createdAt":"$NOWISO"}]
J
"$REPORT" streak agy-contract 2
sync_ok agy
title_is agy "agy CLI: contract fails on main"
has agy 'gh workflow run agy-contract.yml --ref main -f cli_version=latest'

echo "== case 35: the list lookup stays empty through the real sync -> still one issue, with the row and the marker =="
for d in "$STUB_ISSUES"/1[0-9][0-9]; do echo closed > "$d/state"; done
releases muse=1.0.0:1.1.0
touch "$STUB_RUNS/nolist-muse"
SYNC="$SYNC_SH" "$REPORT" failure muse 1.1.0 || fail "failure with a lagging lookup exited $?"
rm "$STUB_RUNS/nolist-muse"
n=0
for d in "$STUB_ISSUES"/1[0-9][0-9]; do
  [ "$(cat "$d/state")" = open ] && case "$(cat "$d/title")" in "muse CLI: "*) n=$((n + 1)) ;; esac
done
[ "$n" = 1 ] || fail "expected one muse issue after report + sync, got $n"
has muse '`muse` `1.1.0`'
title_is muse "muse CLI: contract failed on 1.1.0"

echo "== case 30: a gone fragment keeps an issue open on a kind the watcher handles; clearing it lets it close =="
for d in "$STUB_ISSUES"/1[0-9][0-9]; do case "$(cat "$d/title")" in "agy CLI: "*) echo closed > "$d/state" ;; esac; done
releases agy=1.0.0:1.0.1
sync_ok agy
no_issue agy   # control: the same drift with no row opens nothing
"$REPORT" fragments agy 1.0.1 'Esc to cancel'
sync_ok agy
title_is agy "agy CLI: upstream message fragments gone in 1.0.1"
has agy '`Esc to cancel`'
has agy 'This is a place to look, not proof of a break'
tested agy 1.0.1                       # the contract passed: still not "just merge"
sync_ok agy
title_is agy "agy CLI: upstream message fragments gone in 1.0.1"
releases agy=1.0.0:1.0.2               # a newer release replaced the one the row was about
sync_ok agy
no_issue agy
"$REPORT" fragments-clear agy

echo "== case 34: an advisory caps row for latest keeps a current pin's issue open; a pass or a newer release lets it close =="
for d in "$STUB_ISSUES"/1[0-9][0-9]; do echo closed > "$d/state"; done
releases copilot=1.4.0:1.4.0
"$REPORT" advisory copilot 1.4.0
sync_ok copilot
title_is copilot "copilot CLI: caps probes drifted on 1.4.0"
has copilot 'gate nothing yet'
state_is red copilot none   # still no marker (the earlier success cleared it to none)
"$REPORT" advisory-clear copilot
sync_ok copilot
no_issue copilot
"$REPORT" advisory copilot 1.4.0
releases copilot=1.4.0:1.4.1           # a newer release replaced the one the row was about
sync_ok copilot
no_issue copilot

echo "OK: one unreadable release source no longer stops the watcher"
