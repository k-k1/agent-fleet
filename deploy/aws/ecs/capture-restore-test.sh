#!/usr/bin/env bash
# Agent Fleet — regression test around the captured restore point (the reasoning itself
# lives in deploy/aws/ecs/env.sh).
#
#   deploy/aws/ecs/capture-restore-test.sh
#
# ## Why this exists
#
# The deployment scripts only run in full against real AWS. That does not mean nothing can
# be checked until a real deployment proves it: the branches produced by the three-part
# trap (re-tagging inside ECR / the capture staying stale without --force / ECR's
# EmptyOnDelete) can be walked here by substituting `aws` and `crane`. Only those branches
# are measured here — whether CFN accepts the templates and whether the deployment actually
# comes up is not, and only a real deployment can say.
#
# ## What is substituted
#
#   - `aws` / `crane` … fakes placed at the front of PATH. They answer from their arguments
#   - where the capture lives … `AF_DEPLOY_STATE_DIR` (read by env.sh's af_state_root)
#   - what GHCR holds … `AF_TEST_GHCR` (the list of "tags that exist" the fake crane reads)
#
# If a fake silently answers nothing, the checks go green. So every case demands a string
# that can only appear on that branch, and the end counts how many times the fakes were
# actually called.
#
# Being precise about the words: CFN create / delete / update happen 0 times (so nothing is
# billed). There are 198 calls, and all of them are reads against the fake `aws`
# (`cloudformation describe-stacks` 180 of them, plus others). Writing "0 CFN calls" would
# be a lie once quoted elsewhere, hence the distinction.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
PASS=0
FAIL=0

ok()   { PASS=$((PASS + 1)); echo "  OK   $1"; }
bad()  { FAIL=$((FAIL + 1)); echo "  NG   $1"; [ -n "${2:-}" ] && echo "       $2"; return 0; }
# want <name> <file> <substring> — that string must be in the output.
want() { if grep -qF "$3" "$2"; then ok "$1"; else bad "$1" "not in the output: $3"; fi; }
# nowant <name> <file> <substring>
nowant() { if grep -qF "$3" "$2"; then bad "$1" "a string that must not appear did: $3"; else ok "$1"; fi; }

# --- fake aws (holds this deployment's "live shape" in one place) -------------
mkdir -p "$T/bin"
cat > "$T/bin/aws" <<'STUB'
#!/usr/bin/env bash
# Record the arguments it was called with, so "green because the fake was never called"
# can be told apart from a real pass.
echo "$*" >> "${AF_TEST_AWS_LOG:-/dev/null}"
args="$*"
stack=""
prev=""
for a in "$@"; do
  [ "$prev" = "--stack-name" ] && stack="$a"
  prev="$a"
done
param() {  # param <stack> <key>
  case "$1.$2" in
    af-ecs-ingress.Fqdn)             echo "af.example.test" ;;
    af-ecs-ingress.NetworkStackName) echo "af-ecs-network" ;;
    af-ecs-ingress.DataStackName)    echo "af-ecs-data" ;;
    af-ecs-ingress.PlatformStackName) echo "af-ecs-platform" ;;
    af-ecs-ingress.WsRuntime)        echo "ecs" ;;
    af-ecs-ingress.ImageTag)         echo "${AF_TEST_LIVE_TAG:-0.0.9-dev-abcd1234}" ;;
    af-ecs-ingress.SsmPrefix)        echo "/af-cp" ;;
    af-ecs-ingress.CpArch)           echo "x86_64" ;;
    af-ecs-ingress.Ec2SlotLaunchTemplate) echo "" ;;
    af-ecs-data.Persistence)         echo "delete" ;;
    *) echo "" ;;
  esac
}
case "$args" in
  *"ParameterKey=='"*)
    key="${args#*ParameterKey==\'}"; key="${key%%\'*}"
    param "$stack" "$key" ;;
  *"OutputKey=='"*) echo "" ;;
  *"Parameters[].join"*)
    printf 'Fqdn=af.example.test\tImageTag=%s\tSsmPrefix=/af-cp\n' "${AF_TEST_LIVE_TAG:-0.0.9-dev-abcd1234}" ;;
  *"Outputs[].join"*) echo "" ;;
  *"cloudformation describe-stacks"*)
    case "$stack" in af-ecs-*) exit 0 ;; *) exit 254 ;; esac ;;
  *"list-exports"*) echo "" ;;
  *"sts get-caller-identity"*) echo "111122223333" ;;
  *) echo "" ;;
