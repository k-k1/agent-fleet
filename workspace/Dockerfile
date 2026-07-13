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
      git git-lfs subversion tmux vim openssh-client ca-certificates gnupg curl wget less procps htop strace tzdata \
      ripgrep fd-find bat jq unzip zip \
      build-essential pkg-config \
      python3 python3-pip python3-venv python3-dev python-is-python3 \
 && ln -sf "$(command -v fdfind)" /usr/local/bin/fd \
 && ln -sf "$(command -v batcat)" /usr/local/bin/bat \
 && rm -rf /var/lib/apt/lists/* \
 && git lfs install --system --skip-repo
# Beyond the original baseline this adds a C/C++ toolchain (build-essential +
# pkg-config + python3-dev) so cgo / node-gyp / source-built wheels compile, plus
# common utilities (wget, jq, unzip/zip, gnupg, htop), fd/bat (Debian ships them
# as fdfind/batcat; symlinked to the expected names), and subversion for svn repos
# (HTTPS via libserf, pulled as a dep). git-delta is not in bookworm, so it's
# intentionally omitted (add via its GitHub .deb if wanted).
# Python 3 (bookworm = 3.11) baked in as a baseline tool, like git/tmux. python-is-
# python3 provides `python`. Debian (PEP 668) marks the system env externally-managed,
# which blocks `pip install` into it; allow break-system-packages so `pip install
# --user <pkg>` lands in the persistent home (~/.local, on PATH). venvs work as usual.
RUN printf '[global]\nbreak-system-packages = true\n' > /etc/pip.conf

# uv/uvx (Astral) baked as a baseline tool: it is the launcher for the ops MCP
# servers the SRE assistant uses (docs/25 — `workspace-agent mcp-run pagerduty`
# execs `uvx pagerduty-mcp`). Installed to /usr/local/bin so it's on PATH for
# every user without a per-home setup step; uvx caches fetched servers under the
# persistent ~/.cache after first run.
RUN pip install --no-cache-dir uv \
    && uv --version && uvx --version

# mcp-grafana (official Grafana MCP server, Go single binary) baked for the ops
# integrations (docs/25 — `workspace-agent mcp-run grafana` execs it with the
# stored URL + service-account token and -disable-write -disable-admin). Works
# against self-hosted / Grafana Cloud / Amazon Managed Grafana. Bump
# MCP_GRAFANA_VERSION to upgrade (ARG pin busts the cache; same idiom as GH_VERSION).
ARG MCP_GRAFANA_VERSION=0.17.1
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) mgarch=x86_64 ;; arm64) mgarch=arm64 ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac; \
    curl -fsSL "https://github.com/grafana/mcp-grafana/releases/download/v${MCP_GRAFANA_VERSION}/mcp-grafana_Linux_${mgarch}.tar.gz" -o /tmp/mcp-grafana.tgz; \
    tar -C /tmp -xzf /tmp/mcp-grafana.tgz mcp-grafana; \
    install -m 0755 /tmp/mcp-grafana /usr/local/bin/mcp-grafana; \
    rm -f /tmp/mcp-grafana.tgz /tmp/mcp-grafana; \
    mcp-grafana --version

# awslabs CloudWatch MCP server (Python) baked as a uv tool so `workspace-agent
# mcp-run cloudwatch` starts instantly with no PyPI fetch at runtime (docs/25
# §5-5 (d)). UV_PYTHON_DOWNLOADS=never + --python pin the SYSTEM python: without
# them uv drops a managed interpreter into root's home, unreadable by the
# runtime user. The venv + entrypoint under /usr/local are root-owned and
# world-readable, which is all the runtime user needs.
ARG CLOUDWATCH_MCP_VERSION=0.1.4
RUN set -eux; \
    UV_TOOL_DIR=/usr/local/share/uv/tools UV_TOOL_BIN_DIR=/usr/local/bin \
    UV_PYTHON_DOWNLOADS=never UV_NO_CACHE=1 \
    uv tool install --python /usr/bin/python3 "awslabs.cloudwatch-mcp-server==${CLOUDWATCH_MCP_VERSION}"; \
    test -x /usr/local/bin/awslabs.cloudwatch-mcp-server

# Headless-browser verification support: the shared libraries Chromium needs at
# runtime, plus fonts (DejaVu + Noto CJK so the Console's Japanese UI renders in
# screenshots instead of tofu). The browser binary itself is NOT baked — this
# image is root-only at build time and these libs were the one thing an agent
# could not install itself (no sudo at runtime; hand-extracting .debs cost a
# session ~30min). Each user fetches the browser once into the persistent home:
#   npm i playwright-core && npx playwright-core install chromium
# (~/.cache/ms-playwright survives container recreation; version tracks whatever
# playwright the task needs). Keep runs headless and short-lived — the shared
# host is memory-constrained (workspace-notes).
RUN apt-get update && apt-get install -y --no-install-recommends \
      libglib2.0-0 libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libatspi2.0-0 \
      libcups2 libdbus-1-3 libdrm2 libgbm1 libasound2 libpango-1.0-0 libcairo2 \
      libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 libxfixes3 libxi6 \
      libxrandr2 libxkbcommon0 libxshmfence1 \
      fonts-dejavu-core fonts-noto-cjk \
 && rm -rf /var/lib/apt/lists/*
# git-lfs filters live in the system gitconfig (/etc/gitconfig) so clones/checkouts
# smudge LFS objects for every user regardless of home state. LFS auth reuses the
# HTTPS credential helper (same host token), so private LFS objects fetch too.

# Java is NOT baked into the image (it would add ~1.5GB). Instead the Temurin JDKs
# live once on the host (provisioned by deploy/local/provision-jvm.sh) and the
# Control Plane bind-mounts that dir read-only at /usr/lib/jvm into every workspace
# (WS_JVM_DIR), so they're shared. The entrypoint picks JAVA_HOME from there.
# ── 重い・滅多に変わらないレイヤを先に置く（Go toolchain / agent CLI / リネーム）。
# entrypoint や workspace-notes など変更頻度の高い COPY はこの後ろに集約し、小修正で
# これらがキャッシュ無効化されないようにする（layer ordering = ビルド時間最適化）。

# Go toolchain from the official pinned tarball (decoupled from the agent builder
# image; arch auto-detected). Pure-Go builds work as-is; cgo / node-gyp / native
# Python wheels use the C toolchain (build-essential) installed above. `go install`
# binaries and the module cache land under ~/go (home volume → persist). Keep
# GO_VERSION in step with agent/go.mod's `go` directive; bump here to upgrade.
ARG GO_VERSION=1.26.4
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) goarch=amd64 ;; arm64) goarch=arm64 ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz" -o /tmp/go.tgz; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz; \
    /usr/local/go/bin/go version

# AWS CLI v2 + Session Manager plugin for SSM login sessions (kind="ssm"): a member
# opens a browser terminal that runs `aws sso login` (device-code URL surfaced in the
# terminal) + `aws ssm start-session` against their OWN AWS account. NO AWS credentials
# are baked or stored anywhere — auth is per-session via SSO and cached in the home
# volume; the Control Plane only resolves non-secret host bookmarks. arch auto-detected.
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) awsarch=x86_64; smparch=64bit ;; \
      arm64) awsarch=aarch64; smparch=arm64 ;; \
      *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${awsarch}.zip" -o /tmp/awscliv2.zip; \
    unzip -q /tmp/awscliv2.zip -d /tmp; \
    /tmp/aws/install; \
    curl -fsSL "https://s3.amazonaws.com/session-manager-downloads/plugin/latest/ubuntu_${smparch}/session-manager-plugin.deb" -o /tmp/smp.deb; \
    dpkg -i /tmp/smp.deb; \
    rm -rf /tmp/awscliv2.zip /tmp/aws /tmp/smp.deb; \
    aws --version && session-manager-plugin --version

# GitHub CLI (gh) from the official pinned release (arch auto-detected). Baked into the
# image so `gh` (PR/issue ops, `gh auth`, API) is available on a fresh container without a
# per-home download. Bump GH_VERSION to upgrade.
# 実体は /usr/local/libexec/gh に置き、/usr/local/bin/gh は透過認証ラッパー
# (gh-auth-wrapper.sh) にする。ラッパーが呼び出しのたびに git と同一の cred helper
# からトークンを取り出し GH_TOKEN に注入するので、`gh auth login` 不要で gh が使える
# （docs/dev/08 §8.3）。ラッパー本体の COPY は末尾の高頻度 COPY 群でまとめて行う。
ARG GH_VERSION=2.96.0
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64|arm64) gharch="$arch" ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac; \
    curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${gharch}.tar.gz" -o /tmp/gh.tgz; \
    tar -C /tmp -xzf /tmp/gh.tgz; \
    install -D -m 0755 "/tmp/gh_${GH_VERSION}_linux_${gharch}/bin/gh" /usr/local/libexec/gh; \
    rm -rf /tmp/gh.tgz "/tmp/gh_${GH_VERSION}_linux_${gharch}"; \
    /usr/local/libexec/gh --version

# Coding-agent CLIs baked into the image (stable /usr/local install) for fast,
# offline container start. claude: a user-home ~/.local install still takes PATH
# precedence and is auto-updated by the entrypoint; this is the pinned fallback /
# fresh-home default. opencode: an alternative agent (kind="opencode"); its auth
# (opencode auth login) and sessions persist under ~/.local/share/opencode (home).
# codex: OpenAI Codex CLI (kind="codex"); auth (codex login --with-api-key /
# --device-auth) writes ~/.codex/auth.json and sessions land in ~/.codex (home).
# 版は ARG でピン止め（GH_VERSION / GO_VERSION と同じ流儀）。bump は下の ARG を書き
# 換えて再ビルド → 各 Workspace を Stop→Start。latest の確認は `npm view <pkg> version`。
# 未ピン時代は同一 RUN 行が Docker レイヤキャッシュに当たり、再ビルドしても古い版の
# ままになる罠があった（ピン止めなら ARG 変更が確実にキャッシュを破る）。
ARG CLAUDE_CODE_VERSION=2.1.207
ARG OPENCODE_VERSION=1.17.18
ARG CODEX_VERSION=0.144.1
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
      "opencode-ai@${OPENCODE_VERSION}" \
      "@openai/codex@${CODEX_VERSION}" \
 && claude --version && opencode --version && codex --version

# ビルド時ピンをコンテナ内へ書き出す（ARG はビルド後に消えて実行時に参照できない）。
# 設定 UI「ツールのバージョン」の「ピン vs 実体」表示と、スモークの検証材料に使う。
RUN mkdir -p /usr/local/share/agent-fleet \
 && printf '{"claude":"%s","opencode":"%s","codex":"%s","go":"%s","gh":"%s"}\n' \
      "$CLAUDE_CODE_VERSION" "$OPENCODE_VERSION" "$CODEX_VERSION" "$GO_VERSION" "$GH_VERSION" \
      > /usr/local/share/agent-fleet/versions.json

# The baked claude is a root-owned npm-global install run as the non-root `dev`
# user, so Claude Code's background auto-updater can never write to
# /usr/local/lib/node_modules and prints "auto-update failed" on every start. We
# manage the claude version through image rebuilds (and the entrypoint still runs
# `claude update` for a native ~/.local install), so the background updater is both
# redundant and impossible — disable just it (DISABLE_AUTOUPDATER, not
# DISABLE_UPDATES, so explicit `claude update`/`claude install` still work).
ENV DISABLE_AUTOUPDATER=1

# Hand the npm-global tree to `dev` so an opted-in member can update the baked CLIs
# (claude/opencode/codex) to latest IN PLACE at container start (entrypoint runs as
# dev). Single-user container, so dev owning its own /usr/local node_modules is no
# escalation. Gated by the operator (AF_AGENT_SELF_UPDATE_ALLOWED); Stop→Start
# recreates from the image, so turning the toggle off reverts to these baked versions.
# Numeric uid:gid 1000 (the node/dev user; the node→dev rename happens further down).
RUN chown -R 1000:1000 /usr/local/lib/node_modules /usr/local/bin

# UTF-8 ロケール。未設定(POSIX)だと tmux が UTF-8 を無効化し日本語等が化ける。
# C.UTF-8 は locale 生成不要で Debian に存在する。
ENV LANG=C.UTF-8 LC_ALL=C.UTF-8

# tmux: attach したクライアントを1セッションに閉じ込める設定。/etc は bind mount で
# 上書きされない(=home と違い確実に残る)ため /etc/tmux.conf に置く。agent が最初に
# tmux を叩いてサーバ起動する時点で読まれる。
COPY tmux.conf /etc/tmux.conf

# node:22 の既定ユーザー node（uid/gid=1000）を dev にリネーム。Node.js と紛らわしい
# ため改名するが uid/gid=1000 は維持（ホストの uid 1000 と一致 → bind mount 素通し）。
# -m で home を /home/node → /home/dev へ移設（実行時は home が bind mount で上書きされる）。
RUN usermod -l dev -d /home/dev -m node && groupmod -n dev node

# claude は ~/.local/bin に入る。docker exec 等の既定 PATH に追加（非ログインシェル/Agent用）。
# Go は /usr/local/go/bin（go 本体）と ~/go/bin（go install 先）を前置。
ENV PATH="/home/dev/.local/bin:/home/dev/go/bin:/usr/local/go/bin:${PATH}"
# ログインシェル(bash -l)は /etc/profile で PATH を上書きするため、端末で claude / go を
# 確実に見つけられるよう profile.d でも前置する。
RUN echo 'export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"' > /etc/profile.d/claude-path.sh

# ── ここから下は変更頻度の高い入力（COPY 主体・低コスト）。上の go/npm レイヤを
# 無効化しないよう末尾に集約する。すべて root 操作なので USER dev の前に置く。
COPY --from=agentbuild /out/workspace-agent /usr/local/bin/workspace-agent
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
# gh 透過認証ラッパー（実体は /usr/local/libexec/gh）。§8.3 参照。
COPY gh-auth-wrapper.sh /usr/local/bin/gh
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/gh
# opencode plugins source (seeded into ~/.config/opencode/plugin at runtime):
# agent-fleet-status.js (session working/idle bridge — seeded by the entrypoint) +
# rtk.ts (token-saving command rewrite, the opencode analog of claude's rtk hook —
# seeded/removed by the agent per the ~/.config/agent-fleet/rtk.json toggle).
COPY opencode-plugin/ /usr/local/share/agent-fleet/opencode-plugin/

# Workspace 利用ガイド（やってはいけないこと等）を各エージェントの「常時読込」位置へ。
# claude は managed-policy ファイル /etc/claude-code/CLAUDE.md（毎セッション読込・除外不可）
# として焼き込み、codex/opencode 分は home 永続なので entrypoint が毎起動で seed する。
COPY workspace-notes.md /usr/local/share/agent-fleet/workspace-notes.md
RUN mkdir -p /etc/claude-code \
 && cp /usr/local/share/agent-fleet/workspace-notes.md /etc/claude-code/CLAUDE.md

# Optional: install a vendored rtk binary (static) so the claude PreToolUse hook
# can route Bash through it for token savings. Other deployments leave vendor/
# empty (only .gitkeep) → no rtk, and the entrypoint seeds the hook only when rtk
# is present. The binary is git-ignored; run-dev.sh copies the host's rtk in.
COPY vendor/ /opt/vendor/
RUN if [ -f /opt/vendor/rtk ]; then \
      install -m 0755 /opt/vendor/rtk /usr/local/bin/rtk && echo "installed rtk"; \
    fi

USER dev
WORKDIR /home/dev
EXPOSE 7700
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["workspace-agent"]
