#!/usr/bin/env bash
# Agent Fleet — does a Persistence=retain deployment actually have its daily EFS backup?
#
# Sourced by update.sh (an upgrade preflight); run on its own it is the same check:
#
#   deploy/aws/ecs/efs-backup-check.sh --profile p --region r [--stack af-ecs-ingress]
#
# Why it exists: the vault and plan are resources of 10-data, added in 0.26.0, and no upgrade
# step redeploys that stack. A data stack stood up earlier keeps its old template for ever, so
# `Persistence=retain` reads as "backed up" while no vault, plan or recovery point exists. Nothing
# else notices. Read-only: describe/list/get calls only, it never changes a stack.
#
# The decision is a pure function (af_efs_backup_verdict) so the stub test can walk every branch
# without AWS; af_efs_backup_check gathers the facts and prints.

# af_efs_backup_verdict <persistence> <vault-name> <efs-arn> <selected-resources> [<cp-efs-arn>]
# Prints one of: skip | ok | no-vault | no-plan | unknown.
#   <persistence>         the data stack's Persistence parameter; "" when it could not be read.
#   <vault-name>          the data stack's EfsBackupVaultName output ("" when absent);
#                         "UNREADABLE" when the stack could not be read at all.
#   <efs-arn>             the exact ARN of the stack's file system (partition, region, account).
#   <selected-resources>  whitespace-separated (TAB from the CLI) Resources of every plain
#                         selection of the plan, plus the token COMPLEX for a selection that
#                         has NotResources, Conditions or ListOfTags (not evaluated here);
#                         "" when there is none; "UNREADABLE" when a read failed.
#   <cp-efs-arn>          the Control Plane's own file system (CpDataEfsId, #4089), which must be
#                         selected too; "" for a data stack older than it (only <efs-arn> is
#                         required then); "UNREADABLE" when its output could not be read.
# Only an exact ARN match counts; a wildcard resource is COMPLEX (unevaluated), not a miss. When in doubt this answers unknown, never ok: a missing
# permission, or a selection this cannot evaluate, must not read as protected.
af_efs_backup_verdict() {
  local persistence="$1" vault="$2" arn="$3" sel="$4" cp="${5:-}" tok complex=0 want found missing=0
  if [ "$vault" = UNREADABLE ] || [ -z "$persistence" ]; then echo unknown; return 0; fi
  if [ "$persistence" != retain ]; then echo skip; return 0; fi
  if [ -z "$vault" ]; then echo no-vault; return 0; fi
  if [ "$sel" = UNREADABLE ] || [ -z "$arn" ] || [ "$cp" = UNREADABLE ]; then echo unknown; return 0; fi
  # shellcheck disable=SC2086  # word splitting on any whitespace is the tokenising
  for tok in $sel; do
    # A pattern is not evaluated, and it may well cover a file system: unknown, not "missing".
    case "$tok" in COMPLEX|*[\*\?]*) complex=1 ;; esac
  done
  # Every required file system needs its exact ARN; one of them covered does not cover the other.
  for want in $arn $cp; do
    found=0
    # shellcheck disable=SC2086
    for tok in $sel; do [ "$tok" = "$want" ] && found=1; done
    [ "$found" = 1 ] || missing=1
  done
  if [ "$missing" = 0 ]; then echo ok
  elif [ "$complex" = 1 ]; then echo unknown
  else echo no-plan; fi
}

# af_efs_backup_marker <verdict>
# Prints the value for the ingress stack's EfsBackup parameter (AF_EFS_BACKUP in the Control
# Plane, which warns in the Console on "missing"): ok -> present, skip -> off, no-vault|no-plan
# -> missing. Prints nothing for unknown (and returns 1): the caller must then NOT pass the
# parameter, so a failed read keeps the previous value instead of raising or clearing a warning.
af_efs_backup_marker() {
  case "$1" in
    ok) echo present ;;
    skip) echo off ;;
    no-vault|no-plan) echo missing ;;
    *) return 1 ;;
  esac
}

# af_efs_backup_data_stack <ingress-stack>
# Prints the data stack's name. Returns 1 when the ingress stack cannot be read: the caller
# must then say "unknown" and must NOT fall back to the default name, which could be another
# deployment's stack. A readable stack without the parameter means the default.
af_efs_backup_data_stack() {
  local v
  v="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$1" \
    --query "Stacks[0].Parameters[?ParameterKey=='DataStackName'].ParameterValue" --output text 2>/dev/null)" || return 1
  case "$v" in None|"") v=af-ecs-data ;; esac
  echo "$v"
}