esac
exit 0
STUB

# --- fake crane (AF_TEST_GHCR decides what is in GHCR) -----------------------
cat > "$T/bin/crane" <<'STUB'
#!/usr/bin/env bash
echo "$*" >> "${AF_TEST_CRANE_LOG:-/dev/null}"
[ "${1:-}" = "manifest" ] || exit 0
ref="${2##*/}"          # <repo>:<tag>
for have in ${AF_TEST_GHCR:-}; do
  [ "$have" = "$ref" ] && exit 0
done
exit 1
STUB
chmod +x "$T/bin/aws" "$T/bin/crane"
PATH="$T/bin:$PATH"
export PATH

export AF_DEPLOY_STATE_DIR="$T/state"
export AF_TEST_AWS_LOG="$T/aws.log"
export AF_TEST_CRANE_LOG="$T/crane.log"
ENVDIR="$T/state/dev.ap-northeast-1"

capture() { "$HERE/capture-env.sh" --profile dev --region ap-northeast-1 "$@"; }

echo "== 1. capture a deployment whose restore point is complete in GHCR =="
rm -rf "$T/state"
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234 workspace:0.0.9-dev-abcd1234" \
  capture > "$T/c1.out" 2> "$T/c1.err" || bad "capture failed" "$(tail -3 "$T/c1.err")"
want "AF_IMAGE_TAG lands in the capture" "$ENVDIR/env" "AF_IMAGE_TAG=0.0.9-dev-abcd1234"
want "the restore point is recorded as yes" "$ENVDIR/env" "AF_IMAGE_RECOVERABLE=yes"
nowant "no warning when it is complete" "$T/c1.err" "restore point WILL BE LOST"

echo "== 2. workspace is not in GHCR (a tag only re-tagged inside ECR) =="
rm -rf "$T/state"
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234" \
  capture > "$T/c2.out" 2> "$T/c2.err" || bad "capture failed" "$(tail -3 "$T/c2.err")"
want "the restore point is recorded as no" "$ENVDIR/env" "AF_IMAGE_RECOVERABLE=no"
want "it says to take the image out before teardown" "$T/c2.err" "crane copy"
want "it names the tag that would be lost" "$T/c2.err" "0.0.9-dev-abcd1234"

echo "== 3. no crane, so it cannot be measured (do not conclude \"absent\") =="
# Removing the fake is not enough: this Workspace has a real crane installed
# (`~/.local/bin/crane`), and dropping the fake falls through to it — the real one actually
# queries GHCR and answers "absent", so "could not measure (unknown)" turns into "measured
# (no)". Hence PATH is cut down to the minimum, producing a state where crane exists nowhere.
rm -rf "$T/state"
mv "$T/bin/crane" "$T/crane.hidden"
env PATH="$T/bin:/usr/bin:/bin" AF_DEPLOY_STATE_DIR="$AF_DEPLOY_STATE_DIR" AF_TEST_AWS_LOG="$AF_TEST_AWS_LOG" \
  "$HERE/capture-env.sh" --profile dev --region ap-northeast-1 > "$T/c3.out" 2> "$T/c3.err" \
  || bad "capture failed" "$(tail -3 "$T/c3.err")"
want "unknown when it cannot be measured" "$ENVDIR/env" "AF_IMAGE_RECOVERABLE=unknown"
nowant "unknown is not reported as \"will be lost\"" "$T/c3.err" "restore point WILL BE LOST"
mv "$T/crane.hidden" "$T/bin/crane"

