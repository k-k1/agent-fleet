#!/usr/bin/env bash
# Red contracts, written into the CLI drift tracking issue.
#
#   cli-contract-report.sh failure <kind> <version>                 a contract finished red
#   cli-contract-report.sh success <kind>                           it passed again
#   cli-contract-report.sh streak <workflow> <n>                    N red runs in a row?
#   cli-contract-report.sh prune                                    drop rows for old versions
#   cli-contract-report.sh carry <issue-number> <body-file>         re-append the section
#   cli-contract-report.sh rewrite <issue-number> <body-file>       carry, then edit the issue
#   cli-contract-report.sh active                                   exit 0 when any row is set
#
# ## Why one section in the body
#
# The issue `cli-drift.yml` maintains is the one a person opens to ask "why have the pins not
# moved?". The report lives there, as a single `Red contracts` section between two HTML
# comment fences, rewritten in place: one comment per run would bury it, and a red contract
# can fire every 2 hours. cli-drift.yml rewrites the whole body on every drift run, so it
# calls `carry` to keep the section instead of dropping it.
#
# Rows are one list line each, keyed by a trailing HTML comment:
#   `failure` rows: `<!-- cli-contract-failure <kind> -->`, one per kind, replaced by the
#                   newest failure; removed by `success` or when `latest` moves (`prune`).
#   `streak` rows:  `<!-- cli-contract-streak <workflow> -->`, removed once the workflow's
#                   newest run is no longer red.
#
# ## Failure modes
#
# - Several contracts can fail at the same moment and each does a read-modify-write of the
#   body. Every write is read back and re-applied (4 attempts) until the intended change
#   holds in what the issue now says. There is no compare-and-swap on an issue body: a
#   cli-drift.yml rewrite landing in the seconds between its `carry` and its edit can still
#   drop a row written in that window, which the next failure or streak run restores.
# - A streak row is only kept while the newest of its runs is under 14 days old.
# - No tracking issue yet (the pins are in sync but a contract failed): one is created, with
#   only this section. cli-drift.yml neither closes nor rewrites it away while a row remains.
# - `failure` also writes the `red` marker (see cli-release-edges.sh) for kinds the watcher
#   dispatches unattended; cursor and kiro get the report but no marker.
set -euo pipefail

TITLE="CLI version drift: Dockerfile pins are behind public latest"
BEGIN='<!-- cli-contract-failures:begin -->'
END='<!-- cli-contract-failures:end -->'
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RED_KINDS="${RED_KINDS:-claude codex opencode copilot agy muse}"
RUN_URL="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-}/actions/runs/${GITHUB_RUN_ID:-}"

tmp="$(mktemp)"
trap 'rm -f "$tmp" "$tmp".*' EXIT

issue_number() {
  gh issue list --state open --search "in:title CLI version drift" --json number,title \
    --jq '[.[] | select(.title | startswith("CLI version drift:")) | .number] | first // empty'
}

# The rows (list lines) of the section in a body, one per line.
rows_of() { sed -n "\\|^$BEGIN\$|,\\|^$END\$|{/^- /p}"; }

# Body without the section, then the section built from the given rows file (if any).
render() { # render <body-file> <rows-file>  -> stdout
  awk -v b="$BEGIN" -v e="$END" '$0==b{skip=1} !skip{print} $0==e{skip=0}' "$1" |
    sed -e :a -e '/^\n*$/{$d;N;ba' -e '}'
  if [ -s "$2" ]; then
    printf '\n%s\n### Red contracts\n\n' "$BEGIN"
    cat "$2"
    printf '\nUpdated in place by the contract workflows and `cli-drift.yml`; a row goes away when\nthat contract passes again or a newer release replaces the version.\n%s\n' "$END"
  fi
}

