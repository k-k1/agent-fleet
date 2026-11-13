#!/usr/bin/env bash
# efs-backup-check.sh (the upgrade preflight behind update.sh), against a stubbed `aws`.
#
#   deploy/local/efs-backup-check-stub-test.sh
#
# Pinned: the DECISION. A data stack stood up before 0.26.0 has no EFS backup vault or plan and
# nothing else says so (#2063), so the cases that matter are the ones that must WARN
# (no-vault, no-plan) and the ones that must not claim protection they could not verify
# (unknown). The "ok" and "skip" cases keep the warning honest: a check that always warns is
# one nobody reads.
# shellcheck disable=SC2016  # case 7 greps for literal source text, not expansions
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
CHECK="$ROOT/deploy/aws/ecs/efs-backup-check.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; LOG="$WORK/calls.log"
mkdir -p "$STUB"

# Knobs (environment): STUB_PERSISTENCE, STUB_VAULT, STUB_EFS, STUB_PLAN, STUB_SELECTIONS (ids),
# STUB_RESOURCES (get-backup-selection Resources), STUB_EXTRA (NotResources/Conditions/tags
# lengths, TAB separated), STUB_BACKUP_DENIED=1, STUB_PLAN_DENIED=1, STUB_INGRESS_DENIED=1,
# STUB_CP_EFS (CpDataEfsId; unset = a data stack older than it), STUB_CP_DENIED=1.
#
# The stub is an allowlist: after the global --profile/--region are stripped (and checked), the
# remaining arguments must match one known read call EXACTLY, query included. Anything else is
# logged as UNKNOWN and fails the test, so a mistyped query or a write call cannot pass.
cat > "$STUB/aws" <<'FAKE'
#!/usr/bin/env bash
[ "$1 $2 $3 $4" = "--profile p --region ap-northeast-1" ] || { echo "UNKNOWN globals: $*" >> "$STUB_LOG"; exit 99; }
shift 4
echo "aws $*" >> "$STUB_LOG"
DS="cloudformation describe-stacks --stack-name"
Q="--query"
case "$*" in
  "$DS t-ingress $Q Stacks[0].Parameters[?ParameterKey=='DataStackName'].ParameterValue --output text")
    [ -z "${STUB_INGRESS_DENIED:-}" ] || { echo "AccessDenied" >&2; exit 254; }
    echo "${STUB_DATA_NAME:-custom-data}" ;;
  "$DS custom-data $Q Stacks[0].Parameters[?ParameterKey=='Persistence'].ParameterValue --output text")
    echo "${STUB_PERSISTENCE:-retain}" ;;
  "$DS custom-data $Q Stacks[0].Outputs[?OutputKey=='EfsBackupVaultName'].OutputValue --output text")
    echo "${STUB_VAULT:-None}" ;;
  "$DS custom-data $Q Stacks[0].Outputs[?OutputKey=='EfsId'].OutputValue --output text")
    echo "${STUB_EFS:-fs-0abc}" ;;
  "$DS custom-data $Q Stacks[0].Outputs[?OutputKey=='CpDataEfsId'].OutputValue --output text")
    [ -z "${STUB_CP_DENIED:-}" ] || { echo "Throttling" >&2; exit 254; }
    echo "${STUB_CP_EFS:-None}" ;;
  "$DS custom-data $Q Stacks[0].StackId --output text")
    echo "arn:aws:cloudformation:ap-northeast-1:111122223333:stack/custom-data/0000" ;;
  "cloudformation describe-stack-resources --stack-name custom-data --logical-resource-id EfsBackupPlan $Q StackResources[0].PhysicalResourceId --output text")
    [ -z "${STUB_PLAN_DENIED:-}" ] || { echo "AccessDenied" >&2; exit 254; }
    echo "${STUB_PLAN:-None}" ;;
  "backup list-backup-selections --backup-plan-id plan-1 $Q BackupSelectionsList[].SelectionId --output text")
    [ -z "${STUB_BACKUP_DENIED:-}" ] || { echo "AccessDenied" >&2; exit 254; }
    echo "${STUB_SELECTIONS:-None}" ;;
  "backup get-backup-selection --backup-plan-id plan-1 --selection-id sel-1 $Q BackupSelection.[length(NotResources||\`[]\`),length(Conditions.*[]||\`[]\`),length(ListOfTags||\`[]\`)] --output text")
    [ -z "${STUB_BACKUP_DENIED:-}" ] || { echo "AccessDenied" >&2; exit 254; }
    printf '%b\n' "${STUB_EXTRA:-0\t0\t0}" ;;
  "backup get-backup-selection --backup-plan-id plan-1 --selection-id sel-1 $Q BackupSelection.Resources --output text")
    [ -z "${STUB_BACKUP_DENIED:-}" ] || { echo "AccessDenied" >&2; exit 254; }
    printf '%b\n' "${STUB_RESOURCES:-}" ;;
  *) echo "UNKNOWN call: $*" >> "$STUB_LOG"; echo "stub: unknown call: $*" >&2; exit 99 ;;
