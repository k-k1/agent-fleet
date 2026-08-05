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
# VERSION stamps the binary (startup log, docs/35 §35.6.1); only the release
# pipeline passes a real value — dev builds stay "dev".
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-X main.buildVersion=${VERSION}" -o /out/workspace-agent .

# --- runtime ---
FROM node:22-bookworm-slim
# Links the GHCR package to this repository — see control-plane/Dockerfile for why.
LABEL org.opencontainers.image.source="https://github.com/k-k1/agent-fleet" \
      org.opencontainers.image.description="Agent Fleet per-user workspace — isolated container running the agent CLIs" \
      org.opencontainers.image.licenses="Apache-2.0"
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

# --- 配布ノブ（docs/35 §35.4.1・§35.7.1-5） ----------------------------------
# BAKE_AGENT_CLIS=0  … エージェント CLI（claude/opencode/codex/copilot/cursor/agy/rtk）を
#   焼かない lean variant（ライセンス理由: プロプライエタリ CLI は再配布不可扱い）。
#   entrypoint の boot-install が versions.json のピン版を初回起動時に ~/.local へ導入。
# BAKE_OPTIONAL_TOOLS=0 … chromium+CJK フォント・Go・awscli+SSM plugin・ops MCP 群を
#   焼かない（サイズ理由・native rootfs 用。chromium の実行時ライブラリと
#   fontconfig/DejaVu だけは残し、chromium 本体はオンデマンド DL — P2）。
# 既定 BAKE_AGENT_CLIS=0 = プロプライエタリ CLI を焼かない安全既定。素の `docker build`
#   でも再配布不可の CLI をイメージに含めないため、うっかり配布しても事故らない。CLI も
#   焼いて初回起動を速くしたい自社デプロイは BAKE_AGENT_CLIS=1 を明示（run-dev.sh も対応）。
# 既定 BAKE_OPTIONAL_TOOLS=1 = サイズ系ツール（chromium/CJK フォント・Go・awscli 等）は
#   従来どおり焼き込み（ライセンス無関係・native lean rootfs だけが 0）。
ARG BAKE_AGENT_CLIS=0
ARG BAKE_OPTIONAL_TOOLS=1

# mcp-grafana (official Grafana MCP server, Go single binary) baked for the ops
# integrations (docs/25 — `workspace-agent mcp-run grafana` execs it with the
# stored URL + service-account token and -disable-write -disable-admin). Works
# against self-hosted / Grafana Cloud / Amazon Managed Grafana. Bump
# MCP_GRAFANA_VERSION to upgrade (ARG pin busts the cache; same idiom as GH_VERSION).
ARG MCP_GRAFANA_VERSION=0.17.1
RUN if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) mgarch=x86_64 ;; arm64) mgarch=arm64 ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac; \
    curl -fsSL "https://github.com/grafana/mcp-grafana/releases/download/v${MCP_GRAFANA_VERSION}/mcp-grafana_Linux_${mgarch}.tar.gz" -o /tmp/mcp-grafana.tgz; \
    tar -C /tmp -xzf /tmp/mcp-grafana.tgz mcp-grafana; \
    install -m 0755 /tmp/mcp-grafana /usr/local/bin/mcp-grafana; \
    rm -f /tmp/mcp-grafana.tgz /tmp/mcp-grafana; \
    mcp-grafana --version; \
    fi

# awslabs CloudWatch MCP server (Python) baked as a uv tool so `workspace-agent
# mcp-run cloudwatch` starts instantly with no PyPI fetch at runtime (docs/25
# §5-5 (d)). UV_PYTHON_DOWNLOADS=never + --python pin the SYSTEM python: without
# them uv drops a managed interpreter into root's home, unreadable by the
# runtime user. The venv + entrypoint under /usr/local are root-owned and
# world-readable, which is all the runtime user needs.
ARG CLOUDWATCH_MCP_VERSION=0.1.4
RUN if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then set -eux; \
    UV_TOOL_DIR=/usr/local/share/uv/tools UV_TOOL_BIN_DIR=/usr/local/bin \
    UV_PYTHON_DOWNLOADS=never UV_NO_CACHE=1 \
    uv tool install --python /usr/bin/python3 "awslabs.cloudwatch-mcp-server==${CLOUDWATCH_MCP_VERSION}"; \
    test -x /usr/local/bin/awslabs.cloudwatch-mcp-server; \
    fi

