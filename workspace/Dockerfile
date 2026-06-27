# Workspace image: git + tmux + Workspace Agent + Claude CLI.
# Claude CLI はイメージに焼く（/usr/local、高速・オフライン起動）。ユーザー home の
# ~/.local に claude があれば PATH 優先で、entrypoint が自動更新する（焼いた版は固定の
# フォールバック / 新規 home の既定）。ツール本体は /usr/local、ユーザーデータは home(volume)。
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
      git tmux openssh-client ca-certificates curl ripgrep less procps tzdata \
 && rm -rf /var/lib/apt/lists/*

# Java is NOT baked into the image (it would add ~1.5GB). Instead the Temurin JDKs
# live once on the host (provisioned by deploy/local/provision-jvm.sh) and the
# Control Plane bind-mounts that dir read-only at /usr/lib/jvm into every workspace
# (WS_JVM_DIR), so they're shared. The entrypoint picks JAVA_HOME from there.
COPY --from=agentbuild /out/workspace-agent /usr/local/bin/workspace-agent
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Optional: install a vendored rtk binary (static) so the claude PreToolUse hook
# can route Bash through it for token savings. Other deployments leave vendor/
# empty (only .gitkeep) → no rtk, and the entrypoint seeds the hook only when rtk
# is present. The binary is git-ignored; run-dev.sh copies the host's rtk in.
COPY vendor/ /opt/vendor/
RUN if [ -f /opt/vendor/rtk ]; then \
      install -m 0755 /opt/vendor/rtk /usr/local/bin/rtk && echo "installed rtk"; \
    fi

# Claude CLI baked into the image (stable /usr/local install) for fast, offline
# container start. A user-home ~/.local install still takes PATH precedence and is
# auto-updated by the entrypoint; this is the pinned fallback / fresh-home default.
RUN npm install -g @anthropic-ai/claude-code && claude --version

# UTF-8 ロケール。未設定(POSIX)だと tmux が UTF-8 を無効化し日本語等が化ける。
# C.UTF-8 は locale 生成不要で Debian に存在する。
ENV LANG=C.UTF-8 LC_ALL=C.UTF-8

# node:22 の既定ユーザー node（uid/gid=1000）を dev にリネーム。Node.js と紛らわしい
# ため改名するが uid/gid=1000 は維持（ホストの uid 1000 と一致 → bind mount 素通し）。
# -m で home を /home/node → /home/dev へ移設（実行時は home が bind mount で上書きされる）。
RUN usermod -l dev -d /home/dev -m node && groupmod -n dev node

# claude は ~/.local/bin に入る。docker exec 等の既定 PATH に追加（非ログインシェル/Agent用）。
ENV PATH="/home/dev/.local/bin:${PATH}"
# ログインシェル(bash -l)は /etc/profile で PATH を上書きするため、端末で claude を確実に
# 見つけられるよう profile.d でも ~/.local/bin を前置する。
RUN echo 'export PATH="$HOME/.local/bin:$PATH"' > /etc/profile.d/claude-path.sh

USER dev
WORKDIR /home/dev
EXPOSE 7700
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["workspace-agent"]