esac
exit 0
FAKE
chmod +x "$STUB/aws"
export PATH="$STUB:$PATH" STUB_LOG="$LOG"

fail() { echo "NG: $1"; echo "--- out ---"; cat "$WORK/out" 2>/dev/null; echo "--- log ---"; cat "$LOG"; exit 1; }
says() { grep -qF -- "$1" "$WORK/out" || fail "not printed: $1"; }
saysnt() { ! grep -qF -- "$1" "$WORK/out" || fail "must not be printed: $1"; }
# check <expected exit> [script]  -- knobs come from the environment
check() {
  local want="$1" script="${2:-$CHECK}" got=0
  : > "$LOG"
  "$script" --profile p --region ap-northeast-1 --stack t-ingress > "$WORK/out" 2>&1 || got=$?
  [ "$got" = "$want" ] || fail "exit $got, wanted $want"
  ! grep -q UNKNOWN "$LOG" || fail "the script made a call outside the read-only allowlist"
  # belt and braces: no verb that writes, whatever the global options in front of it
  ! grep -Eq '^aws [a-z0-9-]+ (create|update|delete|execute|put|start|deploy)[-a-z]*( |$)' "$LOG" || fail "a write verb was issued"
}
ARN="arn:aws:elasticfilesystem:ap-northeast-1:111122223333:file-system/fs-0abc"
TAB=$'\t'

echo "== verdict: the pure decision =="
# shellcheck source=deploy/aws/ecs/efs-backup-check.sh
. "$CHECK"
A=arn:aws:elasticfilesystem:r:111122223333:file-system
v() { [ "$(af_efs_backup_verdict "$@")" = "$WANT" ] || fail "verdict($*) = $(af_efs_backup_verdict "$@"), wanted $WANT"; }
WANT=skip;     v delete "" "$A/fs-1" ""
WANT=no-vault; v retain "" "$A/fs-1" ""
WANT=no-plan;  v retain vault "$A/fs-1" ""
WANT=no-plan;  v retain vault "$A/fs-1" "$A/fs-other"
WANT=ok;       v retain vault "$A/fs-1" "$A/fs-1"
WANT=ok;       v retain vault "$A/fs-1" "$A/fs-other$TAB$A/fs-1"
WANT=no-plan;  v retain vault "$A/fs-1" "$A/fs-12"
# another region/account/partition is another file system
WANT=no-plan;  v retain vault "$A/fs-1" "arn:aws:elasticfilesystem:us-east-1:111122223333:file-system/fs-1"
# a wildcard is not evaluated: unknown (it may cover the file system), never ok or missing
WANT=unknown;  v retain vault "$A/fs-1" "arn:aws:elasticfilesystem:*:*:file-system/*"
WANT=unknown;  v retain vault "$A/fs-1" "$A/*"
WANT=ok;       v retain vault "$A/fs-1" "$A/* $A/fs-1"
WANT=unknown;  v retain vault "$A/fs-1" "COMPLEX"
WANT=ok;       v retain vault "$A/fs-1" "COMPLEX $A/fs-1"
WANT=unknown;  v retain vault "$A/fs-1" UNREADABLE
WANT=unknown;  v retain vault "" ""
WANT=unknown;  v retain UNREADABLE "$A/fs-1" ""
WANT=unknown;  v "" "" "" ""
# The Control Plane's file system (#4089): both ARNs must be selected; absent = older stack.
WANT=ok;       v retain vault "$A/fs-1" "$A/fs-1$TAB$A/fs-cp" "$A/fs-cp"
WANT=ok;       v retain vault "$A/fs-1" "$A/fs-1" ""
WANT=no-plan;  v retain vault "$A/fs-1" "$A/fs-1" "$A/fs-cp"
WANT=no-plan;  v retain vault "$A/fs-1" "$A/fs-cp" "$A/fs-cp"
WANT=unknown;  v retain vault "$A/fs-1" "$A/fs-1" UNREADABLE
WANT=unknown;  v retain vault "$A/fs-1" "$A/fs-1 $A/*" "$A/fs-cp"
WANT=skip;     v delete "" "$A/fs-1" "" UNREADABLE