# AWS MCP proxy (Agent Toolkit for AWS — docs/25 §AWS MCP) baked the same way:
# `workspace-agent mcp-run aws` execs it to reach the AWS-operated MCP Server over
# SigV4. Worth baking rather than leaving to uvx: the dependency tree is botocore +
# awscrt (~20MB of wheels), so a cold fetch is the difference between an MCP server
# that starts and one the CLI gives up on.
ARG AWS_MCP_PROXY_VERSION=1.6.4
RUN if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then set -eux; \
    UV_TOOL_DIR=/usr/local/share/uv/tools UV_TOOL_BIN_DIR=/usr/local/bin \
    UV_PYTHON_DOWNLOADS=never UV_NO_CACHE=1 \
    uv tool install --python /usr/bin/python3 "mcp-proxy-for-aws==${AWS_MCP_PROXY_VERSION}"; \
    test -x /usr/local/bin/mcp-proxy-for-aws; \
    fi

# Chromium for the in-container browser pane and headless UI verification. Debian's
# package supplies the same /usr/bin/chromium entrypoint on amd64 and arm64 and pulls
# its runtime libraries as dependencies. Pin all three Chromium packages together:
# chromium-common requires an exact match and chromium-sandbox is normally only a
# recommendation (suppressed by --no-install-recommends). Bump the ARG after Debian
# publishes the same security build for both architectures. Fonts and fontconfig are
# explicit so Japanese pages render without depending on a transient recommendation.
ARG CHROMIUM_VERSION=151.0.7922.71-1~deb12u1
# BAKE_OPTIONAL_TOOLS=0（native lean rootfs）は chromium 本体・CJK フォントを焼かず
# オンデマンド DL（P2 の install-chromium、CHROMIUM_DL_VERSION ピン）に回すが、
# その DL 版が動くための実行時ライブラリと fontconfig/DejaVu はここで明示導入する
# （docs/35 §35.7.1-5。ライブラリ一覧は playwright の Debian 12 chromium deps 準拠）。
RUN apt-get update \
 && if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then \
      apt-get install -y --no-install-recommends \
        "chromium=${CHROMIUM_VERSION}" \
        "chromium-common=${CHROMIUM_VERSION}" \
        "chromium-sandbox=${CHROMIUM_VERSION}" \
        fontconfig fonts-dejavu-core fonts-noto-cjk \
      && chromium --version \
      && test "$(dpkg-query -W -f='${Version}' chromium)" = "$CHROMIUM_VERSION" \
      && test "$(stat -c '%u:%g:%a' /usr/lib/chromium/chrome-sandbox)" = "0:0:4755"; \
    else \
      apt-get install -y --no-install-recommends \
        fontconfig fonts-dejavu-core \
        libasound2 libatk-bridge2.0-0 libatk1.0-0 libatspi2.0-0 libcairo2 \
        libcups2 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0 libnspr4 libnss3 \
        libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 \
        libxfixes3 libxkbcommon0 libxrandr2; \
    fi \
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
RUN if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) goarch=amd64 ;; arm64) goarch=arm64 ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz" -o /tmp/go.tgz; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz; \
    /usr/local/go/bin/go version; \
    fi

# AWS CLI v2 + Session Manager plugin for SSM login sessions (kind="ssm"): a member
# opens a browser terminal that runs `aws sso login` (device-code URL surfaced in the
# terminal) + `aws ssm start-session` against their OWN AWS account. NO AWS credentials
# are baked or stored anywhere — auth is per-session via SSO and cached in the home
# volume; the Control Plane only resolves non-secret host bookmarks. arch auto-detected.
# 版は ARG でピン止め（versioned URL — かつては latest URL で未ピンだった。docs/35
# §35.9-7(c)。bump は下の ARG を書き換えて再ビルド）。
ARG AWSCLI_VERSION=2.36.4
ARG SESSION_MANAGER_PLUGIN_VERSION=1.2.835.0
RUN if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) awsarch=x86_64; smparch=64bit ;; \
      arm64) awsarch=aarch64; smparch=arm64 ;; \
      *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${awsarch}-${AWSCLI_VERSION}.zip" -o /tmp/awscliv2.zip; \
    unzip -q /tmp/awscliv2.zip -d /tmp; \
    /tmp/aws/install; \
    curl -fsSL "https://s3.amazonaws.com/session-manager-downloads/plugin/${SESSION_MANAGER_PLUGIN_VERSION}/ubuntu_${smparch}/session-manager-plugin.deb" -o /tmp/smp.deb; \
    dpkg -i /tmp/smp.deb; \
    rm -rf /tmp/awscliv2.zip /tmp/aws /tmp/smp.deb; \
    aws --version && session-manager-plugin --version; \
    fi

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

