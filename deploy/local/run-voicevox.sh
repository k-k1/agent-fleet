#!/usr/bin/env bash
# ローカル dev 用: VOICEVOX エンジン（CPU 版・ずんだもん等）を docker で起動する。
# エージェント回答の音声読み上げ（docs/24 + ADR0013）に使う。CP はホスト起動なので、
# エンジンを 127.0.0.1:50021 に publish すれば CP の既定 AF_VOICEVOX_URL で到達できる。
#
#   deploy/local/run-voicevox.sh            # 前景で起動（Ctrl-C で停止）
#   deploy/local/run-voicevox.sh -d         # バックグラウンド（--name af-voicevox）
#   docker stop af-voicevox && docker rm af-voicevox   # 停止・破棄
#
# 初回はイメージ pull（数百 MB〜）で時間がかかる。GPU は不要。メモリは常駐 ~1GB 程度。
# docker グループが無いシェルでは `sg docker -c "deploy/local/run-voicevox.sh"`。
set -euo pipefail

IMAGE="${VOICEVOX_IMAGE:-voicevox/voicevox_engine:cpu-latest}"
PORT="${VOICEVOX_PORT:-50021}"
NAME="${VOICEVOX_NAME:-af-voicevox}"

# CORS は不要（CP-native: ブラウザは CP を叩き、CP がエンジンを叩く）。loopback publish のみ。
ARGS=(--rm --name "$NAME" -p "127.0.0.1:${PORT}:50021")
if [ "${1:-}" = "-d" ]; then
  ARGS=(-d "${ARGS[@]}") # detach（--rm は維持）
fi

echo "==> VOICEVOX engine: $IMAGE  ->  http://127.0.0.1:${PORT}  (CP: AF_VOICEVOX_URL 既定で到達)"
exec docker run "${ARGS[@]}" "$IMAGE"
