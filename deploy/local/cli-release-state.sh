#!/usr/bin/env bash
# Durable release-watcher state backed by append-only comments on one GitHub issue.
#
# Repository Actions variables cannot be written by GITHUB_TOKEN (HTTP 403 even with
# actions:write). Issue comments need only issues:write, are durable, and concurrent
# contracts can append without overwriting each other.
#
#   cli-release-state.sh get tested codex
#   cli-release-state.sh set tested codex 0.145.0
# Namespaces: tested = successful automated contract; seen = fleet-probe issue emitted.
set -euo pipefail

cmd="${1:-}"
namespace="${2:-}"
cli="${3:-}"
version="${4:-}"
title="CLI release watcher state"

case "$namespace" in
  tested|seen) ;;
  *) echo "namespace must be tested or seen" >&2; exit 2 ;;
esac
[[ "$cli" =~ ^[a-z0-9-]+$ ]] || { echo "invalid CLI name: $cli" >&2; exit 2; }

issue_number() {
  gh issue list --state open --search "in:title $title" --json number,title \
    --jq ".[] | select(.title == \"$title\") | .number" | head -1
}

ensure_issue() {
  local num
  num="$(issue_number)"
  if [ -z "$num" ]; then
    # shellcheck disable=SC2016 # literal Markdown backticks, no shell expansion
    num="$(gh issue create --title "$title" --body \
      'Machine-readable state for `.github/workflows/cli-release-watch.yml`. Keep this issue open; each successful contract or handled fleet-probe release appends a state marker.' |
      sed -n 's|.*/issues/\([0-9][0-9]*\)$|\1|p')"
  fi
  [ -n "$num" ] || { echo "could not resolve state issue" >&2; exit 1; }
  printf '%s' "$num"
}

case "$cmd" in
  get)
    num="$(issue_number)"
    [ -n "$num" ] || exit 0
    gh issue view "$num" --json body,comments \
      --jq '[.body, (.comments[].body)] | .[]' |
      sed -n "s/^<!-- cli-release-state $namespace $cli=\\([^ ]*\\) -->$/\\1/p" |
      tail -1
    ;;
  set)
    [ -n "$version" ] || { echo "version is required for set" >&2; exit 2; }
    [[ "$version" =~ ^[0-9A-Za-z._+-]+$ ]] ||
      { echo "invalid version: $version" >&2; exit 2; }
    current="$("$0" get "$namespace" "$cli")"
    [ "$current" != "$version" ] || exit 0
    num="$(ensure_issue)"
    gh issue comment "$num" \
      --body "<!-- cli-release-state $namespace $cli=$version -->" >/dev/null
    ;;
  ensure)
    ensure_issue >/dev/null
    ;;
  *)
    echo "usage: $0 get <tested|seen> <cli> | set <tested|seen> <cli> <version> | ensure <tested|seen> <cli>" >&2
    exit 2
    ;;
esac