# rtk (token-saving claude Bash hook): fetched pinned at build time, ALWAYS baked
# by default — the former host-vendoring path (run-dev.sh copying a host binary into
# vendor/) is gone, so every deployment gets the same pinned rtk like the other
# CLIs. BAKE_RTK=0 remains as an escape hatch for airgapped builds (no build-time
# GitHub fetch); the entrypoint seeds the hook only when `rtk` is on PATH.
# rtk-ai/rtk is public, so the asset + checksums download without auth. Static musl
# (amd64) / gnu (arm64) binary → runs in the slim container.
# Bump RTK_VERSION to upgrade (ARG change busts the cache; same idiom as GH_VERSION).
# The AF_AGENT_SELF_UPDATE opt-in also updates rtk in place at start (entrypoint).
# BAKE_AGENT_CLIS=0（lean 配布 variant）は rtk も焼かない — entrypoint の boot-install
# が versions.json の rtk ピンで ~/.local/bin へ導入する（docs/35 §35.4.1）。
ARG BAKE_RTK=1
ARG RTK_VERSION=0.44.2
RUN if [ "${BAKE_RTK}" = "1" ] && [ "${BAKE_AGENT_CLIS}" = "1" ]; then set -eux; \
      arch="$(dpkg --print-architecture)"; \
      case "$arch" in \
        amd64) asset="rtk-x86_64-unknown-linux-musl.tar.gz" ;; \
        arm64) asset="rtk-aarch64-unknown-linux-gnu.tar.gz" ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
      esac; \
      base="https://github.com/rtk-ai/rtk/releases/download/v${RTK_VERSION}"; \
      cd /tmp; \
      curl -fsSL "${base}/${asset}" -o "${asset}"; \
      curl -fsSL "${base}/checksums.txt" -o checksums.txt; \
      grep " ${asset}\$" checksums.txt | sha256sum -c -; \
      tar xzf "${asset}"; \
      install -m 0755 rtk /usr/local/bin/rtk; \
      rm -f "${asset}" checksums.txt rtk; \
      rtk --version; \
    fi

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
ARG CLAUDE_CODE_VERSION=2.1.222
ARG OPENCODE_VERSION=1.18.13
ARG CODEX_VERSION=0.146.0
ARG COPILOT_VERSION=1.0.78
RUN if [ "${BAKE_AGENT_CLIS}" = "1" ]; then \
      npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
        "opencode-ai@${OPENCODE_VERSION}" \
        "@openai/codex@${CODEX_VERSION}" \
        "@github/copilot@${COPILOT_VERSION}" \
      && claude --version && opencode --version && codex --version && copilot --version; \
    fi

# copilot（kind="copilot"、docs/36）: 自己更新は既定 ON（CI env 検出時のみ自動 OFF）
# — フリートの版管理はイメージ再ビルドが正なので明示的に封殺する（claude の
# DISABLE_AUTOUPDATER・agy の AGY_CLI_DISABLE_AUTO_UPDATE と同じ理屈。ユーザーの
# 明示的な `copilot update` は可能なまま）。
ENV COPILOT_AUTO_UPDATE=false