# af_efs_backup_check <data-stack|""> <profile> <region>
# Needs the caller's AWS array (update.sh and env.sh set it). Sets AF_EFS_BACKUP_VERDICT and
# prints a warning with the fix on anything but ok / skip. Always returns 0: it advises, the
# upgrade is not blocked by it. An empty <data-stack> means the name could not be determined.
af_efs_backup_check() {
  local data="$1" profile="$2" region="$3"
  local persistence="" vault="" efs="" sid_arn="" arn="" plan="" sel_ids sid got extra res="" rc=0
  local cp_efs="" cp_arn=""
  local part acct
  if [ -z "$data" ]; then
    persistence=""
  else
    persistence="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$data" \
      --query "Stacks[0].Parameters[?ParameterKey=='Persistence'].ParameterValue" --output text 2>/dev/null)" || persistence=""
    case "$persistence" in None) persistence="" ;; esac
  fi
  if [ "$persistence" = retain ]; then
    vault="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$data" \
      --query "Stacks[0].Outputs[?OutputKey=='EfsBackupVaultName'].OutputValue" --output text 2>/dev/null)" || vault=UNREADABLE
    case "$vault" in None) vault="" ;; esac
    efs="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$data" \
      --query "Stacks[0].Outputs[?OutputKey=='EfsId'].OutputValue" --output text 2>/dev/null)" || efs=""
    case "$efs" in None) efs="" ;; esac
    sid_arn="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$data" \
      --query "Stacks[0].StackId" --output text 2>/dev/null)" || sid_arn=""
    # arn:<partition>:cloudformation:<region>:<account>:stack/<name>/<uuid>
    part="$(echo "$sid_arn" | cut -d: -f2)"; acct="$(echo "$sid_arn" | cut -d: -f5)"
    [ -n "$efs" ] && [ -n "$part" ] && [ -n "$acct" ] \
      && arn="arn:$part:elasticfilesystem:$region:$acct:file-system/$efs"
    # The Control Plane's own file system (#4089). No output = a data stack older than it, which
    # is judged on EfsId alone; a failed read must not drop the requirement, so it is unknown.
    if cp_efs="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$data" \
      --query "Stacks[0].Outputs[?OutputKey=='CpDataEfsId'].OutputValue" --output text 2>/dev/null)"; then
      case "$cp_efs" in None) cp_efs="" ;; esac
      if [ -n "$cp_efs" ]; then
        if [ -n "$part" ] && [ -n "$acct" ]; then
          cp_arn="arn:$part:elasticfilesystem:$region:$acct:file-system/$cp_efs"
        else
          cp_arn=UNREADABLE
        fi
      fi
    else
      cp_arn=UNREADABLE
    fi
  fi
  if [ -n "$vault" ] && [ "$vault" != UNREADABLE ]; then
    if plan="$("${AWS[@]}" cloudformation describe-stack-resources --stack-name "$data" \
      --logical-resource-id EfsBackupPlan --query 'StackResources[0].PhysicalResourceId' \
      --output text 2>/dev/null)"; then
      case "$plan" in None) plan="" ;; esac
    else
      plan=UNREADABLE
    fi
    if [ "$plan" = UNREADABLE ]; then
      res=UNREADABLE
    elif [ -n "$plan" ]; then
      sel_ids="$("${AWS[@]}" backup list-backup-selections --backup-plan-id "$plan" \
        --query 'BackupSelectionsList[].SelectionId' --output text 2>/dev/null)" || rc=1
      for sid in $sel_ids; do
        [ "$sid" = None ] && continue
        # Conditions counts the condition entries, not its keys: AWS returns all four operator
        # keys with empty lists on a selection that has no conditions, so counting the keys
        # reads every real selection as COMPLEX and the verdict can never be ok.
        # shellcheck disable=SC2016  # backticks are JMESPath literals, not a subshell
        extra="$("${AWS[@]}" backup get-backup-selection --backup-plan-id "$plan" --selection-id "$sid" \
          --query 'BackupSelection.[length(NotResources||`[]`),length(Conditions.*[]||`[]`),length(ListOfTags||`[]`)]' \
          --output text 2>/dev/null)" || { rc=1; continue; }
        if [ "$extra" != "$(printf '0\t0\t0')" ]; then res="$res COMPLEX"; continue; fi
        got="$("${AWS[@]}" backup get-backup-selection --backup-plan-id "$plan" --selection-id "$sid" \
          --query 'BackupSelection.Resources' --output text 2>/dev/null)" || { rc=1; continue; }
        res="$res $got"
      done
      [ "$rc" = 0 ] || res=UNREADABLE
    fi
  fi
  AF_EFS_BACKUP_VERDICT="$(af_efs_backup_verdict "$persistence" "$vault" "$arn" "$res" "$cp_arn")"
  case "$AF_EFS_BACKUP_VERDICT" in
    skip) echo "==> EFS backup: $data runs Persistence=${persistence:-delete} — no EFS backup is expected" ;;
    ok)   echo "==> EFS backup: $data has vault $vault and a plan selecting its file system${cp_arn:+s (the members and the Control Plane)}" ;;
    unknown)
      {
        echo "WARNING: could not confirm the daily EFS backup of ${data:-the data stack (its name could not be read)}"
        echo "         (a stack or AWS Backup read failed, or the plan's selection has NotResources/Conditions/tags"
        echo "         this check does not evaluate — missing backup:List*/Get* or cloudformation:Describe*?). Check by hand:"
        echo "           aws backup list-backup-plans --query \"BackupPlansList[].BackupPlanName\" --profile $profile --region $region"
      } >&2 ;;
    no-vault|no-plan)
      {
        echo "WARNING: $data is Persistence=retain but has NO daily EFS backup ($AF_EFS_BACKUP_VERDICT)."
        echo "         The backup vault and plan were added to 10-data in 0.26.0; a data stack stood up earlier"
        echo "         never got them, and nothing else updates that stack. Every member's Claude state and"
        echo "         home on the EFS is unprotected against deletion or corruption. This upgrade does not"
        echo "         fix it — do it with a change set (guide/operate/aws-ecs/backup.md, \"Turn backups on for a running deployment\"):"
        echo "           aws cloudformation deploy --stack-name $data --template-file $(dirname "${BASH_SOURCE[0]}")/cfn/10-data.yaml \\"
        echo "             --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM --no-execute-changeset \\"
        echo "             --profile $profile --region $region"
        if [ "$AF_EFS_BACKUP_VERDICT" = no-plan ]; then
          echo "         The plan does not select ${arn:-the members file system}${cp_arn:+ and $cp_arn (the Control Plane) together}:"
          echo "         every file system it names must be listed by its exact ARN."
        fi
        # What a correct change set looks like depends on what is missing: a stack without the
        # vault gains the four EfsBackup* resources; one with a plan only rewrites its selection.
        if [ "$AF_EFS_BACKUP_VERDICT" = no-vault ]; then
          echo "         Read the change set (describe-change-set) and execute it only if it adds the four"
          echo "         EfsBackup* resources,"
        else
          echo "         Read the change set (describe-change-set) and execute it only if it modifies (or"
          echo "         replaces) EfsBackupSelection,"
        fi
        echo "         plus the five CpData* resources on a stack older than the Control Plane's file system"
        echo "         (copy its readable internal git repositories out first: guide/operate/aws-ecs/upgrade.md,"
        echo "         \"The Control Plane's data root\"), and nothing else — never a Modify/Replacement of FileSystem or Db."
        echo "         guide/operate/aws-ecs/backup.md, \"Turn backups on for a running deployment\"."
      } >&2 ;;
  esac
  return 0
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  set -euo pipefail
  PROFILE=""; REGION=""; STACK="af-ecs-ingress"
  while [ $# -gt 0 ]; do
    case "$1" in
      --profile) PROFILE="${2:?--profile needs a value}"; shift ;;
      --region)  REGION="${2:?--region needs a value}"; shift ;;
      --stack)   STACK="${2:?--stack needs a value}"; shift ;;
      -h|--help) echo "usage: efs-backup-check.sh --profile <p> --region <r> [--stack <ingress stack>]" >&2; exit 0 ;;
      *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
    shift
  done
  if [ -z "$PROFILE" ] || [ -z "$REGION" ]; then
    echo "usage: efs-backup-check.sh --profile <p> --region <r> [--stack <ingress stack>]" >&2
    exit 2
  fi
  AWS=(aws --profile "$PROFILE" --region "$REGION")
  DATA="$(af_efs_backup_data_stack "$STACK")" || DATA=""
  af_efs_backup_check "$DATA" "$PROFILE" "$REGION"
  case "$AF_EFS_BACKUP_VERDICT" in ok|skip) exit 0 ;; *) exit 1 ;; esac
fi
