#!/usr/bin/env bash
# One GitHub issue per CLI, open only while a person has something to do about it.
#
#   cli-update-issues.sh sync [kind...]     open, rewrite or close the issues (default: every row)
#   cli-update-issues.sh number <kind>      the open issue's number, or nothing
#
# ## The contract with the reader
#
# The issue's title is the next step ("kiro CLI: run the contract on 2.30.0"), and its body
# opens with the one thing to do, as a command where there is one. Every path ends in a PR
# that closes the issue: the pin-bump PR (`cli-pin-bump.yml` writes `Closes #N` for each
# kind it bumps), a fix PR, or a hand bump. A reader never has to work out which of
# several issues, or which row of a table, is theirs.
#
# An issue is OPENED only for a state a person must act on:
#
#   red         the contract failed on latest (a row in the issue's `Red contracts` section)
#   streak      the contract failed its last 2 runs on main, whatever the version
#   dispatch    cursor / kiro: the credential rotates, so the watcher never runs them
#   credential  claude / copilot / agy while their CI secret is not configured
#   manual      rtk: no contract exists, it is bumped by hand
#   fragments   the latest release no longer contains upstream text Agent Fleet matches on
#               (a row in `Red contracts`, written by cli-pin-bump.sh via cli-fragment-check.sh)
#
# An issue that is already open is kept, with a new title, through `merge` (latest passed
# its contract: merge the bump PR, which closes it). It is closed when nothing is left for
# a person: the pin equals latest with no red row, or an unattended kind went back to
# waiting for the watcher (a newer release replaced the red one).
#
# The pin-drift steady state (a CLI moved, its contract is running or passed and the bump
# PR is open) opens nothing: the bump PR is the whole interaction there.
#
# ## Inputs
#
#   DRIFT_CHECK    the pin / latest source (default: cli-drift-check.sh)
#   RELEASE_STATE  the `tested` source (default: cli-release-state.sh)
#   REPORT         the red-contracts section owner (default: cli-contract-report.sh)
#   CRED_CLAUDE, CRED_COPILOT, CRED_AGY
#                  `false` when that contract's CI secret is missing. Anything else,
#                  including unset, means "the watcher can run it": a contract job calling
#                  in cannot see the other secrets, and when in doubt this must not open an
#                  issue asking for a hand probe.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DRIFT_CHECK="${DRIFT_CHECK:-$HERE/cli-drift-check.sh}"
RELEASE_STATE="${RELEASE_STATE:-$HERE/cli-release-state.sh}"
REPORT="${REPORT:-$HERE/cli-contract-report.sh}"
LABEL=cli-drift
BUMP_BRANCH=automation/cli-pin-bump
ALL_KINDS=(claude codex opencode copilot agy cursor kiro muse rtk)

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

valid_kind() { [[ "$1" =~ ^[a-z0-9-]+$ ]] || { echo "invalid kind: $1" >&2; exit 2; }; }

# The title prefix is the key. Only issues the workflows opened count: the repository is
# public, and anyone can open an issue with the same title.
number() {
  gh issue list --state open --label "$LABEL" --limit 100 --json number,title,author \
    --jq "[.[] | select((.title | startswith(\"$1 CLI: \")) and .author.login == \"app/github-actions\") | .number] | first // empty"
}

workflow_of() { case "$1" in claude) echo claude-tui-contract.yml ;; *) echo "$1-contract.yml" ;; esac; }

dispatch_cmd() {
  local wf; wf="$(workflow_of "$1")"
  case "$1" in
    codex|opencode) echo "gh workflow run $wf --ref main" ;;
    muse)           echo "gh workflow run $wf --ref main -f version=latest" ;;
    *)              echo "gh workflow run $wf --ref main -f cli_version=latest" ;;
  esac
}

# Markdown for the issue body; the backticks are literal.
# shellcheck disable=SC2016
secret_of() {
  case "$1" in
    claude)  echo '`E2E_CLAUDE_OAUTH_TOKEN` or `E2E_ANTHROPIC_API_KEY`' ;;
    copilot) echo '`E2E_COPILOT_GITHUB_TOKEN`' ;;
    agy)     echo '`E2E_AGY_OAUTH_TOKEN`' ;;
    cursor)  echo '`E2E_CURSOR_AUTH_JSON`' ;;
    kiro)    echo '`E2E_KIRO_AUTH_DB_B64_1..8` (`deploy/local/set-kiro-ci-auth.sh`)' ;;
  esac
}