# Antigravity CLI（kind="agy"、docs/decisions/0008 / docs/32）。npm でなく Go バイナリ
# 配布なので別 RUN。かつては install.sh（常に latest manifest・版ピン不可）だったが、
# 1.1.7以降は GitHub Releases が追随しないため、公式installerが返す不変のGCS
# object（version-build-idを含むURL）を直接取得する。sha256 はその公式manifestの
# sha512検証済みアーカイブから計算して二重に固定する。bump は AGY_VERSION、
# AGY_RELEASE_BUILD と両archのshaを揃えて書き換える。
# 注意: agy は Go BoringCrypto(FIPS) ビルドで、x86 では RDRAND 命令が無いホストだと
# 起動即 SIGABRT する（0008 PoC: Ryzen Embedded R2514）。RDRAND 非提示のビルドホスト
# でもイメージは焼けるよう、起動を伴う --version 検証は rdrand 提示時だけ行う。
ARG AGY_VERSION=1.1.10
ARG AGY_RELEASE_BUILD=6423386432339968
ARG AGY_SHA256_X64=9e94cfffa869ee2bf9ab32e05eff3baa4661b66d5e00a2fd40a8c5dc6f9bd450
ARG AGY_SHA256_ARM64=4308f85b1dd4f6b5654ea5f7a0962a482f33acf8169b2e6ac4aecdb209d13411
RUN if [ "${BAKE_AGENT_CLIS}" = "1" ]; then set -eux; \
      arch="$(dpkg --print-architecture)"; \
      case "$arch" in \
        amd64) asset="linux-x64/cli_linux_x64.tar.gz"; sha="${AGY_SHA256_X64}" ;; \
        arm64) asset="linux-arm/cli_linux_arm64.tar.gz"; sha="${AGY_SHA256_ARM64}" ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
      esac; \
      curl -fsSL "https://storage.googleapis.com/antigravity-public/antigravity-cli/${AGY_VERSION}-${AGY_RELEASE_BUILD}/${asset}" -o /tmp/agy.tgz; \
      echo "${sha}  /tmp/agy.tgz" | sha256sum -c -; \
      tar -C /tmp -xzf /tmp/agy.tgz antigravity; \
      install -m 0755 /tmp/antigravity /usr/local/bin/agy; \
      rm -f /tmp/agy.tgz /tmp/antigravity; \
      if grep -qw rdrand /proc/cpuinfo; then agy --version; \
      else echo "skip agy --version check (build host exposes no rdrand)"; fi; \
    fi

# agy はインストール済みバイナリを通常実行中にバックグラウンド自己更新する
# （install.sh の明記仕様）。版はイメージ再ビルドで管理するので封じる — claude の
# DISABLE_AUTOUPDATER と同じ理屈（明示的な `agy update` は引き続き可能）。
ENV AGY_CLI_DISABLE_AUTO_UPDATE=1

# Cursor CLI（kind="cursor"、docs/40 / docs/decisions/0023）。npm でも単一 Go バイナリ
# でもなく、版付き tarball の Node.js バンドル（bundled node ＋ webpack chunk 群 ＋
# node_sqlite3/pty/ripgrep 同梱）。上流 `curl cursor.com/install | bash` は版ピンされた
# スクリプトを配り、`downloads.cursor.com/lab/<版>/linux/<arch>/agent-cli-package.tar.gz`
# を `~/.local/share/cursor-agent/versions/<版>/` へ `tar --strip-components=1` 展開し、
# `~/.local/bin/{agent,cursor-agent}` へ symlink する（wrapper は realpath で自分の版
# ディレクトリの node/index.js を解決）。焼き込みは同レイアウトを /usr/local に置いて
# root 所有にする（dev ユーザは書けない）。バレ名 `agent` は PATH 衝突しやすいので
# /usr/local/bin には `cursor-agent` だけを張る（AF は cursor-agent のみ呼ぶ）。
#
# 版数は日付形式（2026.07.20-8cc9c0b）で semver でない — 版比較ロジックは要注意
# （rtk-always-baked と同類。e2e-smoke は版文字列を丸ごと突き合わせる）。上流は
# チェックサムを公開しないので sha256 は両 arch とも bake 時に自前計算してピン
# （改竄検知）。bump は CURSOR_VERSION と両 sha を揃えて書き換える（`curl -fsSL
# https://cursor.com/install | grep -oE 'versions/[0-9.]+-[a-f0-9]+'` で最新版、
# tarball を落として sha256sum で digest）。
#
# 自己更新は公式に無効化できる（バンドル解析: 背景更新は起動2秒後の
# setTimeout(...).unref() で走るが `disableAutoUpdate || channel==="static"` でスキップ）。
# 焼き込みでは AF が起動フラグ `--disable-auto-update`（全経路）＋ entrypoint の
# cli-config.json `channel:"static"` 再固定の二重で封じる（詳細 docs/40 Track B）。
ARG CURSOR_VERSION=2026.07.23-e383d2b
ARG CURSOR_SHA256_X64=702ad595213bee5df0268be9f80a19f29fcceaa2a42fc55e39f2b5199051f0c4
ARG CURSOR_SHA256_ARM64=f40b99647cb24e0da885e97620a2048034f1fe8961910d573d827d77c4d26dcb
RUN if [ "${BAKE_AGENT_CLIS}" = "1" ]; then set -eux; \
      arch="$(dpkg --print-architecture)"; \
      case "$arch" in \
        amd64) casset="x64"; sha="${CURSOR_SHA256_X64}" ;; \
        arm64) casset="arm64"; sha="${CURSOR_SHA256_ARM64}" ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
      esac; \
      dir="/usr/local/share/cursor-agent/versions/${CURSOR_VERSION}"; \
      mkdir -p "$dir"; \
      curl -fsSL "https://downloads.cursor.com/lab/${CURSOR_VERSION}/linux/${casset}/agent-cli-package.tar.gz" -o /tmp/cursor.tgz; \
      echo "${sha}  /tmp/cursor.tgz" | sha256sum -c -; \
      tar --strip-components=1 -xzf /tmp/cursor.tgz -C "$dir"; \
      rm -f /tmp/cursor.tgz; \
      ln -sf "$dir/cursor-agent" /usr/local/bin/cursor-agent; \
      cursor-agent --disable-auto-update --version; \
    fi

