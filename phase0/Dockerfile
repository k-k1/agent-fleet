# Phase 0 PoC: Workspace イメージ（最小）
# 目的: ヘッドレスコンテナで claude /login が通るかを検証する。
# 設計原則: ツールはイメージ（/usr/local 等）に、ユーザーデータはホーム(volume)に。
#           → ホームを丸ごと永続化しても CLI が shadow されない。
FROM node:22-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      git tmux openssh-client ca-certificates ripgrep less procps nano \
 && rm -rf /var/lib/apt/lists/*

# Claude Code 本体（npm グローバル = /usr/local/bin/claude、ホーム外）
RUN npm install -g @anthropic-ai/claude-code

# node:22 イメージの既定ユーザー `node` は uid=1000/gid=1000。
# ホスト(uid 1000)と一致するため bind mount したホームを素通しで読み書きできる。
USER node
WORKDIR /home/node

# Agent 実装前の PoC では常駐だけさせ、`docker compose exec` で入って手動操作する。
CMD ["sleep", "infinity"]
