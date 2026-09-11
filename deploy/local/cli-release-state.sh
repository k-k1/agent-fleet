#!/usr/bin/env bash
# Durable release-watcher state backed by one GitHub issue.
#
# Repository Actions variables cannot be written by GITHUB_TOKEN (HTTP 403 even with
# actions:write). Issue comments need only issues:write, are durable, and concurrent
# contracts can append without overwriting each other.
#
#   cli-release-state.sh get tested codex
#   cli-release-state.sh set tested codex 0.145.0
#   cli-release-state.sh set watcher ok 2026-09-11T05:30:12Z
#
# Namespaces:
#   tested   a successful automated contract, appended as a comment (one per version)
#   seen     a fleet-probe issue was emitted, likewise
#   watcher  the watcher's own liveness, keyed by item rather than CLI: `ok` (the last
#            run where every release source was readable), `failed` (the rows the last
#            run could not read, or `none`) and `at` (when that run was). Without it a
#            `tested` version that stops moving reads the same whether upstream went
#            quiet or the watcher fell over; on 2026-09-09 it was the latter and nobody
#            could tell.
#
# watcher is current status, not history, so it is **rewritten in the issue body** and
# written as visible list items. Appending it would add a comment every single day —
# `gh issue view --json comments` would eventually stop returning the `tested` markers
# this whole mechanism depends on — and the person opening the issue to ask "why is
# tested old?" has to be able to read the answer without viewing the Markdown source.
# Only cli-release-watch.yml writes it, under a `concurrency` group of one, so the
# read-modify-write of the body has no second writer.
set -euo pipefail

cmd="${1:-}"
namespace="${2:-}"
cli="${3:-}"
version="${4:-}"
title="CLI release watcher state"

case "$namespace" in
  tested|seen|watcher) ;;
  *) echo "namespace must be tested, seen or watcher" >&2; exit 2 ;;
esac
[[ "$cli" =~ ^[a-z0-9-]+$ ]] || { echo "invalid CLI name: $cli" >&2; exit 2; }

tmp_body="$(mktemp)"
trap 'rm -f "$tmp_body"' EXIT

# `first` inside jq, not `| head -1`: the pipe leaves gh to die of SIGPIPE, and under
# `pipefail` that aborts this whole script (`set -e`) for a lookup that succeeded.
issue_number() {
  gh issue list --state open --search "in:title $title" --json number,title \
    --jq "[.[] | select(.title == \"$title\") | .number] | first // empty"
}

ensure_issue() {
  local num
  num="$(issue_number)"
  if [ -z "$num" ]; then
    # shellcheck disable=SC2016 # literal Markdown backticks, no shell expansion
    num="$(gh issue create --title "$title" --body \
      'Machine-readable state for `.github/workflows/cli-release-watch.yml`. Keep this issue open; each successful contract or handled fleet-probe release appends a state marker, and the Watcher section below is rewritten on every run.' |
      sed -n 's|.*/issues/\([0-9][0-9]*\)$|\1|p')"
  fi
  [ -n "$num" ] || { echo "could not resolve state issue" >&2; exit 1; }
  printf '%s' "$num"
}

# The watcher block's line, visible as a Markdown list item and parsed back by the
# same shape. `sed -n` over it never needs the HTML-comment form.
watcher_line() { printf -- '- `cli-release-state watcher %s=%s`' "$1" "$2"; }

case "$cmd" in
  get)
    num="$(issue_number)"
    [ -n "$num" ] || exit 0
    if [ "$namespace" = watcher ]; then
      body="$(gh issue view "$num" --json body --jq .body)"
      sed -n "s/^- \`cli-release-state watcher $cli=\\([^ \`]*\\)\`\$/\\1/p" <<< "$body" | tail -1
      exit 0
    fi
    gh issue view "$num" --json body,comments \
      --jq '[.body, (.comments[].body)] | .[]' |
      sed -n "s/^<!-- cli-release-state $namespace $cli=\\([^ ]*\\) -->$/\\1/p" |
      tail -1
    ;;
  set)
    [ -n "$version" ] || { echo "version is required for set" >&2; exit 2; }
    # Both patterns exclude whitespace and the delimiters the value is read back
    # between. watcher values additionally carry `:` (ISO timestamps) and `,` (a list
    # of rows).
    if [ "$namespace" = watcher ]; then
      pattern='^[0-9A-Za-z._:,+-]+$'
    else
      pattern='^[0-9A-Za-z._+-]+$'
    fi
    [[ "$version" =~ $pattern ]] ||
      { echo "invalid value: $version" >&2; exit 2; }
    current="$("$0" get "$namespace" "$cli")"
    [ "$current" != "$version" ] || exit 0
    num="$(ensure_issue)"
    if [ "$namespace" = watcher ]; then
      body="$(gh issue view "$num" --json body --jq .body)"
      # `|| true`: grep -v exits 1 when it filters everything out, and the body of a
      # freshly created issue is exactly that case.
      kept="$(grep -v "^- \`cli-release-state watcher $cli=" <<< "$body" || true)"
      case "$kept" in
        *'### Watcher'*) ;;
        *) kept="$kept"$'\n\n### Watcher\n\nRewritten by `cli-release-watch.yml` on every run.\n' ;;
      esac
      printf '%s\n%s\n' "$kept" "$(watcher_line "$cli" "$version")" > "$tmp_body"
      gh issue edit "$num" --body-file "$tmp_body" >/dev/null
      exit 0
    fi
    gh issue comment "$num" \
      --body "<!-- cli-release-state $namespace $cli=$version -->" >/dev/null
    ;;
  ensure)
    ensure_issue >/dev/null
    ;;
  *)
    echo "usage: $0 get <tested|seen|watcher> <cli|item> | set <tested|seen|watcher> <cli|item> <value> | ensure <namespace> <cli>" >&2
    exit 2
    ;;
esac