echo "== case 1: Persistence=delete -- nothing expected, nothing said =="
STUB_PERSISTENCE=delete check 0
says "no EFS backup is expected"; saysnt "WARNING"

echo "== case 2: retain, pre-0.26.0 data stack (no vault output) -- warns with the fix =="
STUB_VAULT=None check 1
says "NO daily EFS backup (no-vault)"
says "--no-execute-changeset"
says "never a Modify/Replacement of FileSystem or Db"
saysnt "execute-change-set"

echo "== case 3: retain, vault but the plan selects nothing -- warns =="
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=None check 1
says "(no-plan)"

echo "== case 4: retain, plan selects another file system / region -- warns =="
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="arn:aws:elasticfilesystem:ap-northeast-1:111122223333:file-system/fs-other" check 1
says "(no-plan)"
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="arn:aws:elasticfilesystem:us-east-1:111122223333:file-system/fs-0abc" check 1
says "(no-plan)"

echo "== case 5: retain, plan selects this EFS -- ok (also among several, TAB separated) =="
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$ARN" check 0
says "has vault v and a plan selecting"; saysnt "WARNING"
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$ARN\tarn:other:file-system/fs-9" check 0
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="arn:other:file-system/fs-9\t$ARN" check 0

echo "== case 5b: a data stack with the Control Plane's file system needs both selected (#4089) =="
CPARN="arn:aws:elasticfilesystem:ap-northeast-1:111122223333:file-system/fs-0cp"
STUB_CP_EFS=fs-0cp STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$ARN\t$CPARN" check 0
says "the members and the Control Plane"; saysnt "WARNING"
# the home file system alone no longer reads as protected
STUB_CP_EFS=fs-0cp STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$ARN" check 1
says "(no-plan)"; says "$CPARN"; saysnt "has vault"
STUB_CP_EFS=fs-0cp STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$CPARN" check 1
says "(no-plan)"
# its output unreadable: unknown, never ok on the home file system alone
STUB_CP_DENIED=1 STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$ARN\t$CPARN" check 1
says "could not confirm"; saysnt "has vault"; saysnt "NO daily"
# the fix it prints names the CpData* additions as expected, still never FileSystem or Db
STUB_VAULT=None check 1
says "five CpData* resources"

echo "== case 6: AWS Backup / plan lookup not readable -- never reads as protected, never as missing =="
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_BACKUP_DENIED=1 check 1
says "could not confirm"; saysnt "has vault"; saysnt "NO daily"
STUB_VAULT=v STUB_PLAN=plan-1 STUB_PLAN_DENIED=1 check 1
says "could not confirm"; saysnt "NO daily"

echo "== case 7: a selection with NotResources / Conditions / tags is not evaluated -- unknown =="
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$ARN" STUB_EXTRA='1\t0\t0' check 1
says "could not confirm"; saysnt "has vault"
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$ARN" STUB_EXTRA='0\t1\t0' check 1
says "could not confirm"
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$ARN" STUB_EXTRA='0\t0\t1' check 1
says "could not confirm"

echo "== case 7b: a wildcard Resources selection may cover the EFS -- unknown, no false alarm =="
STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 \
  STUB_RESOURCES="arn:aws:elasticfilesystem:ap-northeast-1:111122223333:file-system/*" check 1
says "could not confirm"; saysnt "NO daily"; saysnt "has vault"

echo "== case 8: the ingress stack is unreadable -- unknown, and no other stack is asked =="
# The default name af-ecs-data may be a different deployment; the allowlist stub fails on any
# describe-stacks of it, and the custom stack's data must not be consulted either.
STUB_INGRESS_DENIED=1 check 1
says "could not confirm"; saysnt "no EFS backup is expected"
! grep -q 'af-ecs-data\|custom-data' "$LOG" || fail "a data stack was queried although its name is unknown"

echo "== case 9: update.sh runs the preflight, and reminds at the end =="
UPD="$ROOT/deploy/aws/ecs/update.sh"
grep -q '^\. "\$HERE/efs-backup-check.sh"$' "$UPD" || fail "update.sh does not source efs-backup-check.sh"
grep -q 'af_efs_backup_data_stack "\$STACK"' "$UPD" || fail "update.sh does not use the strict data-stack read"
grep -q 'af_efs_backup_check "\$DATA_STACK"' "$UPD" || fail "update.sh does not call af_efs_backup_check"
grep -q 'REMINDER: ' "$UPD" || fail "update.sh has no end-of-run reminder"

