#!/usr/bin/env bash
# Stub test for deploy/aws/ecs/harness/teardown.sh's Cloud Map sweep (#3555).
#
# A fake `aws` stands in for an account that holds the harness's namespace AND another
# deployment's stack-owned Cloud Map services (50-tts / 60-engines). Like the real API, an
# unfiltered `servicediscovery list-services` returns every service in the account and region;
# only `--filters Name=NAMESPACE_ID,...` narrows it. The sweep must delete the harness's own
# services and nothing else.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"
LOG="$WORK/calls.log"; : > "$LOG"

cat > "$WORK/bin/aws" <<'STUB'
#!/usr/bin/env bash
args="$*"
echo "$args" >> "$STUB_LOG"
case "$args" in
  *"servicediscovery list-namespaces"*) echo "ns-harness" ;;
  *"servicediscovery list-services"*)
    case "$args" in
      *"NAMESPACE_ID,Values=ns-harness"*) echo "srv-harness-1 srv-harness-2" ;;
      *"--filters"*) ;;                                    # another namespace: nothing
      *) echo "srv-harness-1 srv-harness-2 srv-stack-tts srv-stack-llm srv-stack-image" ;;
    esac ;;
  *"servicediscovery delete-service"*) ;;
esac
exit 0
STUB
printf '#!/bin/sh\nexit 0\n' > "$WORK/bin/sleep"
chmod +x "$WORK/bin/aws" "$WORK/bin/sleep"

STUB_LOG="$LOG" PATH="$WORK/bin:$PATH" bash "$ROOT/deploy/aws/ecs/harness/teardown.sh" > "$WORK/out" 2>&1 \
  || { cat "$WORK/out"; echo "NG: teardown.sh failed"; exit 1; }

for sv in srv-harness-1 srv-harness-2; do
  grep -qF "servicediscovery delete-service --id $sv" "$LOG" \
    || { cat "$LOG"; echo "NG: the harness's own service $sv was not deleted"; exit 1; }
done
if grep -E "servicediscovery delete-service --id srv-stack-" "$LOG"; then
  echo "NG: a stack-owned Cloud Map service was deleted"; exit 1
fi
echo "OK: harness teardown deletes only its own namespace's Cloud Map services"