# Kiro CLI（kind="kiro"、docs/43 / docs/decisions/0026）。旧 Amazon Q Developer CLI
# の版付きネイティブバイナリ配布（bundled kiro-cli + kiro-cli-chat + kiro-cli-term）。
# 公式 manifest（prod.download.cli.kiro.dev/stable/latest/manifest.json）が版付き URL
# ＋ sha256 を掲載しており、agy と同じ「版付き URL ＋ sha256 ピン」で焼ける。zip 内は
# `kirocli/{BUILD-INFO,bin/*,install.sh}` で、同梱 install.sh を Q_INSTALL_GLOBAL=1
# Q_SKIP_SETUP=1 で回すと 3 バイナリだけを /usr/local/bin へ入れる（integrations/setup は
# 走らない）。展開後 ~855MB と桁違いに巨大なので、既定（BAKE_AGENT_CLIS=0）では焼かず
# オンデマンド導入（workspace-agent install-kiro／初回起動時の自動導入・§4-2 決定）に回し、
# BAKE_AGENT_CLIS=1（覚悟のノブ）でのみ焼く。
#
# arch 別アセット: x86_64 は gnu 版（install.sh の glibc 下限 2.34・Debian 12 は 2.36 で
# 充足）、aarch64 は **musl 版**（gnu は glibc 2.39 要求で Debian 12 では動かない — 実測）。
# sha256 は manifest 掲載値をそのまま ARG に写す（bump は KIRO_VERSION と両 arch の sha を
# 揃えて書き換え。値は `curl -fsSL .../stable/latest/manifest.json | jq` で確認）。
#
# 自己更新（既定 ON）はビルド時 ENV 相当のノブが無く、~/.kiro/settings/cli.json の
# `app.disableAutoupdates` で止める設定型なので、封殺は runtime（entrypoint 起動毎再固定
# ＋ 縦割りの ensureSettings）に寄せる（copilot の COPILOT_AUTO_UPDATE とは違い build ENV
# では止められない）。
ARG KIRO_VERSION=2.16.1
ARG KIRO_SHA256_X64=a799286f193c81872d4fad78abcab74104dbe0059f1ebcae3af18c4fb6d2d68c
ARG KIRO_SHA256_ARM64=18e2f9af55e0e634fb1ea864f4f63feb1bdeb42419b157c4586c89efa8e6d3ba
RUN if [ "${BAKE_AGENT_CLIS}" = "1" ]; then set -eux; \
      arch="$(dpkg --print-architecture)"; \
      case "$arch" in \
        amd64) kasset="kirocli-x86_64-linux.zip";      sha="${KIRO_SHA256_X64}" ;; \
        arm64) kasset="kirocli-aarch64-linux-musl.zip"; sha="${KIRO_SHA256_ARM64}" ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
      esac; \
      curl -fsSL "https://prod.download.cli.kiro.dev/stable/${KIRO_VERSION}/${kasset}" -o /tmp/kiro.zip; \
      echo "${sha}  /tmp/kiro.zip" | sha256sum -c -; \
      unzip -q /tmp/kiro.zip -d /tmp/kiro; \
      Q_INSTALL_GLOBAL=1 Q_SKIP_SETUP=1 sh /tmp/kiro/kirocli/install.sh; \
      rm -rf /tmp/kiro.zip /tmp/kiro; \
      kiro-cli --version; \
    fi