# mutate <rows-transform-function>: read the issue, transform its rows, write back, verify.
#
# Verification is of the INTENT, not of the bytes written: after the write the rows are read
# again and the transform is applied to them once more; the write counts only when that
# changes nothing (as a set). A concurrent writer that read the body before ours landed and
# then wrote over it leaves our row missing, the second application is not a no-op, and the
# loop re-reads and re-applies. Comparing against our own output would pass in exactly the
# case that loses a row.
mutate() {
  local fn="$1" num attempt
  for attempt in 1 2 3 4; do
    num="$(issue_number)"
    if [ -n "$num" ]; then
      gh issue view "$num" --json body --jq .body > "$tmp"
    else
      printf 'Contract results for the public CLI releases (see below). The pin-drift report is added by `cli-drift.yml`.\n' > "$tmp"
    fi
    rows_of < "$tmp" > "$tmp.rows"
    "$fn" "$tmp.rows" > "$tmp.rows.new"
    render "$tmp" "$tmp.rows.new" > "$tmp.new"
    if cmp -s "$tmp" "$tmp.new"; then return 0; fi
    if [ -z "$num" ]; then
      # Nothing to remove from an issue that does not exist.
      [ -s "$tmp.rows.new" ] || return 0
      # Another reporter may have created it since the lookup above: re-read instead of
      # opening a second tracking issue.
      if [ -n "$(issue_number)" ]; then continue; fi
      gh label create cli-drift --color FBCA04 --description "upstream CLI version drift" 2>/dev/null || true
      gh issue create --title "$TITLE" --body-file "$tmp.new" --label cli-drift >/dev/null
    else
      gh issue edit "$num" --body-file "$tmp.new" >/dev/null
    fi
    num="$(issue_number)"
    gh issue view "$num" --json body --jq .body | rows_of > "$tmp.check"
    "$fn" "$tmp.check" | sort > "$tmp.check.applied"
    sort "$tmp.check" > "$tmp.check.sorted"
    if cmp -s "$tmp.check.sorted" "$tmp.check.applied"; then return 0; fi
    sleep $((attempt * 2))
  done
  echo "could not write the red-contracts section after 4 attempts" >&2
  return 1
}

# The steps of this run that failed, one name per line, from the Actions API (needs
# `actions: read`). Step names, not test names: the contract steps stay untouched, and each
# step is one named contract (`real models catalog drift contract`), so the name says what
# broke; the linked run has the test output. The step that is calling us is still running
# and has no conclusion yet, so it never lists itself.
failed_steps() {
  [ -n "${GITHUB_RUN_ID:-}" ] && [ -n "${GITHUB_REPOSITORY:-}" ] || return 0
  gh api "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/jobs?per_page=100" \
    --jq '.jobs[].steps[]? | select(.conclusion == "failure") | .name' 2>/dev/null |
    awk '!s[$0]++' | head -n 6 | paste -sd'|' - | sed 's/|/`, `/g' || true
}

