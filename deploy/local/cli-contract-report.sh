#!/usr/bin/env bash
# Red contracts, written into the CLI's own update issue (cli-update-issues.sh).
#
#   cli-contract-report.sh failure <kind> <version>                 a contract finished red
#   cli-contract-report.sh success <kind>                           it passed again
#   cli-contract-report.sh advisory <kind> <version> [detail]       advisory finding, no marker
#   cli-contract-report.sh advisory-clear <kind>                    the advisory check passed
#   cli-contract-report.sh fragments <kind> <version> <fragment>... upstream message fragments gone
#   cli-contract-report.sh fragments-clear <kind>                   none are gone any more
#   cli-contract-report.sh streak <workflow> <n>                    N red runs in a row?
#   cli-contract-report.sh prune                                    drop rows for old versions
#   cli-contract-report.sh carry <issue-number> <body-file>         re-append the section
#   cli-contract-report.sh active <kind>                            exit 0 when any row is set
#
# ## Why one section in the body
#
# Each CLI has at most one open issue, titled with the next step, and a red contract is the
# reason it says "fix this". The report lives there, as a single `Red contracts` section
# between two HTML comment fences, rewritten in place: one comment per run would bury it,
# and a red contract can fire every 2 hours. cli-update-issues.sh rewrites the rest of the
# body whenever the state moves, so it calls `carry` to keep the section.
#
# Rows are one list line each, keyed by a trailing HTML comment:
#   `failure` rows: `<!-- cli-contract-failure <kind> -->`, replaced by the newest failure;
#                   removed by `success` or when `latest` moves (`prune`).
#   `fragments` rows: `<!-- cli-contract-fragments <kind> -->`, from cli-fragment-check.sh via
#                   cli-pin-bump.sh: the new release no longer contains text Agent Fleet matches
#                   on. Not a failed contract, so it writes no `red` marker and `success` leaves
#                   it; replaced by the newest check, removed by `fragments-clear` or `prune`.
#   `caps` rows:    `<!-- cli-contract-caps <kind> -->`, from the advisory caps drift probes. A
#                   finding to look at, not a failed contract: it writes no `red` marker and
#                   leaves the watcher alone, so a false positive costs a row and nothing else.
#                   `success` leaves it; replaced by the newest finding, removed by
#                   `advisory-clear` or `prune`.
#   `streak` rows:  `<!-- cli-contract-streak <workflow> -->`, removed once the workflow's
#                   newest run is no longer red.
#
# ## Failure modes
#
# - Several writers can touch the same issue at once (a contract and the cli-drift run), and
#   each does a read-modify-write of the body. Every write is read back and re-applied (4
#   attempts) until the intended change holds in what the issue now says. There is no
#   compare-and-swap on an issue body: a rewrite landing in the seconds between its `carry`
#   and its edit can still drop a row written in that window, which the next failure or
#   streak run restores.
# - A streak row is only kept while the newest of its runs is under 14 days old.
# - No issue for the kind yet: one is created with only this section, and `failure` /
#   `success` then ask cli-update-issues.sh to write the title and next step for that kind
#   (best effort; cli-drift.yml does it on its next run anyway). An issue is never closed
#   while a row remains.
# - `failure` also writes the `red` marker (see cli-release-edges.sh) for kinds the watcher
#   dispatches unattended; cursor and kiro get the report but no marker.
set -euo pipefail

BEGIN='<!-- cli-contract-failures:begin -->'
END='<!-- cli-contract-failures:end -->'
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RED_KINDS="${RED_KINDS:-claude codex opencode copilot agy muse}"
RUN_URL="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-}/actions/runs/${GITHUB_RUN_ID:-}"

tmp="$(mktemp)"
trap 'rm -f "$tmp" "$tmp".*' EXIT

ISSUES="${ISSUES:-$HERE/cli-update-issues.sh}"
# Brings the kind's title and next step up to date after a row changed. `true` in tests.
SYNC="${SYNC:-$HERE/cli-update-issues.sh}"

issue_number() { "$ISSUES" number "$1"; }

# The kind's issue number as mutate last saw or created it. `sync` gets it through
# KNOWN_ISSUE: its own list lookup lags just after a creation and would open a second issue.
MUTATED_NUM=""

kind_of_workflow() { case "$1" in claude-tui-contract) echo claude ;; *) echo "${1%-contract}" ;; esac; }

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