echo "== case 10: the verdict reaches the Console as the ingress EfsBackup parameter =="
m() { [ "$(af_efs_backup_marker "$1" || true)" = "$2" ] || fail "marker($1) = $(af_efs_backup_marker "$1" || true), wanted $2"; }
m ok present; m skip off; m no-vault missing; m no-plan missing
# unknown passes nothing, so a failed read cannot raise or clear the warning
m unknown ""; m "" ""
af_efs_backup_marker unknown && fail "marker(unknown) returned 0"
grep -q 'af_efs_backup_marker "\$AF_EFS_BACKUP_VERDICT"' "$UPD" || fail "update.sh does not derive the EfsBackup marker"
grep -q '"EfsBackup=\$EFS_BACKUP_MARKER"' "$UPD" || fail "update.sh does not pass EfsBackup to the ingress deploy"
grep -q 'af_param_override EfsBackup' "$ROOT/deploy/aws/ecs/standup.sh" || fail "standup.sh does not pass EfsBackup"
ING="$ROOT/deploy/aws/ecs/cfn/30-ingress.yaml"
grep -q 'AllowedValues: \[ "", "off", present, missing \]' "$ING" || fail "30-ingress EfsBackup values drifted from the CP's"
grep -q 'Name: AF_EFS_BACKUP, Value: !Ref EfsBackup' "$ING" || fail "30-ingress does not hand EfsBackup to the CP"
# CloudFormation parses YAML 1.1, where an unquoted off is the boolean false and the parameter
# would then refuse the very value the scripts pass. Parse the way it does: all strings, all four.
python3 - "$ING" <<'PY' || fail "30-ingress EfsBackup AllowedValues do not parse as the four strings"
import sys, yaml
class L(yaml.SafeLoader): pass
L.add_multi_constructor("!", lambda l, s, n: None)
v = yaml.load(open(sys.argv[1]), L)["Parameters"]["EfsBackup"]["AllowedValues"]
sys.exit(0 if v == ["", "off", "present", "missing"] and all(type(x) is str for x in v) else 1)
PY
# A new parameter must not be mandatory for existing stacks.
awk '/^  EfsBackup:/{f=1} f&&/Default: ""/{ok=1} f&&/^  CustodianKmsKeyArn:/{exit} END{exit !ok}' "$ING" || fail "EfsBackup has no empty default"

echo "== negative controls: the harness itself must go red on a broken script =="
# Each mutant is a copy of the real script with one defect; check 0 is wrong for all of them,
# so a harness that cannot see the defect would let them through.
mutant() {  # mutant <name> <sed expr>
  sed "$2" "$CHECK" > "$WORK/$1.sh"; chmod +x "$WORK/$1.sh"
  cmp -s "$CHECK" "$WORK/$1.sh" && { echo "NG: mutant $1 changed nothing"; exit 1; }
  return 0
}
expect_red() {  # expect_red <name>: the same ok scenario must NOT stay green
  local got=0
  ( STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$ARN" check 0 "$WORK/$1.sh" ) > "$WORK/mut.out" 2>&1 || got=$?
  [ "$got" != 0 ] || { echo "NG: mutant $1 was not caught"; exit 1; }
}
mutant typo 's/BackupSelection\.Resources/BackupSelection.Typo/'
expect_red typo
mutant write 's/^  AF_EFS_BACKUP_VERDICT="\$(af_efs_backup_verdict/  "${AWS[@]}" cloudformation delete-stack --stack-name "$data"\n&/'
expect_red write
mutant region 's/elasticfilesystem:\$region:/elasticfilesystem:us-east-1:/'
expect_red region
# The Control Plane's file system dropped from the verdict: the home-only selection goes green.
mutant cponly 's/"\$res" "\$cp_arn")/"$res")/'
got=0
( STUB_CP_EFS=fs-0cp STUB_VAULT=v STUB_PLAN=plan-1 STUB_SELECTIONS=sel-1 STUB_RESOURCES="$ARN" check 1 "$WORK/cponly.sh" ) \
  > "$WORK/mut.out" 2>&1 || got=$?
[ "$got" != 0 ] || { echo "NG: mutant cponly was not caught"; exit 1; }

echo "OK: the EFS backup preflight decides correctly"