cmd="${1:-}"
case "$cmd" in
  failure)
    kind="${2:?kind}"; version="${3:-unknown}"
    [[ "$kind" =~ ^[a-z0-9-]+$ ]] || { echo "invalid kind: $kind" >&2; exit 2; }
    [[ "$version" =~ ^[0-9A-Za-z._+-]+$ ]] || version=unknown
    steps="$(failed_steps)"
    if [ -n "$steps" ]; then steps="failing step: \`$steps\`"; else steps="failing step not captured"; fi
    row="- \`$kind\` \`$version\` — $steps — [run]($RUN_URL) <!-- cli-contract-failure $kind -->"
    add_row() { grep -v "<!-- cli-contract-failure $kind -->" "$1" || true; printf '%s\n' "$row"; }
    mutate add_row
    # The brake for the watcher. Only a real version can be matched against `latest`.
    if [ "$version" != unknown ] && case " $RED_KINDS " in *" $kind "*) true ;; *) false ;; esac; then
      "$HERE/cli-release-state.sh" set red "$kind" "$version"
    fi
    ;;
  success)
    kind="${2:?kind}"
    drop_row() { grep -v "<!-- cli-contract-failure $kind -->" "$1" || true; }
    mutate drop_row
    # Release the watcher's brake too, so `get red` never reports a version that has since
    # passed. `none` is the cleared value: the state script has no delete.
    if [ -n "$("$HERE/cli-release-state.sh" get red "$kind")" ] &&
       [ "$("$HERE/cli-release-state.sh" get red "$kind")" != none ]; then
      "$HERE/cli-release-state.sh" set red "$kind" none
    fi
    ;;
  rewrite)
    # rewrite <issue> <file>: replace the whole body with <file> but keep the section as it
    # is NOW (read immediately before the edit, to keep the window small).
    num="${2:?issue}"; file="${3:?body file}"
    "$0" carry "$num" "$file"
    gh issue edit "$num" --body-file "$file" >/dev/null
    ;;
  streak)
    wf="${2:?workflow}"; n="${3:?n}"
    [[ "$wf" =~ ^[a-z0-9-]+$ ]] || { echo "invalid workflow: $wf" >&2; exit 2; }
    # Newest first. Cancelled and skipped runs say nothing about the harness, so they are
    # neither red nor green: only the newest N that finished decide.
    conclusions="$(gh run list --workflow "$wf.yml" --branch develop --status completed --limit 30 \
      --json conclusion,url,createdAt --jq '[.[] | select(.conclusion == "success" or .conclusion == "failure")]')"
    total="$(jq 'length' <<< "$conclusions")"
    reds="$(jq --argjson n "$n" '[.[:$n][] | select(.conclusion == "failure")] | length' <<< "$conclusions")"
    url="$(jq -r '.[0].url // ""' <<< "$conclusions")"
    # A dispatch-only workflow gets no new run until the next release, so an old red streak
    # that nobody has re-run is history, not news: past STREAK_MAX_AGE_DAYS the row is not
    # (re)created and an existing one is dropped.
    fresh="$(jq --argjson d "${STREAK_MAX_AGE_DAYS:-14}" '(.[0].createdAt // "1970-01-01T00:00:00Z" | fromdateiso8601) > (now - $d * 86400)' <<< "$conclusions")"
    if [ "$total" -ge "$n" ] && [ "$reds" -eq "$n" ] && [ "$fresh" = true ]; then
      row="- \`$wf\` failed its last $n runs on \`develop\`, whatever the version — [latest run]($url) <!-- cli-contract-streak $wf -->"
      set_row() { grep -v "<!-- cli-contract-streak $wf -->" "$1" || true; printf '%s\n' "$row"; }
      mutate set_row
    else
      clear_row() { grep -v "<!-- cli-contract-streak $wf -->" "$1" || true; }
      mutate clear_row
    fi
    ;;
  prune)
    # LATEST_<KIND> from cli-drift-check.sh. A row for a version that is no longer latest
    # describes a release nobody will dispatch again.
    prune_rows() {
      local line k v upper cur
      while IFS= read -r line; do
        k="$(sed -n 's/.*<!-- cli-contract-failure \([a-z0-9-]*\) -->.*/\1/p' <<< "$line")"
        if [ -n "$k" ]; then
          v="$(sed -n 's/^- `[a-z0-9-]*` `\([^`]*\)`.*/\1/p' <<< "$line")"
          upper="$(printf '%s' "$k" | tr '[:lower:]-' '[:upper:]_')"
          eval "cur=\${LATEST_${upper}:-}"
          # An unread latest keeps the row: when in doubt, leave the report standing.
          [ -z "$cur" ] || [ "$cur" = "$v" ] || continue
        fi
        printf '%s\n' "$line"
      done < "$1"
    }
    mutate prune_rows
    ;;
  carry)
    num="${2:?issue}"; file="${3:?body file}"
    gh issue view "$num" --json body --jq .body | rows_of > "$tmp.rows"
    [ -s "$tmp.rows" ] || exit 0
    cp "$file" "$tmp.body"
    render "$tmp.body" "$tmp.rows" > "$file"
    ;;
  active)
    num="$(issue_number)"
    [ -n "$num" ] || exit 1
    gh issue view "$num" --json body --jq .body | rows_of | grep -q .
    ;;
  *)
    echo "usage: $0 failure <kind> <version> | success <kind> | rewrite <issue> <file> | streak <workflow> <n> | prune | carry <issue> <file> | active" >&2
    exit 2
    ;;
esac
