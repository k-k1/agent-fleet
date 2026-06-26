# Workspace image: Claude CLI + git + tmux + Workspace Agent.
# 設計原則: ツールは /usr/local（イメージ）に置き、ホーム(volume)を丸ごと永続化しても
# CLI が shadow されないようにする。docs/11-phase1-plan.md / docs/09-portability.md 参照。

# --- build the agent (Go) ---
FROM golang:1.26-bookworm AS agentbuild
WORKDIR /src
COPY agent/go.mod agent/go.sum ./
RUN go mod download
COPY agent/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/workspace-agent .

# --- runtime ---
FROM node:22-bookworm-slim
# claude 同梱はビルド時に切替可能。メモリ制約下の検証では INSTALL_CLAUDE=0 で軽くする。
ARG INSTALL_CLAUDE=1
RUN apt-get update && apt-get install -y --no-install-recommends \
      git tmux openssh-client ca-certificates ripgrep less procps \
 && rm -rf /var/lib/apt/lists/*
RUN if [ "$INSTALL_CLAUDE" = "1" ]; then npm install -g @anthropic-ai/claude-code; fi
COPY --from=agentbuild /out/workspace-agent /usr/local/bin/workspace-agent

# node:22 の既定ユーザー node は uid/gid=1000（ホストの uid 1000 と一致 → bind mount 素通し）。
USER node
WORKDIR /home/node
EXPOSE 7700
CMD ["workspace-agent"]
