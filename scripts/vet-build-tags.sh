#!/usr/bin/env bash
# Vets the build-tagged files of the current Go module.
#
# Build-tagged files are not part of the default build, so none of gofmt / go vet /
# go build / go test touches them, and neither do the six CI jobs — a rotten reference
# passes green. Measured: an unused import left in opencode_contract_test.go kept every
# worker-side gate and 6/6 CI jobs green while only `go vet -tags clicontract` exited 1.
# Running the tagged tests needs the real CLI binaries and cannot happen in CI, but vet is
# type checking only, so it runs.
#
# Usage: scripts/vet-build-tags.sh <known tags...>
#   Passing no known tag declares "this module should have no tagged files".
#   A tag appearing in the source turns this red, which is how anyone notices.
set -euo pipefail

known=("$@")

# Platform words (GOOS/GOARCH) must never be vetted.
# Hand-writing this list means that the day a `//go:build riscv64` file appears it goes red
# as an "unknown tag" and pushes whoever hits it toward the worst possible fix — adding it
# to known, i.e. running `go vet -tags riscv64` on linux and forcing a platform-only file
# into the build. So Go itself enumerates them.
mapfile -t platform < <(go tool dist list | tr '/' '\n' | sort -u)

# Tags defined by the toolchain are out of scope for the same reason. `goexperiment.*` and
# `go1.*` match by prefix.
toolchain=(cgo gc gccgo race msan asan purego boringcrypto unix ignore)

is_excluded() {
  local t=$1
  case "$t" in goexperiment.*|go1.*) return 0 ;; esac
  local x
  for x in "${platform[@]}" "${toolchain[@]}"; do [ "$t" = "$x" ] && return 0; done
  return 1
}

# Extraction must not reject on "identifier shape". A shape allowlist such as
# `^[a-z][a-z_]*$` silently drops tags containing digits or dots (newtag2,
# goexperiment.arenas) instead of reporting them as unknown — the one hole in a design
# whose rule is "reject what you do not know" (measured during review). Collect broadly and
# exclude only through the two tables above.
# `|| true` is required: when nothing remains, pipefail makes the whole pipeline exit 1 and
# produces a red with no reason given (which always happens in a module with zero tags).
found=$(grep -rhE '^//go:build ' --include='*.go' . 2>/dev/null \
  | sed 's|^//go:build ||' \
  | tr ' ' '\n' | tr -d '()!' \
  | grep -E '^[A-Za-z0-9_.]+$' \
  | grep -vE '^(&&|\|\|)$' \
  | sort -u || true)

unknown=0
for t in $found; do
  is_excluded "$t" && continue
  hit=0
  for k in ${known[@]+"${known[@]}"}; do [ "$t" = "$k" ] && hit=1 && break; done
  if [ "$hit" -eq 0 ]; then
    echo "::error::unknown build tag '$t' is present in the source. Add it to the scripts/vet-build-tags.sh invocation" >&2
    unknown=1
  fi
done
[ "$unknown" -eq 0 ] || exit 1

for t in ${known[@]+"${known[@]}"}; do
  echo "== go vet -tags $t"
  go vet -tags "$t" ./...
done
