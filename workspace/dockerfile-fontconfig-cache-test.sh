#!/usr/bin/env bash
# The Dockerfile must delete /var/cache/fontconfig/* after the last step that can recreate
# it (apt-get, fc-cache) and before USER dev, or the release scanner can fail on random
# bytes in the cache (#2600).
#
#   workspace/dockerfile-fontconfig-cache-test.sh [Dockerfile]
set -eu
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DF="${1:-$HERE/Dockerfile}"

clean=$(grep -n '^RUN rm -rf /var/cache/fontconfig/\*' "$DF" | tail -1 | cut -d: -f1 || true)
[ -n "$clean" ] || { echo "NG: no 'RUN rm -rf /var/cache/fontconfig/*' in $DF"; exit 1; }
last=$(grep -nE '^(RUN|[[:space:]]).*(apt-get (install|update)|fc-cache|dpkg -i)' "$DF" | tail -1 | cut -d: -f1 || true)
user=$(grep -n '^USER dev' "$DF" | tail -1 | cut -d: -f1)
[ -n "$user" ] || { echo "NG: no USER dev in $DF"; exit 1; }
if [ -n "$last" ] && [ "$clean" -le "$last" ]; then
  echo "NG: fontconfig cache is removed at line $clean, before the last cache-producing step (line $last)"; exit 1
fi
[ "$clean" -lt "$user" ] || { echo "NG: cache removal (line $clean) is after USER dev (line $user)"; exit 1; }
echo "OK: fontconfig cache removed at line $clean (last producer: ${last:-none}, USER dev: $user)"
