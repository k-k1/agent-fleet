# Workspace image: git + tmux + Workspace Agent.
# Claude CLI はイメージに焼き込まず、コンテナ起動時(entrypoint)に最新を取得して
# 永続ホーム(~/.local)へ置く。これで再ビルドなしに常に最新 CLI を使える。
# ツール本体は /usr/local（イメージ）、ユーザーデータと claude は home(volume)。
# docs/11-phase1-plan.md / docs/09-portability.md 参照。

# --- build the agent (Go) ---
FROM golang:1.26-bookworm AS agentbuild
WORKDIR /src
COPY agent/go.mod agent/go.sum ./
RUN go mod download
COPY agent/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/workspace-agent .

# --- runtime ---
FROM node:22-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      git tmux openssh-client ca-certificates curl ripgrep less procps \
 && rm -rf /var/lib/apt/lists/*
COPY --from=agentbuild /out/workspace-agent /usr/local/bin/workspace-agent
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# UTF-8 ロケール。未設定(POSIX)だと tmux が UTF-8 を無効化し日本語等が化ける。
# C.UTF-8 は locale 生成不要で Debian に存在する。
ENV LANG=C.UTF-8 LC_ALL=C.UTF-8

# claude は ~/.local/bin に入る。docker exec 等の既定 PATH に追加（非ログインシェル/Agent用）。
ENV PATH="/home/node/.local/bin:${PATH}"
# ログインシェル(bash -l)は /etc/profile で PATH を上書きするため、端末で claude を確実に
# 見つけられるよう profile.d でも ~/.local/bin を前置する。
RUN echo 'export PATH="$HOME/.local/bin:$PATH"' > /etc/profile.d/claude-path.sh

# node:22 の既定ユーザー node は uid/gid=1000（ホストの uid 1000 と一致 → bind mount 素通し）。
USER node
WORKDIR /home/node
EXPOSE 7700
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["workspace-agent"]