# The open bump PR that carries <kind> at <latest>, read from its evidence table row
# (`| kiro | `2.29.1` → `2.30.0` | …`). Best effort: a token without pull-requests:read
# only loses the PR number from the sentence.
bump_pr_for() {
  gh pr list --head "$BUMP_BRANCH" --state open --json number,body 2>/dev/null |
    jq -r --arg k "$1" --arg v "$2" \
      '[.[] | select(.body | split("\n") | any(startswith("| " + $k + " | `") and contains("` → `" + $v + "` |"))) | .number] | first // empty' 2>/dev/null || true
}

# --- one kind ------------------------------------------------------------------------

sync_kind() { # sync_kind <kind> <pin> <latest>
  local kind="$1" pin="$2" latest="$3" num="" red_ver="" streak=false state="" tested="" cred
  # KNOWN_ISSUE: the number of an issue created moments ago by cli-contract-report.sh,
  # which the list lookup may not show yet; without it a second issue would open.
  num="${KNOWN_ISSUE:-}"
  [[ "$num" =~ ^[0-9]+$ ]] || num="$(number "$kind")"
  : > "$WORK/rows"
  if [ -n "$num" ]; then
    gh issue view "$num" --json body --jq .body |
      sed -n '/^<!-- cli-contract-failures:begin -->$/,/^<!-- cli-contract-failures:end -->$/{/^- /p}' > "$WORK/rows"
  fi
  red_ver="$(sed -n "s/^- \`$kind\` \`\([^\`]*\)\`.*<!-- cli-contract-failure $kind -->.*/\1/p" "$WORK/rows" | tail -1)"
  # An `unknown` row (a job that died before reading the version) replaces the kind's last
  # row but says nothing about latest; it must not hide the marker below.
  [ "$red_ver" != unknown ] || red_ver=""
  # The watcher's `red` marker says the same thing without a row: a failure recorded before
  # this issue existed, or a row a concurrent rewrite dropped. Never ignore it — the marker
  # also stops the watcher from running the contract again, so nothing else would ask.
  [ -n "$red_ver" ] || [ "$kind" = rtk ] || red_ver="$("$RELEASE_STATE" get red "$kind")"
  grep -q "<!-- cli-contract-streak $(workflow_of "$kind" | sed 's/\.yml$//') -->" "$WORK/rows" && streak=true
  [ "$kind" = rtk ] || tested="$("$RELEASE_STATE" get tested "$kind")"
  eval "cred=\${CRED_$(printf '%s' "$kind" | tr '[:lower:]-' '[:upper:]_'):-}"

  if [ -n "$red_ver" ] && [ "$red_ver" = "$latest" ]; then state=red
  elif [ "$streak" = true ]; then state=streak
  elif [ "$pin" = "$latest" ]; then state=current
  elif [ "$kind" = rtk ]; then state=manual
  elif [ "$tested" = "$latest" ]; then state=merge
  elif [ "$kind" = cursor ] || [ "$kind" = kiro ]; then state=dispatch
  elif [ "$cred" = false ] && case "$kind" in claude|copilot|agy) true ;; *) false ;; esac; then state=credential
  else state=waiting
  fi

  # A gone fragment outranks "the watcher will handle it" and "merge the bump PR": the
  # contract passing says nothing about wording, and merging is the moment to look.
  if [ "$state" = waiting ] || [ "$state" = merge ]; then
    local frag_ver
    frag_ver="$(sed -n "s/^- \`$kind\` \`\([^\`]*\)\`.*<!-- cli-contract-fragments $kind -->.*/\1/p" "$WORK/rows" | tail -1)"
    [ "$frag_ver" != "$latest" ] || state=fragments
  fi

  # An advisory caps finding for latest keeps the issue open: the probes gate nothing, so
  # this is the only place a person would see it. Cleared by a passing probe or a newer release.
  if [ "$state" = current ] || [ "$state" = waiting ]; then
    local caps_ver
    caps_ver="$(sed -n "s/^- \`$kind\` \`\([^\`]*\)\`.*<!-- cli-contract-caps $kind -->.*/\1/p" "$WORK/rows" | tail -1)"
    [ "$caps_ver" != "$latest" ] || state=caps
  fi

  case "$state" in
    current|waiting)
      [ -n "$num" ] || return 0
      local why="\`$kind\` is pinned at its latest release, \`$latest\`, and no contract is red."
      [ "$state" = current ] || why="\`$latest\` replaced the release this issue was about, and \`cli-release-watch.yml\` runs its contract unattended. A new issue opens if a person is needed again."
      gh issue close "$num" --comment "Nothing left to do here: $why (\`cli-update-issues.sh\`)" >/dev/null
      echo "$kind: closed #$num ($state)"
      return 0 ;;
    merge)
      if [ -z "$num" ]; then echo "$kind: $state, no issue needed"; return 0; fi ;;
  esac

  local title cmd="" next
  local wf; wf="$(workflow_of "$kind")"
  case "$state" in
    red)
      title="$kind CLI: contract failed on $latest"
      local where="the run is linked under **Red contracts** below"
      if ! grep -q "<!-- cli-contract-failure $kind -->" "$WORK/rows"; then
        local url; url="$(gh run list --workflow "$wf" --status failure --limit 1 --json url --jq '.[0].url // empty' 2>/dev/null || true)"
        where="${url:+[the newest red run]($url)}"; where="${where:-see the newest red run of the workflow}"
      fi
      next="\`$wf\` failed on \`$latest\` ($where). Fix the cause in a PR that mentions this issue, and once it is merged run the contract again on \`main\`. When it passes, the pin-bump PR that follows closes this issue."
      cmd="$(dispatch_cmd "$kind")" ;;
    streak)
      title="$kind CLI: contract fails on main"
      next="\`$wf\` failed its last 2 runs on \`main\` whatever the version, so the harness or its credential is broken rather than a release. Fix it in a PR that mentions this issue, then run the contract again. The issue closes once it passes and the pin is current."
      cmd="$(dispatch_cmd "$kind")" ;;
    manual)
      title="$kind CLI: bump the pin to $latest"
      next="No contract covers \`$kind\`. Bump \`RTK_VERSION\` in \`workspace/Dockerfile\` to \`$latest\` by hand (\`docs/build/10-development.md\` §10.2.1) in a PR that closes this issue." ;;
    merge)
      local pr; pr="$(bump_pr_for "$kind" "$latest")"
      title="$kind CLI: merge the pin bump to $latest"
      local run; run="$("$RELEASE_STATE" evidence tested "$kind" "$latest" || true)"
      local passed="\`$wf\` passed on \`$latest\`"
      [ -z "$run" ] || passed="[$passed]($run)"
      if [ -n "$pr" ]; then
        next="$passed. Review and merge #$pr; it bumps the pin and closes this issue."
      else
        next="$passed. \`cli-pin-bump.yml\` opens the PR that bumps the pin and closes this issue within two hours; review and merge it."
      fi ;;
    dispatch)
      title="$kind CLI: run the contract on $latest"
      next="\`$kind\` signs in with a credential that rotates, so the watcher never runs its contract unattended. Run it by hand. If it fails at sign-in, refresh $(secret_of "$kind") and run it again. When it passes, the pin-bump PR that follows closes this issue."
      cmd="$(dispatch_cmd "$kind")" ;;
    fragments)
      title="$kind CLI: upstream message fragments gone in $latest"
      next="\`$latest\` no longer contains text Agent Fleet matches on (listed under **Red contracts** below). Check whether the code that matches it still works against the new wording (\`deploy/local/cli-message-fragments.txt\` says where each fragment is used), fix it in a PR that mentions this issue, and update the registry. This is a place to look, not proof of a break. Merging the pin-bump PR closes this issue."
      cmd="" ;;
    caps)
      title="$kind CLI: caps probes drifted on $latest"
      next="The advisory caps drift probes of \`$wf\` reported DRIFT or a probe error on \`$latest\` (the run is linked under **Red contracts** below). They gate nothing yet: the mirror contract's result stands. Read the probe output, then either fix the declared \`Caps\` or the probe, or record it as a false positive. The row goes away when the probes pass or a newer release replaces the version."
      cmd="$(dispatch_cmd "$kind")" ;;
    credential)
      title="$kind CLI: verify $latest (no CI credential)"
      next="The contract secret $(secret_of "$kind") is not configured, so nothing tested \`$latest\`. Either register the secret and run the contract (the pin-bump PR that follows closes this issue), or verify it by hand in a signed-in Terminal (CLI) session — the composer readiness/footer is detected; a prompt sent from the mirror reaches the TUI; both turns appear in \`/messages\`; it returns to idle — then bump the pin in a PR that closes this issue."
      cmd="$(dispatch_cmd "$kind")" ;;
  esac

  {
    printf '**Next step:** %s\n' "$next"
    if [ -n "$cmd" ]; then printf '\n```\n%s\n```\n' "$cmd"; fi
    printf '\n| CLI | Pin | Latest | Contract passed on |\n|---|---|---|---|\n'
    printf '| %s | `%s` | `%s` | %s |\n' "$kind" "$pin" "$latest" "$([ "$kind" = rtk ] && echo 'no contract' || echo "\`${tested:-none}\`")"
    printf '\n<sub>One issue per CLI, kept by `cli-drift.yml` (`deploy/local/cli-update-issues.sh`) while a person has something to do. A PR that bumps the pin closes it; it also closes itself once nothing is left to do.</sub>\n'
    printf '<!-- cli-update kind=%s -->\n' "$kind"
  } > "$WORK/body.md"

  # A contract job's sync and cli-drift's can both have found no issue: look again right
  # before creating, or the kind ends up with two and `number` only ever sees one of them.
  [ -n "$num" ] || num="$(number "$kind")"
  if [ -n "$num" ]; then
    # `carry` keeps the Red contracts section the contract workflows write.
    "$REPORT" carry "$num" "$WORK/body.md"
    # Every 2 hours with nothing new: an unchanged issue is not edited.
    if [ "$(gh issue view "$num" --json title --jq .title)" = "$title" ] &&
       [ "$(gh issue view "$num" --json body --jq .body)" = "$(cat "$WORK/body.md")" ]; then
      echo "$kind: #$num unchanged ($state)"
      return 0
    fi
    gh issue edit "$num" --title "$title" --body-file "$WORK/body.md" >/dev/null
    echo "$kind: updated #$num ($state)"
  else
    gh label create "$LABEL" --color FBCA04 --description "upstream CLI version drift" 2>/dev/null || true
    gh issue create --title "$title" --body-file "$WORK/body.md" --label "$LABEL" >/dev/null
    echo "$kind: opened ($state)"
  fi
}

# --- main ----------------------------------------------------------------------------

cmd="${1:-}"
case "$cmd" in
  number)
    valid_kind "${2:?kind}"
    number "$2" ;;
  sync)
    shift
    kinds=("$@")
    [ "${#kinds[@]}" -gt 0 ] || kinds=("${ALL_KINDS[@]}")
    for k in "${kinds[@]}"; do valid_kind "$k"; done
    set +e
    if [ "${#kinds[@]}" = 1 ]; then
      GITHUB_OUTPUT="$WORK/latest" GITHUB_STEP_SUMMARY=/dev/null "$DRIFT_CHECK" "${kinds[0]}" > "$WORK/drift.txt" 2>&1
    else
      GITHUB_OUTPUT="$WORK/latest" GITHUB_STEP_SUMMARY=/dev/null "$DRIFT_CHECK" > "$WORK/drift.txt" 2>&1
    fi
    rc=$?
    set -e
    case "$rc" in
      0|1) ;;
      *) cat "$WORK/drift.txt" >&2; echo "cli-update-issues: no release source answered; nothing changed" >&2; exit 2 ;;
    esac
    failed="$(sed -n 's/^failed=//p' "$WORK/latest" | tail -1)"
    status=0
    for k in "${kinds[@]}"; do
      pin="$(sed -n "s/^pin_$k=//p" "$WORK/latest" | tail -1)"
      latest="$(sed -n "s/^latest_$k=//p" "$WORK/latest" | tail -1)"
      # An unread source says nothing about the release: leave that kind's issue as it is.
      if [ -z "$pin" ] || [ -z "$latest" ] || case ",$failed," in *",$k,"*) true ;; *) false ;; esac; then
        echo "$k: release source unreadable, left as is"
        continue
      fi
      sync_kind "$k" "$pin" "$latest" || { echo "::warning::could not sync the $k issue" >&2; status=1; }
    done
    exit "$status" ;;
  *)
    echo "usage: $0 sync [kind...] | number <kind>" >&2
    exit 2 ;;
esac