echo "== 4. already captured + the deployment moved on -> the refusal says what is stale =="
rm -rf "$T/state"
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234 workspace:0.0.9-dev-abcd1234" capture > /dev/null 2>&1
if AF_TEST_LIVE_TAG="0.1.0-dev-99999999" capture > "$T/c4.out" 2> "$T/c4.err"; then
  bad "the second capture should fail" "it did not fail"
else
  ok "the second capture demands --force and fails"
fi
want "it names the stale captured tag" "$T/c4.err" "AF_IMAGE_TAG=0.0.9-dev-abcd1234"
want "it names the tag that is live now" "$T/c4.err" "0.1.0-dev-99999999"

echo "== 5. re-capturing with --force refreshes it while keeping the marker (AF_DEV_DEPLOY=1) =="
sed -i 's/^AF_DEV_DEPLOY=0/AF_DEV_DEPLOY=1/' "$ENVDIR/env"
AF_TEST_LIVE_TAG="0.1.0-dev-99999999" AF_TEST_GHCR="control-plane:0.1.0-dev-99999999 workspace:0.1.0-dev-99999999" \
  capture --force > "$T/c5.out" 2> "$T/c5.err" || bad "capture --force failed" "$(tail -3 "$T/c5.err")"
want "the tag is refreshed" "$ENVDIR/env" "AF_IMAGE_TAG=0.1.0-dev-99999999"
want "the dev-deployment marker survives" "$ENVDIR/env" "AF_DEV_DEPLOY=1"

echo "== 6. af_env_set replaces exactly one line (it drops no other line) =="
# The function dev-deploy.sh uses to keep the capture in step with every deploy.
# Rewriting the capture wholesale loses information that does not exist on the AWS side,
# such as AF_DEV_DEPLOY, so replacing a single line is the specification itself.
(
  # shellcheck source=deploy/aws/ecs/env.sh
  . "$HERE/env.sh"
  AF_ENV_DIR="$ENVDIR"
  af_env_set AF_IMAGE_TAG "9.9.9-dev-cafe0000"
  af_env_set AF_IMAGE_RECOVERABLE no
  af_env_set AF_BRAND_NEW_KEY 1
)
want "an existing line is replaced" "$ENVDIR/env" "AF_IMAGE_TAG=9.9.9-dev-cafe0000"
want "the other lines remain" "$ENVDIR/env" "AF_DEV_DEPLOY=1"
want "a missing line is appended" "$ENVDIR/env" "AF_BRAND_NEW_KEY=1"
if [ "$(grep -c '^AF_IMAGE_TAG=' "$ENVDIR/env")" = 1 ]; then ok "AF_IMAGE_TAG is still a single line"; else bad "AF_IMAGE_TAG was duplicated"; fi

echo "== 6.5 dev-deploy really calls that function (guard the call site, not just the part) =="
# Case 6 can be green while the caller is gone, and then the capture rots. A reviewer's
# mutation (deleting `af_env_set AF_IMAGE_TAG "$TAG"` from `dev-deploy.sh`, and dropping the
# whole "keep the capture fresh" section) left 44/44 green — this test only reached as far
# as the part. `dev-deploy.sh` needs `gh` / `git ls-remote` / `update.sh`, so it cannot be
# run here; instead of running it, read its source. That is no proof it ran in a real
# deployment — only a real deployment gives that — but it does catch the call disappearing.
DD="$HERE/dev-deploy.sh"
dd_lines="$(wc -l < "$DD")"
# Print the line count that was read, so "0 matches because the file was never read" can be
# told apart from "0 matches although the file is there".
if [ "$dd_lines" -gt 100 ]; then ok "dev-deploy.sh was readable ($dd_lines lines)"; else bad "dev-deploy.sh was not read ($dd_lines lines)"; fi
# It has to be on the success path (the non-DRY side). Landing in the `--dry-run` branch is worthless.
dd_success="$(awk '/^if \[ "\$DRY" = 1 \]; then$/{d=1} /^else$/{if(d){s=1;d=0}} s' "$DD")"
# shellcheck disable=SC2016  # must not expand: what is being looked for is the literal spelling `"$TAG"` inside dev-deploy.sh
if printf '%s' "$dd_success" | grep -q 'af_env_set AF_IMAGE_TAG "\$TAG"'; then
  ok "the success path calls af_env_set AF_IMAGE_TAG"
