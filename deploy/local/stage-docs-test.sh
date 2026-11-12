#!/usr/bin/env bash
# Regression test for the link rewrites of deploy/release/stage-docs.sh (the runbook
# copies in the staged docs) and deploy/release/pin-md-links.sh (the READMEs in a release
# archive).
#
# Builds a throwaway repository with one guide page and one runbook README whose links
# appear in prose, in an inline code span and in a fenced block, stages it, and checks
# that only the prose links are rewritten: code is an example the reader must see as
# written. Then plants a dead prose link (must fail the staging) and a dead link inside
# code (must not).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(cd "$HERE/../.." && pwd)/deploy/release/stage-docs.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

make_repo() {  # <dir>
  local r="$1"
  mkdir -p "$r/guide/operate/compose" "$r/deploy/release/notes" \
    "$r/deploy/compose" "$r/deploy/native" "$r/deploy/local" "$r/deploy/aws/ecs" \
    "$r/deploy/aws/ec2-single" "$r/deploy/kubernetes"
  cp "$SRC" "$(dirname "$SRC")/md-links.awk" "$(dirname "$SRC")/pin-md-links.sh" "$r/deploy/release/"
  printf '# Ops\n' > "$r/guide/operate/README.md"
  printf '# Install\n' > "$r/guide/operate/compose/install.md"
  printf '# S\n' > "$r/deploy/release/notes/SUMMARY.md"
  printf '# S\n' > "$r/deploy/release/notes/SUMMARY.ja.md"
  : > "$r/deploy/release/notes/index.tsv"
  for f in native/README.md local/README-wsl.md aws/ecs/README.md aws/ec2-single/README.md kubernetes/README.md; do
    printf '# x\n' > "$r/deploy/$f"
  done
  cat > "$r/deploy/compose/README.md" <<'MD'
# Compose

| Page | What |
|---|---|
| [Install](../../guide/operate/compose/install.md#start) | prose: to the staged guide |
| [Native](../native/README.md) | prose: to the other runbook's copy |
| [Script](backup.sh) | prose: to GitHub |

Inline code stays: `[Install](../../guide/operate/compose/install.md)` and ``a ` [x](backup.sh)``.

```markdown
[Install](../../guide/operate/compose/install.md)
```

~~~
[Script](backup.sh)
~~~
MD
}

R="$T/repo"; make_repo "$R"
bash "$R/deploy/release/stage-docs.sh" "$T/out" > "$T/log" 2>&1 || { cat "$T/log"; fail "staging a clean tree failed"; }
C="$T/out/operate/runbooks/compose.md"
want() { grep -qF -- "$1" "$C" || { cat "$C"; fail "missing: $1"; }; }
want '| [Install](../compose/install.md#start) |'
want '| [Native](native.md) |'
want '| [Script](https://github.com/k-k1/agent-fleet/blob/main/deploy/compose/backup.sh) |'
# shellcheck disable=SC2016  # backticks are the Markdown under test, not expansions
want 'stays: `[Install](../../guide/operate/compose/install.md)` and ``a ` [x](backup.sh)``.'
[ "$(grep -cxF '[Install](../../guide/operate/compose/install.md)' "$C")" = 1 ] || fail "fenced link was rewritten"
[ "$(grep -cxF '[Script](backup.sh)' "$C")" = 1 ] || fail "~~~-fenced link was rewritten"
echo "ok: prose links rewritten, code kept"

# A dead link in code is not a link: staging still passes.
R2="$T/repo2"; make_repo "$R2"
# shellcheck disable=SC2016  # backticks are the Markdown under test, not expansions
printf '\n`[gone](../../guide/operate/nope.md)`\n\n```\n[gone](../../guide/operate/nope.md)\n```\n' >> "$R2/deploy/compose/README.md"
bash "$R2/deploy/release/stage-docs.sh" "$T/out2" > "$T/log2" 2>&1 || { cat "$T/log2"; fail "a dead link inside code failed the staging"; }
echo "ok: dead link inside code ignored"

# A dead prose link fails the staging and is named.
R3="$T/repo3"; make_repo "$R3"
printf '\n[gone](../../guide/operate/nope.md)\n' >> "$R3/deploy/compose/README.md"
if bash "$R3/deploy/release/stage-docs.sh" "$T/out3" > "$T/log3" 2>&1; then fail "a dead prose link did not fail the staging"; fi
grep -qF "operate/runbooks/compose.md links to '../nope.md'" "$T/log3" || { cat "$T/log3"; fail "the failure does not name the link"; }
echo "ok: dead prose link fails"
# md-links.awk itself: a code span that runs across lines keeps the link inside it,
# while an unmatched backtick does not reach past a blank line.
AWKF="$(dirname "$SRC")/md-links.awk"
printf 'old.md\tNEW\n' > "$T/map"
# shellcheck disable=SC2016  # backticks are the Markdown under test, not expansions
printf 'Before `code\n[x](old.md)\nstill code` after [y](old.md)\n\nOpen `tick\n\n[z](old.md)\n' > "$T/span.md"
awk -v mode=rewrite -v map="$T/map" -f "$AWKF" "$T/span.md" > "$T/span.out"
# shellcheck disable=SC2016
printf 'Before `code\n[x](old.md)\nstill code` after [y](NEW)\n\nOpen `tick\n\n[z](NEW)\n' > "$T/span.want"
cmp -s "$T/span.out" "$T/span.want" || { diff "$T/span.want" "$T/span.out"; fail "multi-line code span"; }
[ "$(awk -v mode=links -f "$AWKF" "$T/span.md" | wc -l)" = 2 ] || fail "links mode counted a link inside a multi-line code span"
echo "ok: multi-line code span kept, unmatched backtick stops at the paragraph"

# pin-md-links.sh: a bundle README's prose links that leave the bundle go to the tag,
# a link that resolves inside the bundle and every link in code stay; check then passes,
# and a planted dead prose link fails it.
PIN="$(dirname "$SRC")/pin-md-links.sh"
B="$T/bundle"; mkdir -p "$B"
cp "$R/deploy/compose/README.md" "$B/README.md"; printf '#!/bin/sh\n' > "$B/backup.sh"
bash "$PIN" pin "$B" v9.9.9 - "README.md:deploy/compose/README.md"
grep -qF '| [Install](https://github.com/k-k1/agent-fleet/blob/v9.9.9/guide/operate/compose/install.md#start) |' "$B/README.md" || { cat "$B/README.md"; fail "guide link not pinned to the tag"; }
grep -qF '| [Script](backup.sh) |' "$B/README.md" || fail "a link that resolves in the bundle was changed"
[ "$(grep -cxF '[Install](../../guide/operate/compose/install.md)' "$B/README.md")" = 1 ] || fail "fenced link was pinned"
bash "$PIN" check "$B" || fail "check failed on a pinned bundle"
printf '\n[gone](nope.md)\n' >> "$B/README.md"
if bash "$PIN" check "$B" 2> "$T/log4"; then fail "check passed a dead prose link"; fi
grep -qF "README.md links to 'nope.md'" "$T/log4" || { cat "$T/log4"; fail "check does not name the link"; }
echo "ok: bundle links pinned, code kept, dead link caught"
echo "== stage-docs test OK =="