# mutate <kind> <rows-transform-function> [<title for a new issue>]: read the kind's issue,
# transform its rows, write back, verify. A title of `-` never creates an issue.
#
# Verification is of the INTENT, not of the bytes written: after the write the rows are read
# again and the transform is applied to them once more; the write counts only when that
# changes nothing (as a set). A concurrent writer that read the body before ours landed and
# then wrote over it leaves our row missing, the second application is not a no-op, and the
# loop re-reads and re-applies. Comparing against our own output would pass in exactly the
# case that loses a row.
mutate() {
  local kind="$1" fn="$2" title="${3:-$1 CLI: contract failed}" num url attempt
  for attempt in 1 2 3 4; do
    num="$(issue_number "$kind")"
    MUTATED_NUM="$num"
    if [ -n "$num" ]; then
      gh issue view "$num" --json body --jq .body > "$tmp"
    else
      printf 'Contract results for `%s` (see below). The next step is added by `cli-update-issues.sh`.\n' "$kind" > "$tmp"
    fi
    rows_of < "$tmp" > "$tmp.rows"
    "$fn" "$tmp.rows" > "$tmp.rows.new"
    render "$tmp" "$tmp.rows.new" > "$tmp.new"
    if cmp -s "$tmp" "$tmp.new"; then return 0; fi
    if [ -z "$num" ]; then
      # Nothing to remove from an issue that does not exist.
      [ -s "$tmp.rows.new" ] || return 0
      [ "$title" != - ] || return 0
      # Another reporter may have created it since the lookup above: re-read instead of
      # opening a second issue for the kind.
      if [ -n "$(issue_number "$kind")" ]; then continue; fi
      gh label create cli-drift --color FBCA04 --description "upstream CLI version drift" 2>/dev/null || true
      # The number comes from the URL create prints: the list lookup is a search index and
      # lags right after creation, and an empty number fails the read-back below.
      url="$(gh issue create --title "$title" --body-file "$tmp.new" --label cli-drift)"
      num="${url##*/}"
      [[ "$num" =~ ^[0-9]+$ ]] || { echo "could not read the new issue's number from: $url" >&2; return 1; }
      MUTATED_NUM="$num"
    else
      gh issue edit "$num" --body-file "$tmp.new" >/dev/null
    fi
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
    # A job that died before it read the version cannot be matched against latest, so the
    # sync would close a new issue right after opening it. Such a row goes only into an
    # issue that is already open; a harness that keeps dying is the streak check's to report.
    if [ "$version" = unknown ]; then mutate "$kind" add_row -; else mutate "$kind" add_row; fi
    # The brake for the watcher. Only a real version can be matched against `latest`.
    if [ "$version" != unknown ] && case " $RED_KINDS " in *" $kind "*) true ;; *) false ;; esac; then
      "$HERE/cli-release-state.sh" set red "$kind" "$version"
    fi
    KNOWN_ISSUE="$MUTATED_NUM" "$SYNC" sync "$kind" || echo "::warning::could not update the $kind issue's next step"
    ;;
  success)
    kind="${2:?kind}"
    drop_row() { grep -v "<!-- cli-contract-failure $kind -->" "$1" || true; }
    mutate "$kind" drop_row
    # Release the watcher's brake too, so `get red` never reports a version that has since
    # passed. `none` is the cleared value: the state script has no delete.
    if [ -n "$("$HERE/cli-release-state.sh" get red "$kind")" ] &&
       [ "$("$HERE/cli-release-state.sh" get red "$kind")" != none ]; then
      "$HERE/cli-release-state.sh" set red "$kind" none
    fi
    # Only when an issue exists: a pass with nothing to report must not cost a sync.
    if [ -n "$(issue_number "$kind")" ]; then
      "$SYNC" sync "$kind" || echo "::warning::could not update the $kind issue's next step"
    fi
    ;;
  advisory)
    kind="${2:?kind}"; version="${3:-unknown}"; detail="${4:-caps drift probes}"
    [[ "$kind" =~ ^[a-z0-9-]+$ ]] || { echo "invalid kind: $kind" >&2; exit 2; }
    [[ "$version" =~ ^[0-9A-Za-z._+-]+$ ]] || version=unknown
    case "$detail" in *'`'*|*'<!--'*|*$'\n'*) detail="caps drift probes" ;; esac
    row="- \`$kind\` \`$version\` — advisory, no gate: \`$detail\` reported DRIFT or a probe error — [run]($RUN_URL) <!-- cli-contract-caps $kind -->"
    add_row() { grep -v "<!-- cli-contract-caps $kind -->" "$1" || true; printf '%s\n' "$row"; }
    # Same rule as `failure`: a row for an unknown version goes only into an open issue.
    if [ "$version" = unknown ]; then mutate "$kind" add_row -; else mutate "$kind" add_row "$kind CLI: caps probes drifted on $version"; fi
    # Deliberately no `red` marker: this must never hold back the watcher.
    KNOWN_ISSUE="$MUTATED_NUM" "$SYNC" sync "$kind" || echo "::warning::could not update the $kind issue's next step"
    ;;
  advisory-clear)
    kind="${2:?kind}"
    [[ "$kind" =~ ^[a-z0-9-]+$ ]] || { echo "invalid kind: $kind" >&2; exit 2; }
    drop_row() { grep -v "<!-- cli-contract-caps $kind -->" "$1" || true; }
    mutate "$kind" drop_row
    if [ -n "$(issue_number "$kind")" ]; then
      "$SYNC" sync "$kind" || echo "::warning::could not update the $kind issue's next step"
    fi
    ;;
  fragments)
    kind="${2:?kind}"; version="${3:?version}"; shift 3
    [ "$#" -gt 0 ] || { echo "fragments needs at least one fragment" >&2; exit 2; }
    [[ "$kind" =~ ^[a-z0-9-]+$ ]] || { echo "invalid kind: $kind" >&2; exit 2; }
    [[ "$version" =~ ^[0-9A-Za-z._+-]+$ ]] || { echo "invalid version: $version" >&2; exit 2; }
    list=""
    for f in "$@"; do
      case "$f" in *'`'*|*'<!--'*) echo "invalid fragment: $f" >&2; exit 2 ;; esac
      list="${list:+$list, }\`$f\`"
    done
    row="- \`$kind\` \`$version\` — upstream message fragments gone from the package: $list (registry: \`deploy/local/cli-message-fragments.txt\`; a place to look, not proof of a break) <!-- cli-contract-fragments $kind -->"
    add_row() { grep -v "<!-- cli-contract-fragments $kind -->" "$1" || true; printf '%s\n' "$row"; }
    mutate "$kind" add_row "$kind CLI: upstream message fragments gone"
    KNOWN_ISSUE="$MUTATED_NUM" "$SYNC" sync "$kind" || echo "::warning::could not update the $kind issue's next step"
    ;;
  fragments-clear)
    kind="${2:?kind}"
    drop_row() { grep -v "<!-- cli-contract-fragments $kind -->" "$1" || true; }
    mutate "$kind" drop_row
    ;;
  streak)
    wf="${2:?workflow}"; n="${3:?n}"
    [[ "$wf" =~ ^[a-z0-9-]+$ ]] || { echo "invalid workflow: $wf" >&2; exit 2; }
    kind="$(kind_of_workflow "$wf")"
    # Newest first. Cancelled and skipped runs say nothing about the harness, so they are
    # neither red nor green: only the newest N that finished decide.
    conclusions="$(gh run list --workflow "$wf.yml" --branch main --status completed --limit 30 \
      --json conclusion,url,createdAt --jq '[.[] | select(.conclusion == "success" or .conclusion == "failure")]')"
    total="$(jq 'length' <<< "$conclusions")"
    reds="$(jq --argjson n "$n" '[.[:$n][] | select(.conclusion == "failure")] | length' <<< "$conclusions")"
    url="$(jq -r '.[0].url // ""' <<< "$conclusions")"
    # A dispatch-only workflow gets no new run until the next release, so an old red streak
    # that nobody has re-run is history, not news: past STREAK_MAX_AGE_DAYS the row is not
    # (re)created and an existing one is dropped.
    fresh="$(jq --argjson d "${STREAK_MAX_AGE_DAYS:-14}" '(.[0].createdAt // "1970-01-01T00:00:00Z" | fromdateiso8601) > (now - $d * 86400)' <<< "$conclusions")"
    if [ "$total" -ge "$n" ] && [ "$reds" -eq "$n" ] && [ "$fresh" = true ]; then
      row="- \`$wf\` failed its last $n runs on \`main\`, whatever the version — [latest run]($url) <!-- cli-contract-streak $wf -->"
      set_row() { grep -v "<!-- cli-contract-streak $wf -->" "$1" || true; printf '%s\n' "$row"; }
      mutate "$kind" set_row "$kind CLI: contract fails on main"
    else
      clear_row() { grep -v "<!-- cli-contract-streak $wf -->" "$1" || true; }
      mutate "$kind" clear_row
    fi
    ;;
  prune)
    # LATEST_<KIND> from cli-drift-check.sh. A row for a version that is no longer latest
    # describes a release nobody will dispatch again.
    prune_rows() {
      local line k v upper cur
      while IFS= read -r line; do
        k="$(sed -n 's/.*<!-- cli-contract-\(failure\|fragments\|caps\) \([a-z0-9-]*\) -->.*/\2/p' <<< "$line")"
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
    for kind in claude codex opencode copilot agy cursor kiro muse; do
      mutate "$kind" prune_rows
    done
    ;;
  carry)
    num="${2:?issue}"; file="${3:?body file}"
    gh issue view "$num" --json body --jq .body | rows_of > "$tmp.rows"
    [ -s "$tmp.rows" ] || exit 0
    cp "$file" "$tmp.body"
    render "$tmp.body" "$tmp.rows" > "$file"
    ;;
  active)
    kind="${2:?kind}"
    num="$(issue_number "$kind")"
    [ -n "$num" ] || exit 1
    gh issue view "$num" --json body --jq .body | rows_of | grep -q .
    ;;
  *)
    echo "usage: $0 failure <kind> <version> | success <kind> | advisory <kind> <version> [detail] | advisory-clear <kind> | fragments <kind> <version> <fragment>... | fragments-clear <kind> | streak <workflow> <n> | prune | carry <issue> <file> | active <kind>" >&2
    exit 2
    ;;
esac