else
  bad "the success path has no af_env_set AF_IMAGE_TAG call" "the capture is left stale (the three-part trap at the top of this file)"
fi
if printf '%s' "$dd_success" | grep -q 'af_env_set AF_IMAGE_RECOVERABLE'; then
  ok "the success path calls af_env_set AF_IMAGE_RECOVERABLE"
else
  bad "the success path has no af_env_set AF_IMAGE_RECOVERABLE call"
fi
# Negative control: the scan must not have become one that finds anything (run the same scan
# for a spelling that cannot exist).
if printf '%s' "$dd_success" | grep -q 'af_env_set AF_THIS_KEY_DOES_NOT_EXIST'; then
  bad "the scan is broken (it matched a spelling that cannot exist)"
else
  ok "the scan does not match an impossible spelling (negative control)"
fi

echo "== 7. teardown says the restore point will be lost before deleting (no --yes = plan only) =="
rm -rf "$T/state"
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234 workspace:0.0.9-dev-abcd1234" capture > /dev/null 2>&1
# 7-a. complete in GHCR -> it says the deployment can be stood back up
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234 workspace:0.0.9-dev-abcd1234" \
  "$HERE/teardown.sh" --profile dev --region ap-northeast-1 > "$T/t1.out" 2>&1 || true
want "when complete it says it can be stood back up" "$T/t1.out" "is in GHCR too"
nowant "no scaremongering when it is complete" "$T/t1.out" "restore point will be lost"
# 7-b. workspace missing from GHCR -> it says it will be lost, and how to take it out
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234" \
  "$HERE/teardown.sh" --profile dev --region ap-northeast-1 > "$T/t2.out" 2>&1 || true
want "it says it will be lost" "$T/t2.out" "restore point will be lost"
want "it says what to do right now" "$T/t2.out" "crane copy"
# 7-c. the capture is stale (the deployment moved ahead) -> it says so
AF_TEST_LIVE_TAG="0.2.0-dev-11112222" AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234 workspace:0.0.9-dev-abcd1234" \
  "$HERE/teardown.sh" --profile dev --region ap-northeast-1 > "$T/t3.out" 2>&1 || true
want "it says the capture is stale" "$T/t3.out" "capture is stale"
want "it says how to re-capture" "$T/t3.out" "capture-env.sh"
nowant "a plan-only run deletes nothing" "$T/t3.out" "==> 1. stopping the control plane"

echo "== 8. were the fakes actually called (kill \"green because nothing ran\") =="
# Every judgement above is "did this string appear", so a case could go green on a
# coincidental match even if the fake aws / crane were never called once. Hold separate
# evidence that the tools ran.
n_aws="$(wc -l < "$AF_TEST_AWS_LOG")"
n_crane="$(wc -l < "$AF_TEST_CRANE_LOG")"
if [ "$n_aws" -gt 20 ]; then ok "the fake aws was called ($n_aws times)"; else bad "the fake aws was barely called ($n_aws times)"; fi
if [ "$n_crane" -gt 5 ]; then ok "the fake crane was called ($n_crane times)"; else bad "the fake crane was barely called ($n_crane times)"; fi
if grep -q 'manifest .*workspace:' "$AF_TEST_CRANE_LOG"; then ok "GHCR is asked whether workspace exists"; else bad "workspace was never asked about"; fi

echo ""
echo "== 9. syntax (the minimum net that needs no real AWS) =="
for f in "$HERE"/*.sh "$HERE"/harness/*.sh; do
  if bash -n "$f"; then ok "bash -n $(basename "$f")"; else bad "bash -n $(basename "$f")"; fi
done

echo ""
if [ "$FAIL" = 0 ]; then
  echo "all $PASS checks OK"
else
  echo "$FAIL NG (OK $PASS)"
  exit 1
fi
