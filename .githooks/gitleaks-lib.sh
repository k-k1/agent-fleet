#!/usr/bin/env bash
# Shared by pre-commit and pre-push (sourced, never run on its own).
#
# CI's secret-scan job runs gitleaks over the whole history, so it only speaks up after the
# push has already published the value (#4905: a made-up key in a test fixture reached a
# pushed branch and blocked unrelated PRs). The two hooks run the same scanner with the same
# .gitleaks.toml earlier: pre-commit over what the commit adds, pre-push over the commits
# about to leave.
#
# Fail-open on purpose: with no gitleaks on PATH, or one that cannot run, the hook warns once
# and lets the work through, like pre-commit does without a Go toolchain. A version other than
# the one ci.yml pins only warns: rule sets differ between releases, and CI stays the authority.

# Exit code gitleaks uses for "leaks found", so a crash (exit 1) is not read as a finding.
GITLEAKS_LEAK_RC=99

# The version ci.yml pins, read from there so the two cannot drift apart. Empty when it
# cannot be found (the check is then skipped, not failed).
gitleaks_pinned_version() {
  awk '
    /install gitleaks/ { seen = 1 }
    seen && /VERSION:/ { print $2; exit }
  ' "$1/.github/workflows/ci.yml" 2>/dev/null || true
}

# Sets GITLEAKS_BIN, or returns 1 after printing the one warning.
# $1 = hook name, $2 = repository root.
gitleaks_locate() {
  local hook="$1" root="$2" have want
  if ! GITLEAKS_BIN="$(command -v gitleaks 2>/dev/null)"; then
    echo "$hook: no gitleaks on PATH — skipping the credential scan (CI's secret-scan still runs it)" >&2
    return 1
  fi
  have="$("$GITLEAKS_BIN" version 2>/dev/null | head -n1 | sed 's/^v//')" || have=""
  want="$(gitleaks_pinned_version "$root")"
  if [ -n "$want" ] && [ "$have" != "$want" ]; then
    echo "$hook: gitleaks ${have:-?} found, ci.yml pins $want — scanning anyway; a result that differs from CI is not a block" >&2
  fi
  return 0
}

# Runs gitleaks with the repository's config. Findings go to stderr, redacted. Returns
# 0 = clean, 2 = a finding, 1 = gitleaks itself failed (the caller lets that through).
# $1 = repository root, rest = arguments for `gitleaks git`.
gitleaks_run() {
  local root="$1" rc=0
  shift
  local cfg=()
  [ -f "$root/.gitleaks.toml" ] && cfg=(-c "$root/.gitleaks.toml")
  (cd "$root" && "$GITLEAKS_BIN" git "$@" ${cfg[@]+"${cfg[@]}"} --redact --verbose --no-banner --no-color --log-level warn \
    --exit-code "$GITLEAKS_LEAK_RC" .) >&2 || rc=$?
  case "$rc" in
    0) return 0 ;;
    "$GITLEAKS_LEAK_RC") return 2 ;;
    *) return 1 ;;
  esac
}

# What to do about a finding, printed after gitleaks' own report.
gitleaks_advice() {
  cat >&2 <<'EOF'
  A credential-shaped value is in the content above (file and line are in the report; the
  value itself is redacted). Do not push it and fix it in the next commit — once pushed it
  is public.
  - A test fixture: build the value at run time (concatenate the pieces) so the literal
    never appears in the file.
  - A value that is provably not a credential: allowlist that exact value (a regex for the
    value, never a path) in .gitleaks.toml, as the file's header says.
EOF
}
