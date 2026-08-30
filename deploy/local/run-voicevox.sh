#!/usr/bin/env bash
# Local dev: run the VOICEVOX engine (CPU build; Zundamon et al.) in docker.
# Used for spoken agent replies (docs/log/24 + ADR0013). The CP runs on the host, so
# publishing the engine on 127.0.0.1:50021 makes it reachable via the CP's default
# AF_VOICEVOX_URL.
#
#   deploy/local/run-voicevox.sh            # foreground (Ctrl-C to stop)
#   deploy/local/run-voicevox.sh -d         # background (--name af-voicevox)
#   docker stop af-voicevox && docker rm af-voicevox   # stop & discard
#
# The first run pulls the image (hundreds of MB) and takes a while. No GPU needed;
# resident memory is roughly ~1GB. In a shell without the docker group:
# `sg docker -c "deploy/local/run-voicevox.sh"`.
set -euo pipefail

IMAGE="${VOICEVOX_IMAGE:-voicevox/voicevox_engine:cpu-latest}"
PORT="${VOICEVOX_PORT:-50021}"
NAME="${VOICEVOX_NAME:-af-voicevox}"

# No CORS needed (CP-native: the browser talks to the CP, the CP talks to the
# engine). Loopback publish only.
ARGS=(--rm --name "$NAME" -p "127.0.0.1:${PORT}:50021")
if [ "${1:-}" = "-d" ]; then
  ARGS=(-d "${ARGS[@]}") # detach (keep --rm)
fi

echo "==> VOICEVOX engine: $IMAGE  ->  http://127.0.0.1:${PORT}  (reachable via the CP's default AF_VOICEVOX_URL)"
exec docker run "${ARGS[@]}" "$IMAGE"