# ビルド時ピンをコンテナ内へ書き出す（ARG はビルド後に消えて実行時に参照できない）。
# 設定 UI「ツールのバージョン」の「ピン vs 実体」表示・スモークの検証材料に加え、
# lean variant（BAKE_AGENT_CLIS=0 / BAKE_OPTIONAL_TOOLS=0）では entrypoint の
# boot-install とオンデマンド導入（P2）がこのピンを取得版として使う（docs/35 §35.4.1）。
# BAKE ノブに関わらず常に全ピンを書き出す。
# chromium_cft は amd64 のオンデマンド chromium ピン = Chrome for Testing の
# browser version（playwright 1.61 同梱 build 1228 と同一実体。playwright は 1.61 で
# x64 を builds/chromium から CfT 配布へ移行済み — docs/35 §35.9-7(a) の P4 実測）。
# chromium_dl は arm64 が使う旧 builds/chromium レイアウトの build 番号ピン。
# noto_cjk は install-chromium が CJK フォントを取る notofonts/noto-cjk のタグピン
# （lean rootfs のオンデマンド導入用 — docs/35 §35.7.2-4）。
ARG CHROMIUM_CFT_VERSION=149.0.7827.55
ARG CHROMIUM_DL_VERSION=1228
ARG NOTO_CJK_VERSION=Sans2.004
# bind マウントポイントを rootfs に焼く: docker は -v 先のディレクトリを実行時に自動
# 作成するが、native rootfs モード（bwrap --ro-bind で / が read-only — docs/35
# §35.7.2）では bwrap が bind 先を mkdir できず起動が即死する。3 箇所とも空のまま焼く。
RUN mkdir -p /var/lib/af/claude /usr/local/share/agent-fleet/docs /usr/lib/jvm
RUN set -eux; mkdir -p /usr/local/share/agent-fleet; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) agy_sha="${AGY_SHA256_X64}"; cursor_sha="${CURSOR_SHA256_X64}"; kiro_sha="${KIRO_SHA256_X64}" ;; \
      arm64) agy_sha="${AGY_SHA256_ARM64}"; cursor_sha="${CURSOR_SHA256_ARM64}"; kiro_sha="${KIRO_SHA256_ARM64}" ;; \
      *) agy_sha=""; cursor_sha=""; kiro_sha="" ;; \
    esac; \
    printf '{"claude":"%s","opencode":"%s","codex":"%s","agy":"%s","agy_build":"%s","agy_sha256":"%s","copilot":"%s","cursor":"%s","cursor_sha256":"%s","kiro":"%s","kiro_sha256":"%s","rtk":"%s","go":"%s","gh":"%s","chromium":"%s","chromium_cft":"%s","chromium_dl":"%s","noto_cjk":"%s","mcp_grafana":"%s","cloudwatch_mcp":"%s","aws_mcp_proxy":"%s","awscli":"%s","session_manager_plugin":"%s"}\n' \
      "$CLAUDE_CODE_VERSION" "$OPENCODE_VERSION" "$CODEX_VERSION" "$AGY_VERSION" "$AGY_RELEASE_BUILD" "$agy_sha" "$COPILOT_VERSION" "$CURSOR_VERSION" "$cursor_sha" "$KIRO_VERSION" "$kiro_sha" "$RTK_VERSION" "$GO_VERSION" "$GH_VERSION" "$CHROMIUM_VERSION" \
      "$CHROMIUM_CFT_VERSION" "$CHROMIUM_DL_VERSION" "$NOTO_CJK_VERSION" "$MCP_GRAFANA_VERSION" "$CLOUDWATCH_MCP_VERSION" "$AWS_MCP_PROXY_VERSION" "$AWSCLI_VERSION" "$SESSION_MANAGER_PLUGIN_VERSION" \
      > /usr/local/share/agent-fleet/versions.json

# The baked claude is a root-owned npm-global install run as the non-root `dev`
# user, so Claude Code's background auto-updater can never write to
# /usr/local/lib/node_modules and prints "auto-update failed" on every start. We
# manage the claude version through image rebuilds (and the entrypoint still runs
# `claude update` for a native ~/.local install), so the background updater is both
# redundant and impossible — disable just it (DISABLE_AUTOUPDATER, not
# DISABLE_UPDATES, so explicit `claude update`/`claude install` still work).
ENV DISABLE_AUTOUPDATER=1

# opencode ships a background auto-updater that self-updates via `npm install` on
# startup (its `autoupdate` config defaults to on). On the root-owned baked /usr/local
# tree that write fails (and with the self-update opt-in it would silently mutate the
# ~/.local shadow) — both unwanted in the version-pinned model. Disable it via env;
# explicit `opencode upgrade` still works. (codex, by contrast, only *notifies* on
# startup by default and self-writes only on explicit `codex update` / opt-in
# autoUpdate, so it needs no analogous flag.)
ENV OPENCODE_DISABLE_AUTOUPDATE=true

# NEVER let git fall back to an interactive /dev/tty credential prompt. This fixes a
# real "the claude session accepts NO keyboard input" bug (gate k, docs/35 §35.9-10):
# when a user works in a github.com repo but has NOT connected GitHub, our global
# credential helper (`workspace-agent cred`) returns nothing for github.com, so the
# `gh pr view` that Claude Code runs for PR status falls to `git credential fill`,
# which opens /dev/tty (== the tmux pane's pty) and BLOCKS on a username/password
# read(). That makes credential-fill a second reader on claude's controlling terminal
# and it eats every keystroke send-keys delivers, so claude's stdin epoll never fires.
# GIT_TERMINAL_PROMPT=0 makes git fail fast instead of prompting; GCM_INTERACTIVE=never
# does the same if git-credential-manager is ever in play. This is NOT native-specific:
# any GitHub-unconnected user (e.g. Bitbucket-only) hits it on any target — it just
# usually stays hidden because most users have GitHub connected. The agent already sets
# GIT_TERMINAL_PROMPT=0 per-command for its OWN git (internal/gitx), but that never
# reaches the git subprocess claude spawns; setting it in the image env covers every
# git invocation on every target. Safe with the settings-modal auth: credentials are
# captured via the Console (OAuth device flow / PAT paste), never via git's prompt, and
# the helper's get path (the only one implemented) runs BEFORE this fallback.
ENV GIT_TERMINAL_PROMPT=0 \
    GCM_INTERACTIVE=never

# Opt a self-hosted fleet out of Claude Code's optional/background network calls. NOTE:
# this does NOT fix the input-hang above (that was an early misdiagnosis, commit
# b1c1c41 / an earlier revision of docs/35 §35.9-10 — the real cause is the git
# credential prompt handled just above; these flags never stop `gh pr view`). Kept only
# as harmless hardening for restricted/air-gapped networks and privacy, and it aligns
# with the version-pinned CLI story (DISABLE_AUTOUPDATER above). Harmless when fully
# connected.
ENV CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    DISABLE_TELEMETRY=1 \
    DISABLE_ERROR_REPORTING=1

# The baked npm-global tree at /usr/local is the PINNED, IMMUTABLE baseline. The opt-in
# self-update no longer updates it in place — it installs latest under ~/.local as a
# PATH-first shadow (entrypoint's "agent self-update" block), leaving the baked copy
# untouched so a broken @latest can never corrupt the known-good baseline; toggling the
# opt-in off drops the shadow and PATH falls back to this pin. So /usr/local is left
# root-owned (default) — dev never writes it at runtime — which also reinforces the
# pin's immutability. (Claude Code's background auto-updater, which would otherwise try
# and fail to write this root-owned tree, is separately silenced by DISABLE_AUTOUPDATER
# above.)

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

# Docker grants SYS_ADMIN only in the capability bounding set so Chromium's
# root-owned setuid helper can create its PID/network namespaces. Keep that path
# exclusive: the normal USER dev process has no effective capabilities, and no
# unrelated setuid/setgid utility may acquire the enlarged bounding set.
# BAKE_OPTIONAL_TOOLS=0 は chromium 不在なので sandbox helper の存在前提を外し、
# 「setuid/setgid が一切残っていない」ことだけを見る（docs/35 §35.7.1-5）。
RUN find / -xdev -type f -perm /6000 ! -path /usr/lib/chromium/chrome-sandbox \
      -exec chmod a-s {} + \
 && if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then \
      test "$(stat -c '%u:%g:%a' /usr/lib/chromium/chrome-sandbox)" = "0:0:4755"; \
    else \
      test ! -e /usr/lib/chromium/chrome-sandbox; \
    fi \
 && test -z "$(find / -xdev -type f -perm /6000 \
      ! -path /usr/lib/chromium/chrome-sandbox -print -quit)"

USER dev
WORKDIR /home/dev
EXPOSE 7700
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["workspace-agent"]
