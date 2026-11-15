# Workspace image: git + tmux + Workspace Agent + Claude CLI.
# The Claude CLI is baked in (/usr/local, for a fast, offline start). A claude in the user's
# ~/.local takes PATH precedence and the entrypoint keeps it updated; the baked one is the
# pinned fallback and the default for a fresh home. Tools live in /usr/local, user data in
# the home volume. See docs/11-phase1-plan.md / docs/09-portability.md.

# Pins shared by the build stages below; the runtime stage redeclares them (no value) to inherit.
ARG GO_VERSION=1.26.10
ARG JIRA_CLI_VERSION=1.7.0
ARG JIRA_CLI_COMMIT=79067e2422df10e59f310bd6fd4979f43a1df0e7

# --- build the agent (Go) ---
# Native on the builder, cross-compiling to the target — the same trick, and for the
# same reason, as control-plane/Dockerfile's `build` stage. CGO is off, so Go needs no
# cross toolchain for GOARCH; emulating the COMPILER under QEMU instead is the single
# most expensive thing a two-architecture workspace build can do (docs/log/72 §72.3).
# ⚠️ Dropping `--platform=$BUILDPLATFORM` still produces a CORRECT image — the cost
# shows up as build minutes, not as a failure, which is how it went unnoticed here
# while the CP had it from the start.
FROM --platform=$BUILDPLATFORM golang:1.26-trixie AS agentbuild
WORKDIR /src
COPY agent/go.mod agent/go.sum ./
RUN go mod download
COPY agent/ ./
# VERSION stamps the binary (startup log, docs/log/35 §35.6.1); only the release
# pipeline passes a real value — dev builds stay "dev".
ARG VERSION=dev
# TARGETARCH is supplied by BuildKit (amd64 / arm64) and maps 1:1 onto GOARCH. It is
# empty for a plain `docker build` on the classic builder, so fall back to the builder's
# own GOARCH rather than to a hardcoded one — an empty GOARCH would otherwise mean
# "amd64" on any host and quietly mis-build on an arm64 laptop.
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH:-$(go env GOARCH)}" \
    go build -trimpath -ldflags="-X main.buildVersion=${VERSION}" -o /out/workspace-agent .

# --- build jira-cli from source ---
# Upstream's latest release (ankitpokhrel/jira-cli v1.7.0, 2025-08-31) is built with go1.24.1,
# golang.org/x/net 0.38.0 and x/text 0.23.0, which carries a CRITICAL and ~30 HIGH Go findings
# in the image scan (#3339), and no newer release exists. So the same tag is rebuilt here with
# our pinned Go and bumped modules; the version/commit stamping matches upstream's .goreleaser.yaml
# so `jira version` still reports the release. JIRA_CLI_COMMIT pins the tag's commit: a moved
# tag fails the build instead of shipping different source. Drop this stage and go back to the
# release tarball once upstream ships a release built with a fixed Go.
# Native on the builder, cross-compiled like agentbuild (CGO off, so no cross toolchain).
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-trixie AS jirabuild
ARG JIRA_CLI_VERSION
ARG JIRA_CLI_COMMIT
ARG TARGETARCH
# GOTOOLCHAIN=local: the bumped x/net raises the go directive; never let go download another toolchain.
ENV GOTOOLCHAIN=local GOFLAGS=-mod=mod
WORKDIR /src
RUN set -eux; \
    git clone --quiet --depth 1 --branch "v${JIRA_CLI_VERSION}" https://github.com/ankitpokhrel/jira-cli.git .; \
    test "$(git rev-parse HEAD)" = "${JIRA_CLI_COMMIT}"; \
    go get golang.org/x/net@v0.62.0 golang.org/x/text@v0.43.0 github.com/yuin/goldmark@v1.7.17; \
    go mod tidy; \
    ts="$(git log -1 --format=%ct)"; \
    CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH:-$(go env GOARCH)}" \
    go build -trimpath -ldflags="-s -w \
      -X github.com/ankitpokhrel/jira-cli/internal/version.Version=${JIRA_CLI_VERSION} \
      -X github.com/ankitpokhrel/jira-cli/internal/version.GitCommit=${JIRA_CLI_COMMIT} \
      -X github.com/ankitpokhrel/jira-cli/internal/version.SourceDateEpoch=${ts}" \
      -o /out/jira ./cmd/jira

# --- runtime ---
FROM node:22-trixie-slim
# Links the GHCR package to this repository — see control-plane/Dockerfile for why.
LABEL org.opencontainers.image.source="https://github.com/k-k1/agent-fleet" \
      org.opencontainers.image.description="Agent Fleet per-user workspace — isolated container running the agent CLIs" \
      org.opencontainers.image.licenses="Apache-2.0"
RUN apt-get update && apt-get install -y --no-install-recommends \
      git git-lfs git-delta subversion tmux vim openssh-client ca-certificates gnupg curl wget less procps htop strace tzdata \
      ripgrep fd-find bat jq unzip zip \
      build-essential pkg-config \
      python3 python3-pip python3-venv python3-dev python-is-python3 \
      libaio1t64 libnuma1 libncurses6 \
 && ln -sf "$(command -v fdfind)" /usr/local/bin/fd \
 && ln -sf "$(command -v batcat)" /usr/local/bin/bat \
 && rm -rf /var/lib/apt/lists/* \
 && git lfs install --system --skip-repo
# Beyond the original baseline this adds a C/C++ toolchain (build-essential +
# pkg-config + python3-dev) so cgo / node-gyp / source-built wheels compile, plus
# common utilities (wget, jq, unzip/zip, gnupg, htop), fd/bat (Debian ships them
# as fdfind/batcat; symlinked to the expected names), and subversion for svn repos
# (HTTPS via libserf, pulled as a dep). git-delta was omitted while the base was
# bookworm (not in that suite); trixie's main has it, so it is a plain install now
# (docs/decisions/0068 — one of the benefits that moved the base, not a cost).
# Python 3 (trixie = 3.13) baked in as a baseline tool, like git/tmux. python-is-
# python3 provides `python`. Debian (PEP 668) marks the system env externally-managed,
# which blocks `pip install` into it; allow break-system-packages so `pip install
# --user <pkg>` lands in the persistent home (~/.local, on PATH). venvs work as usual.
RUN printf '[global]\nbreak-system-packages = true\n' > /etc/pip.conf

# uv/uvx (Astral) baked as a baseline tool: it is the launcher for the ops MCP
# servers the SRE assistant uses (docs/log/25 — `workspace-agent mcp-run pagerduty`
# execs `uvx pagerduty-mcp`). Installed to /usr/local/bin so it's on PATH for
# every user without a per-home setup step; uvx caches fetched servers under the
# persistent ~/.cache after first run.
RUN pip install --no-cache-dir uv \
    && uv --version && uvx --version

# npm is pinned past the copy the node base image bundles: that one carries brace-expansion,
# ip-address, pacote, picomatch and sigstore releases with fixable HIGH advisories (#3339),
# and the base tag floats, so only an explicit pin keeps the scan result stable. The self-
# upgrade replaces /usr/local/lib/node_modules/npm wholesale. Keep it on a release whose
# engines range still admits the node 22 line (npm 12 needs >=22.22.2, so stay on 11.x).
# Not covered by cli-drift / cli-pin-bump (those rows are agent CLIs with contract gates); the
# tracked update path is the image-vuln-scan issue, which reports any fixable npm-bundled
# finding as remedy "bump". Take the new pin from `npm view npm@next-11 version`.
ARG NPM_VERSION=11.21.2
RUN npm install -g "npm@${NPM_VERSION}" \
    && test "$(npm --version)" = "${NPM_VERSION}" \
    && npm cache clean --force

# --- Distribution knobs (docs/log/35 §35.4.1, §35.7.1-5) ---------------------------
# BAKE_AGENT_CLIS=0 … do not bake the agent CLIs (claude/opencode/codex/copilot/cursor/
#   agy/rtk): the lean variant, because the proprietary CLIs are treated as not
#   redistributable. The entrypoint's boot-install puts the versions.json pins into
#   ~/.local on first start.
# BAKE_OPTIONAL_TOOLS=0 … do not bake chromium + CJK fonts, Go, awscli + SSM plugin or
#   the ops MCP servers (size; for the native rootfs). The chromium runtime libraries
#   and fontconfig/DejaVu stay; chromium itself is downloaded on demand.
# BAKE_AGENT_CLIS defaults to 0 so that a plain `docker build` never puts a
#   non-redistributable CLI into an image that might be shared by accident. Own
#   deployments that want a faster first start pass BAKE_AGENT_CLIS=1 (run-dev.sh can).
# BAKE_OPTIONAL_TOOLS defaults to 1: the size-only tools carry no licence concern, and
#   only the native lean rootfs sets 0.
ARG BAKE_AGENT_CLIS=0
ARG BAKE_OPTIONAL_TOOLS=1

# mcp-grafana (official Grafana MCP server, Go single binary) baked for the ops
# integrations (docs/log/25 — `workspace-agent mcp-run grafana` execs it with the
# stored URL + service-account token and -disable-write -disable-admin). Works
# against self-hosted / Grafana Cloud / Amazon Managed Grafana. Bump
# MCP_GRAFANA_VERSION to upgrade (ARG pin busts the cache; same idiom as GH_VERSION).
ARG MCP_GRAFANA_VERSION=2.2.0
RUN if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) mgarch=x86_64 ;; arm64) mgarch=arm64 ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac; \
    curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "https://github.com/grafana/mcp-grafana/releases/download/v${MCP_GRAFANA_VERSION}/mcp-grafana_Linux_${mgarch}.tar.gz" -o /tmp/mcp-grafana.tgz; \
    tar -C /tmp -xzf /tmp/mcp-grafana.tgz mcp-grafana; \
    install -m 0755 /tmp/mcp-grafana /usr/local/bin/mcp-grafana; \
    rm -f /tmp/mcp-grafana.tgz /tmp/mcp-grafana; \
    mcp-grafana --version; \
    fi

# awslabs CloudWatch MCP server (Python) baked as a uv tool so `workspace-agent
# mcp-run cloudwatch` starts instantly with no PyPI fetch at runtime (docs/log/25
# §5-5 (d)). UV_PYTHON_DOWNLOADS=never + --python pin the SYSTEM python: without
# them uv drops a managed interpreter into root's home, unreadable by the
# runtime user. The venv + entrypoint under /usr/local are root-owned and
# world-readable, which is all the runtime user needs.
# The check goes as far as an import because some breakage gets past `test -x`: 0.1.4
# declares `mcp[cli]>=1.23.0` with no upper bound, so a build now resolves mcp 2.0.0, loses
# `mcp.server.fastmcp` and dies at runtime with ModuleNotFoundError (measured). 0.1.8 caps
# it at `<2.0.0` and stays on 1.x; 0.2.0 and later require `>=2.0.0,<3.0.0` and are ported
# to the mcp 2.x API (measured on bump: 0.2.1 resolves to system python 3.13 + mcp 2.2.0 and
# the import below passes). Verify a pin by what it resolves to, not just its version.
ARG CLOUDWATCH_MCP_VERSION=0.2.1
RUN if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then set -eux; \
    UV_TOOL_DIR=/usr/local/share/uv/tools UV_TOOL_BIN_DIR=/usr/local/bin \
    UV_PYTHON_DOWNLOADS=never UV_NO_CACHE=1 \
    uv tool install --python /usr/bin/python3 "awslabs.cloudwatch-mcp-server==${CLOUDWATCH_MCP_VERSION}"; \
    test -x /usr/local/bin/awslabs.cloudwatch-mcp-server; \
    /usr/local/share/uv/tools/awslabs-cloudwatch-mcp-server/bin/python \
      -c "import awslabs.cloudwatch_mcp_server.server"; \
    fi

# AWS MCP proxy (Agent Toolkit for AWS — docs/log/25 §AWS MCP) baked the same way:
# `workspace-agent mcp-run aws` execs it to reach the AWS-operated MCP Server over
# SigV4. Worth baking rather than leaving to uvx: the dependency tree is botocore +
# awscrt (~20MB of wheels), so a cold fetch is the difference between an MCP server
# that starts and one the CLI gives up on.
# Import-checked like cloudwatch. This venv's `bin/python` links to `/usr/bin/python3`, not
# python3.13, so the day the interpreter's version moves the link does not break: it
# silently starts the new python, which cannot see the site-packages, and `test -x` still
# passes (docs/decisions/0068 decision 4). The import name was checked on a real install.
ARG AWS_MCP_PROXY_VERSION=1.7.0
RUN if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then set -eux; \
    UV_TOOL_DIR=/usr/local/share/uv/tools UV_TOOL_BIN_DIR=/usr/local/bin \
    UV_PYTHON_DOWNLOADS=never UV_NO_CACHE=1 \
    uv tool install --python /usr/bin/python3 "mcp-proxy-for-aws==${AWS_MCP_PROXY_VERSION}"; \
    test -x /usr/local/bin/mcp-proxy-for-aws; \
    /usr/local/share/uv/tools/mcp-proxy-for-aws/bin/python \
      -c "import mcp_proxy_for_aws"; \
    fi

# Chromium for the in-container browser pane and headless UI verification. Debian's
# package supplies the same /usr/bin/chromium entrypoint on amd64 and arm64 and pulls
# its runtime libraries as dependencies. Pin all three Chromium packages together:
# chromium-common requires an exact match and chromium-sandbox is normally only a
# recommendation (suppressed by --no-install-recommends). Bump the ARG after Debian
# publishes the same security build for both architectures. Fonts and fontconfig are
# explicit so Japanese pages render without depending on a transient recommendation.
# deploy/local/apt-pin-check.sh checks the pin against both architectures' indexes and
# prints the version to bump to; cli-drift.yml runs it every 2 hours and goes red when the
# pin is gone for either architecture, and apt-pin-bump.yml opens the PR that moves it.
# trixie-security carries only the current build; older versions vanish from the mirror. A
# stale pin fails the next build that misses this layer's cache with `Version ... was not
# found` — triggered by touching some unrelated ARG, so it looks unrelated. Follow upstream
# security updates. The current version:
# `curl -fsSL http://security.debian.org/debian-security/dists/trixie-security/main/binary-<arch>/Packages.xz | xz -dc | grep -A5 '^Package: chromium$'`
# When comparing versions, read the same index on both sides (docs/decisions/0068 decision 3):
# the security and main suites point at different versions, so reading one side from each
# invents a version gap that does not exist. The pin here is always the security suite's version.
ARG CHROMIUM_VERSION=156.0.8078.79-1~deb13u1
# BAKE_OPTIONAL_TOOLS=0 (the native lean rootfs) bakes neither chromium nor the CJK fonts and
# leaves them to the on-demand download (install-chromium, pinned by CHROMIUM_DL_VERSION), but
# the runtime libraries that download needs, plus fontconfig/DejaVu, are installed here
# (docs/log/35 §35.7.1-5; the library list follows playwright's chromium deps).
# The `…t64` names are not typos: Debian's 64-bit time_t transition renamed six of them
# (libasound2 / libatk-bridge2.0-0 / libatk1.0-0 / libatspi2.0-0 / libcups2 / libglib2.0-0)
# and trixie has no old names. Dropping t64 breaks only this branch, and e2e.yml builds only
# BAKE_OPTIONAL_TOOLS=1, so e2e stays green. Verify with a separate BAKE_OPTIONAL_TOOLS=0
# build (docs/decisions/0068).
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
        libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 libcairo2 \
        libcups2t64 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0t64 libnspr4 libnss3 \
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
# ── Heavy, rarely changing layers come first (Go toolchain / agent CLIs / user rename).
# Frequently changing COPYs (entrypoint, workspace-notes, …) are grouped after them so a
# small edit does not invalidate these layers' cache (layer ordering = build time).

# Go toolchain from the official pinned tarball (decoupled from the agent builder
# image; arch auto-detected). Pure-Go builds work as-is; cgo / node-gyp / native
# Python wheels use the C toolchain (build-essential) installed above. `go install`
# binaries and the module cache land under ~/go (home volume → persist). Keep
# GO_VERSION in step with agent/go.mod's `go` directive; bump here to upgrade.
ARG GO_VERSION
RUN if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) goarch=amd64 ;; arm64) goarch=arm64 ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac; \
    curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz" -o /tmp/go.tgz; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz; \
    /usr/local/go/bin/go version; \
    fi

# AWS CLI v2 + Session Manager plugin for SSM login sessions (kind="ssm"): a member
# opens a browser terminal that runs `aws sso login` (device-code URL surfaced in the
# terminal) + `aws ssm start-session` against their OWN AWS account. NO AWS credentials
# are baked or stored anywhere — auth is per-session via SSO and cached in the home
# volume; the Control Plane only resolves non-secret host bookmarks. arch auto-detected.
# Versions are pinned by ARG through versioned URLs (docs/log/35 §35.9-7(c)); bump the ARGs
# below and rebuild.
ARG AWSCLI_VERSION=2.36.46
ARG SESSION_MANAGER_PLUGIN_VERSION=1.2.835.0
RUN if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) awsarch=x86_64; smparch=64bit ;; \
      arm64) awsarch=aarch64; smparch=arm64 ;; \
      *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "https://awscli.amazonaws.com/awscli-exe-linux-${awsarch}-${AWSCLI_VERSION}.zip" -o /tmp/awscliv2.zip; \
    unzip -q /tmp/awscliv2.zip -d /tmp; \
    /tmp/aws/install; \
    curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "https://s3.amazonaws.com/session-manager-downloads/plugin/${SESSION_MANAGER_PLUGIN_VERSION}/ubuntu_${smparch}/session-manager-plugin.deb" -o /tmp/smp.deb; \
    dpkg -i /tmp/smp.deb; \
    rm -rf /tmp/awscliv2.zip /tmp/aws /tmp/smp.deb; \
    aws --version && session-manager-plugin --version; \
    fi

# GitHub CLI (gh) from the official pinned release (arch auto-detected). Baked into the
# image so `gh` (PR/issue ops, `gh auth`, API) is available on a fresh container without a
# per-home download. Bump GH_VERSION to upgrade.
# The real binary goes to /usr/local/libexec/gh and /usr/local/bin/gh is the transparent-auth
# wrapper (gh-auth-wrapper.sh): on every call it takes the token from the same credential
# helper git uses and injects it as GH_TOKEN, so gh works without `gh auth login`
# (docs/build/08 §8.3). The wrapper itself is COPY'd with the frequently changing COPYs at the end.
ARG GH_VERSION=2.103.3
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64|arm64) gharch="$arch" ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac; \
    curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${gharch}.tar.gz" -o /tmp/gh.tgz; \
    tar -C /tmp -xzf /tmp/gh.tgz; \
    install -D -m 0755 "/tmp/gh_${GH_VERSION}_linux_${gharch}/bin/gh" /usr/local/libexec/gh; \
    rm -rf /tmp/gh.tgz "/tmp/gh_${GH_VERSION}_linux_${gharch}"; \
    /usr/local/libexec/gh --version

# Bitbucket CLI (bkt, avivsinai/bitbucket-cli): the Bitbucket counterpart of gh, same shape.
# The real binary goes to /usr/local/libexec/bkt and /usr/local/bin/bkt is the transparent-auth
# wrapper (bkt-auth-wrapper.sh, COPY'd with the other wrappers below). The tarball must match the
# release's own checksums.txt, or the build fails. Bump BKT_VERSION to upgrade (docs/build/08 §8.3).
ARG BKT_VERSION=0.37.0
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) bktarch=x86_64 ;; arm64) bktarch=arm64 ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac; \
    base="https://github.com/avivsinai/bitbucket-cli/releases/download/v${BKT_VERSION}"; \
    asset="bkt_${BKT_VERSION}_linux_${bktarch}.tar.gz"; \
    mkdir /tmp/bkt; \
    curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "${base}/${asset}" -o "/tmp/bkt/${asset}"; \
    curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "${base}/checksums.txt" -o /tmp/bkt/checksums.txt; \
    (cd /tmp/bkt && awk -v a="${asset}" '$2 == a { print; n++ } END { exit n != 1 }' checksums.txt >sum && sha256sum -c sum); \
    tar -C /tmp/bkt -xzf "/tmp/bkt/${asset}" bkt; \
    install -D -m 0755 /tmp/bkt/bkt /usr/local/libexec/bkt; \
    rm -rf /tmp/bkt; \
    /usr/local/libexec/bkt --version

# Jira CLI (jira, ankitpokhrel/jira-cli): same shape as bkt, but built from source in the
# jirabuild stage (see there for why). The real binary goes to /usr/local/libexec/jira and
# /usr/local/bin/jira is the transparent-auth wrapper (jira-auth-wrapper.sh). Bump
# JIRA_CLI_VERSION and JIRA_CLI_COMMIT (top of file) to upgrade (docs/build/08 §8.4.2).
ARG JIRA_CLI_VERSION
COPY --from=jirabuild /out/jira /usr/local/libexec/jira
RUN /usr/local/libexec/jira version

# Confluence CLI (confluence-cli, pchuri/confluence-cli): the Confluence counterpart of jira,
# over the same Atlassian connection. It is an npm package (Node >= 20, the base image's node),
# installed into its own prefix so nothing lands in the global node_modules, and the package
# ships an npm-shrinkwrap.json that pins its dependency tree. /usr/local/libexec/confluence links
# to its bin and /usr/local/bin/confluence is the transparent-auth wrapper
# (confluence-auth-wrapper.sh). Bump CONFLUENCE_CLI_VERSION to upgrade (docs/build/08 §8.4.3).
ARG CONFLUENCE_CLI_VERSION=2.31.1
RUN set -eux; \
    npm install -g --prefix /usr/local/lib/confluence-cli "confluence-cli@${CONFLUENCE_CLI_VERSION}"; \
    install -d /usr/local/libexec; \
    ln -sfn /usr/local/lib/confluence-cli/bin/confluence /usr/local/libexec/confluence; \
    test "$(/usr/local/libexec/confluence --version)" = "${CONFLUENCE_CLI_VERSION}"

# rtk (token-saving claude Bash hook): fetched pinned at build time, but only when
# both BAKE_RTK=1 and BAKE_AGENT_CLIS=1. In the default lean build (BAKE_AGENT_CLIS=0)
# the entrypoint's boot-install puts the rtk pin from versions.json into ~/.local/bin
# instead (docs/log/35 §35.4.1). BAKE_RTK=0 is the escape hatch for airgapped builds
# (no build-time GitHub fetch); the entrypoint seeds the hook only when `rtk` is on PATH.
# rtk-ai/rtk is public, so the asset + checksums download without auth.
# The asset is the Apache-2.0 edition (`rtk-oss-*`): from 1.0.0 the default `rtk-*` asset
# links a BUSL-1.1 engine, which breaks the NOTICE entry and the redistribution premise of
# docs/log/35 §35.4.1. The oss edition drops only that engine (pipelines, PostToolUse
# compression); the PreToolUse hook, `rtk rewrite` and `rtk gain` we use are identical.
# entrypoint.sh downloads the same assets (boot-install and self-update) — keep them in step.
#
# ⚠️ amd64 gets a STATIC musl binary. arm64 stays on `rtk-oss-aarch64-unknown-linux-gnu` (gnu, like
# the default edition's); v1.2.0 also lists an `-aarch64-unknown-linux-musl` asset, not adopted
# here until it has been run on arm64. The gnu build requires **GLIBC_2.39** (exactly two symbols,
# `pidfd_getpid` / `pidfd_spawnp` — verified with readelf on v0.47.0, unchanged on v1.2.0). On the old
# Debian 12 base (**glibc 2.36**) the download and the sha256 both passed and the binary
# then could not start (measured on Graviton4, 2026-08-22, docs/log/70 §70.9.2). The base
# is now Debian 13 (**glibc 2.41 >= 2.39**), so it is expected to load — but "loads" is
# not "works", so nothing here asserts it (docs/decisions/0068). Exactly the shape
# docs/log/43 recorded for kiro, except kiro publishes an aarch64 musl variant and rtk
# did not until 1.2.0 (checked across v0.47.0 and dev-0.48.0-rc.400); not adopted until run on arm64.
#
# So the binary is VERIFIED BY RUNNING IT, and a version that cannot run is removed
# rather than shipped. That is deliberately not an `arch = arm64` check: the day
# upstream ships a musl aarch64 build, or the base image moves to a newer glibc, this
# starts working with no edit here. When rtk is dropped, the reason is written to
# /usr/local/share/agent-fleet/rtk-unavailable so the image can say why (the L1 smoke
# reads it, and "absent with no reason" stays a failure).
# Bump RTK_VERSION to upgrade (ARG change busts the cache; same idiom as GH_VERSION).
# The AF_AGENT_SELF_UPDATE opt-in also updates rtk in place at start (entrypoint).
ARG BAKE_RTK=1
ARG RTK_VERSION=1.2.0
RUN if [ "${BAKE_RTK}" = "1" ] && [ "${BAKE_AGENT_CLIS}" = "1" ]; then set -eux; \
      arch="$(dpkg --print-architecture)"; \
      case "$arch" in \
        amd64) asset="rtk-oss-x86_64-unknown-linux-musl.tar.gz" ;; \
        arm64) asset="rtk-oss-aarch64-unknown-linux-gnu.tar.gz" ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
      esac; \
      base="https://github.com/rtk-ai/rtk/releases/download/v${RTK_VERSION}"; \
      cd /tmp; \
      curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "${base}/${asset}" -o "${asset}"; \
      curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "${base}/checksums.txt" -o checksums.txt; \
      grep " ${asset}\$" checksums.txt | sha256sum -c -; \
      tar xzf "${asset}"; \
      install -m 0755 rtk /usr/local/bin/rtk; \
      rm -f "${asset}" checksums.txt rtk; \
      if ! err="$(/usr/local/bin/rtk --version 2>&1)"; then \
        mkdir -p /usr/local/share/agent-fleet; \
        printf 'rtk %s does not run on %s in this base image: %s\n' "${RTK_VERSION}" "$arch" "$err" \
          > /usr/local/share/agent-fleet/rtk-unavailable; \
        cat /usr/local/share/agent-fleet/rtk-unavailable >&2; \
        rm -f /usr/local/bin/rtk; \
      fi; \
    fi

# Coding-agent CLIs baked into the image (stable /usr/local install) for fast,
# offline container start. claude: a user-home ~/.local install still takes PATH
# precedence and is auto-updated by the entrypoint; this is the pinned fallback /
# fresh-home default. opencode: an alternative agent (kind="opencode"); its auth
# (opencode auth login) and sessions persist under ~/.local/share/opencode (home).
# codex: OpenAI Codex CLI (kind="codex"); auth (codex login --with-api-key /
# --device-auth) writes ~/.codex/auth.json and sessions land in ~/.codex (home).
# Versions are pinned by ARG (same idiom as GH_VERSION / GO_VERSION): bump the ARGs below,
# rebuild, then Stop→Start each Workspace. Check latest with `npm view <pkg> version`.
# Without a pin the unchanged RUN line hits the layer cache and a rebuild keeps the old
# version; changing the ARG is what reliably busts the cache.
ARG CLAUDE_CODE_VERSION=2.1.325
ARG OPENCODE_VERSION=2.3.0
ARG CODEX_VERSION=0.169.0
ARG COPILOT_VERSION=1.0.112
# opencode is the npm package @opencode/cli (it also installs an `opencode2` bin). Only opencode 2
# is supported (ADR 0127): a rollback is an earlier 2.x pin, never a 1.x one (the opencode 1
# package, opencode-ai, is another package that entrypoint.sh removes from ~/.local).
RUN if [ "${BAKE_AGENT_CLIS}" = "1" ]; then \
      npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
        "@opencode/cli@${OPENCODE_VERSION}" \
        "@openai/codex@${CODEX_VERSION}" \
        "@github/copilot@${COPILOT_VERSION}" \
      && claude --version && opencode --version && codex --version && copilot --version; \
    fi

# copilot (kind="copilot", docs/log/36) self-updates by default (off only when it detects a
# CI env). The fleet's versions are owned by image rebuilds, so turn it off explicitly — the
# same reasoning as claude's DISABLE_AUTOUPDATER and agy's AGY_CLI_DISABLE_AUTO_UPDATE. An
# explicit `copilot update` by the user still works.
ENV COPILOT_AUTO_UPDATE=false

# Antigravity CLI (kind="agy", docs/decisions/0008 / docs/log/32). A Go binary rather than an
# npm package, hence its own RUN. install.sh always takes the latest manifest and cannot pin,
# and GitHub Releases stopped tracking versions after 1.1.7, so this fetches the immutable GCS
# object the official installer points at (its URL carries the version and build id). The
# sha256 is computed from that archive after checking it against the official manifest's
# sha512, pinning it twice. Bump AGY_VERSION, AGY_RELEASE_BUILD and both arches' sha together.
# agy is a Go BoringCrypto (FIPS) build and SIGABRTs at start on an x86 host whose kernel has
# withdrawn RDRAND (0008: Ryzen Embedded R2514). There it is checked under the same
# OPENSSL_ia32cap mask the Agent applies to every spawn (0008, decision of 2026-09-07), so
# only a non-x86 build host skips the --version check (an arm64 cross-build cannot always
# run the binary).
ARG AGY_VERSION=1.5.9
ARG AGY_RELEASE_BUILD=4515810895593472
ARG AGY_SHA256_X64=8b1f5e56779f2434a0af36f1b715a8392a084de550f8c68bc79821f95fabf3fa
ARG AGY_SHA256_ARM64=3a18c238c7ee6661e43df7e824a7d4c1bed78aeeda09430921a0f1dbfe825bca
RUN if [ "${BAKE_AGENT_CLIS}" = "1" ]; then set -eux; \
      arch="$(dpkg --print-architecture)"; \
      case "$arch" in \
        amd64) asset="linux-x64/cli_linux_x64.tar.gz"; sha="${AGY_SHA256_X64}" ;; \
        arm64) asset="linux-arm/cli_linux_arm64.tar.gz"; sha="${AGY_SHA256_ARM64}" ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
      esac; \
      curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "https://storage.googleapis.com/antigravity-public/antigravity-cli/${AGY_VERSION}-${AGY_RELEASE_BUILD}/${asset}" -o /tmp/agy.tgz; \
      echo "${sha}  /tmp/agy.tgz" | sha256sum -c -; \
      tar -C /tmp -xzf /tmp/agy.tgz antigravity; \
      install -m 0755 /tmp/antigravity /usr/local/bin/agy; \
      rm -f /tmp/agy.tgz /tmp/antigravity; \
      if grep -qw rdrand /proc/cpuinfo; then agy --version; \
      elif [ "$arch" = "amd64" ]; then OPENSSL_ia32cap='~0x4000000000000000' agy --version; \
      else echo "skip agy --version check (non-x86 build host)"; fi; \
    fi

# agy self-updates its installed binary in the background during normal runs (documented
# in install.sh). Versions are owned by image rebuilds, so turn it off — same reasoning as
# claude's DISABLE_AUTOUPDATER (an explicit `agy update` still works).
ENV AGY_CLI_DISABLE_AUTO_UPDATE=true

# Cursor CLI (kind="cursor", docs/log/40 / docs/decisions/0023). Neither npm nor a single Go
# binary: a versioned tarball of a Node.js bundle (bundled node + webpack chunks +
# node_sqlite3/pty/ripgrep). Upstream's `curl cursor.com/install | bash` serves a
# version-pinned script that unpacks
# `downloads.cursor.com/lab/<version>/linux/<arch>/agent-cli-package.tar.gz` into
# `~/.local/share/cursor-agent/versions/<version>/` with `tar --strip-components=1` and
# symlinks `~/.local/bin/{agent,cursor-agent}` (the wrapper resolves node/index.js in its own
# version directory through realpath). Baking puts the same layout under /usr/local, root-owned
# (the dev user cannot write it). The bare name `agent` collides easily on PATH, so only
# `cursor-agent` is linked into /usr/local/bin (AF calls only cursor-agent).
#
# The version is date-shaped (2026.07.20-8cc9c0b), not semver — careful with any version
# comparison (like rtk-always-baked; e2e-smoke compares the whole version string). Upstream
# publishes no checksums, so the sha256 for both arches is computed at bake time and pinned
# (tamper detection). Bump CURSOR_VERSION and both shas together (latest version:
# `curl -fsSL https://cursor.com/install | grep -oE 'versions/[0-9.]+-[a-f0-9]+'`; digest:
# download the tarball and sha256sum it).
#
# Self-update can be disabled officially (from reading the bundle: the background update runs
# from a setTimeout(...).unref() 2 s after start but skips on
# `disableAutoUpdate || channel==="static"`). The baked CLI is held twice: AF's launch flag
# `--disable-auto-update` (every path) plus the entrypoint re-pinning `channel:"static"` in
# cli-config.json (details: docs/log/40 Track B).
ARG CURSOR_VERSION=2026.10.22-27dcf5f
ARG CURSOR_SHA256_X64=85296944e4600a83c4b764f578d1502bf9c416db200d33a593e8f3ecabfdc294
ARG CURSOR_SHA256_ARM64=22a36df9720830668ccd4d707e5190204ab9c8a0efb2f068cc914895f804d3ec
RUN if [ "${BAKE_AGENT_CLIS}" = "1" ]; then set -eux; \
      arch="$(dpkg --print-architecture)"; \
      case "$arch" in \
        amd64) casset="x64"; sha="${CURSOR_SHA256_X64}" ;; \
        arm64) casset="arm64"; sha="${CURSOR_SHA256_ARM64}" ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
      esac; \
      dir="/usr/local/share/cursor-agent/versions/${CURSOR_VERSION}"; \
      mkdir -p "$dir"; \
      curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "https://downloads.cursor.com/lab/${CURSOR_VERSION}/linux/${casset}/agent-cli-package.tar.gz" -o /tmp/cursor.tgz; \
      echo "${sha}  /tmp/cursor.tgz" | sha256sum -c -; \
      tar --strip-components=1 -xzf /tmp/cursor.tgz -C "$dir"; \
      rm -f /tmp/cursor.tgz; \
      ln -sf "$dir/cursor-agent" /usr/local/bin/cursor-agent; \
      cursor-agent --disable-auto-update --version; \
    fi

# Kiro CLI (kind="kiro", docs/log/43 / docs/decisions/0026), the former Amazon Q Developer CLI,
# shipped as versioned native binaries (bundled kiro-cli + kiro-cli-chat + kiro-cli-term). The
# official manifest (prod.download.cli.kiro.dev/stable/latest/manifest.json) lists versioned
# URLs with sha256, so it bakes the same way as agy: versioned URL + sha256 pin. The zip holds
# `kirocli/{BUILD-INFO,bin/*,install.sh}`. Unpacked it is ~855MB, an order of magnitude larger
# than the rest, so the default (BAKE_AGENT_CLIS=0) does not bake it and leaves it to the
# on-demand install (workspace-agent install-kiro / automatic install on first start, §4-2
# decision); only BAKE_AGENT_CLIS=1, the knob you set knowing the cost, bakes it.
#
# Per-arch assets: x86_64 uses the gnu build (install.sh needs glibc >= 2.34; Debian 13 has
# 2.41), aarch64 the musl build (the gnu one needs glibc 2.39 and did not run on the old
# Debian 12 base — measured). Debian 13 (glibc 2.41) would satisfy the gnu build too, but it
# stays on musl: swapping a working, verified path in the same change as a base update leaves
# nothing to bisect when something breaks (docs/decisions/0068 decision 6).
# The sha256 values are copied from the manifest as they are (bump KIRO_VERSION and both
# arches' sha together; read them with `curl -fsSL .../stable/latest/manifest.json | jq`).
#
# Self-update (on by default) has no build-time ENV knob: it is a setting,
# `app.disableAutoupdates` in ~/.kiro/settings/cli.json, so it is held at runtime (the
# entrypoint re-pins it on every start, plus the per-kind ensureSettings). Unlike copilot's
# COPILOT_AUTO_UPDATE, a build ENV cannot stop it.
ARG KIRO_VERSION=3.3.0
ARG KIRO_SHA256_X64=018e7d19124ebf5088bc33b12dcafa5c7e8de745ae491df024afebcd19f36b62
ARG KIRO_SHA256_ARM64=0218b5440cbdb11418887ad3f7a7cf00054effd47979fd6b0ca37cfc5d8a2d20
# Muse Code (kind="muse", ADR 0095 decision 8). Proprietary, so it is not in the distributed
# image (BAKE_AGENT_CLIS=0 by default; the reasons are under "Distribution knobs" above and in
# NOTICE). The default path is the per-user on-demand install (workspace-agent install-muse),
# which takes this pin from versions.json as the version to fetch. Only BAKE_AGENT_CLIS=1 (the
# knob you set knowing the cost) bakes it into /usr/local/bin.
#
# What is downloaded is the binary itself, not the vendor's bash launcher. The launcher checks
# the channel hourly by default and rewrites itself (MUSE_UPDATE_INTERVAL_SECONDS=3600,
# measured); the manifest's artifact runs standalone with no self-update path, which is what
# makes the pin mean anything.
#
# Version and assets come from the version-specific release manifest:
#   curl -s 'https://api.meta.ai/muse-code/channels/muse-stable'      # version + manifest_url (anonymous)
#   curl -s "<manifest_url>" | jq .artifacts.x86_linux,.artifacts.aarch64_linux
# Copy the sha256 from `artifacts.{x86_linux,aarch64_linux}.checksum` as is (bump MUSE_VERSION
# and both arches' sha together). Measured 362,440,920 B / 323,360,856 B (1.4.1-R4503.1).
# The version string is `Muse Code 1.3.0 (1.3.0-R3401.1)`: the build id in parentheses is what
# is matched against the pin. agy's `tr -dc '0-9.'` approach would drop `-R3401.1` and never
# match (agents/muse/install.go).
ARG MUSE_VERSION=1.4.20-R8619.1
ARG MUSE_SHA256_X64=3685b023d45a1a07c8c559da00088bdf59564920b13a85adad11e13ad6f4d181
ARG MUSE_SHA256_ARM64=7159d6431fc5ff4c9dea32c62d0b771f5089d14389ec672ae903755bd39374dc
# Postgres supply pin (ADR 0086 P0 supply lane): hash of the Zonky embedded-postgres 17.11.0 jar,
# checked with `curl -sfL <jar-url> | sha256sum`.
# For the other majors (16/17/18) install-postgres fetches Maven Central's .sha256 sidecar at run time.
ARG POSTGRES_VERSION=17.11.0
ARG POSTGRES_SHA256_X64=0dd7b72b6f335b8ecfb355fa24c5781e8a93edd09880bb77eb52ebbf29b3e96d
ARG POSTGRES_SHA256_ARM64=8b042e0ea418b1927d95399207950da0734d27fe01e29503ade3bdc464ad4c2b
# MySQL supply pin (ADR 0086 P1 supply lane): hashes of the official tarballs.
# x86_64: the minimal tarball (66,009,936 bytes), sha256 computed from the file.
# arm64:  the full tarball (909,017,708 bytes), sha256 computed from the file (no minimal exists).
# Both downloaded from cdn.mysql.com/archives/mysql-8.4/ and checked with sha256sum.
ARG MYSQL_VERSION=8.4.6
ARG MYSQL_SHA256_X64=f284b17b9e038adbe77f0dd5fb11ed30262286b23a390b8b4e367abc3574c42e
ARG MYSQL_SHA256_ARM64=ab2c553ee4349a6bbe62325d4efcb427204e389264c4759ca32a92eae9aa2abc
# The binaries are copied straight from the archive: the bundled install.sh of 3.1.0 ignores
# Q_INSTALL_GLOBAL and writes to ~/.local/bin (measured: `kiro-cli: not found` right after it).
# 3.x ships kiro-cli alone; 2.x also had kiro-cli-chat and kiro-cli-term, so those are optional.
RUN if [ "${BAKE_AGENT_CLIS}" = "1" ]; then set -eux; \
      arch="$(dpkg --print-architecture)"; \
      case "$arch" in \
        amd64) kasset="kirocli-x86_64-linux.zip";      sha="${KIRO_SHA256_X64}" ;; \
        arm64) kasset="kirocli-aarch64-linux-musl.zip"; sha="${KIRO_SHA256_ARM64}" ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
      esac; \
      curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "https://prod.download.cli.kiro.dev/stable/${KIRO_VERSION}/${kasset}" -o /tmp/kiro.zip; \
      echo "${sha}  /tmp/kiro.zip" | sha256sum -c -; \
      unzip -q /tmp/kiro.zip -d /tmp/kiro; \
      for b in kiro-cli kiro-cli-chat kiro-cli-term; do \
        if [ -f "/tmp/kiro/kirocli/bin/$b" ]; then install -m 0755 "/tmp/kiro/kirocli/bin/$b" "/usr/local/bin/$b"; fi; \
      done; \
      rm -rf /tmp/kiro.zip /tmp/kiro; \
      kiro-cli --version; \
    fi

# Muse Code bake (BAKE_AGENT_CLIS=1 only). The artifact is a standalone binary, so it needs no
# versioned directory or launcher: one file at /usr/local/bin/muse. The `--version` gate keeps
# a truncated or wrong-arch binary out of the image, the same job as the on-demand side's
# museSanityCheck (except that here it fails the build).
RUN if [ "${BAKE_AGENT_CLIS}" = "1" ]; then set -eux; \
      arch="$(dpkg --print-architecture)"; \
      case "$arch" in \
        amd64) masset="muse-x86-linux";     sha="${MUSE_SHA256_X64}" ;; \
        arm64) masset="muse-aarch64-linux"; sha="${MUSE_SHA256_ARM64}" ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
      esac; \
      curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors \
        "https://lookaside.facebook.com/lookaside/muse/download/?channel=muse&version=${MUSE_VERSION}&file=${masset}" \
        -o /usr/local/bin/muse; \
      echo "${sha}  /usr/local/bin/muse" | sha256sum -c -; \
      chmod 0755 /usr/local/bin/muse; \
      muse --version; \
    fi

# Write the build-time pins into the container (ARGs are gone after the build and cannot be
# read at runtime). They feed the "pin vs installed" view of the settings UI's tool versions
# and the smoke checks, and in the lean variant (BAKE_AGENT_CLIS=0 / BAKE_OPTIONAL_TOOLS=0)
# the entrypoint's boot-install and the on-demand installs use them as the versions to fetch
# (docs/log/35 §35.4.1). Every pin is written regardless of the BAKE knobs.
# chromium_cft is amd64's on-demand chromium pin, a Chrome for Testing browser version (the
# same build as playwright 1.61's bundled build 1228; playwright 1.61 moved x64 from
# builds/chromium to CfT — measured in P4, docs/log/35 §35.9-7(a)).
# chromium_dl is the build-number pin for the older builds/chromium layout arm64 uses.
# noto_cjk is the notofonts/noto-cjk tag install-chromium takes the CJK fonts from
# (for the lean rootfs's on-demand install — docs/log/35 §35.7.2-4).
ARG CHROMIUM_CFT_VERSION=149.0.7827.55
ARG CHROMIUM_DL_VERSION=1228
ARG NOTO_CJK_VERSION=Sans2.004
# Bake the bind mount points into the rootfs: docker creates a -v target directory at run
# time, but in native rootfs mode (/ is read-only under bwrap --ro-bind — docs/log/35
# §35.7.2) bwrap cannot mkdir the target and the start dies at once. All four are baked empty.
RUN mkdir -p /var/lib/af/claude /var/lib/af/keep /usr/local/share/agent-fleet/docs /usr/lib/jvm
# claude / keep / docs are owned by dev: claude auth login writes .credentials.json /
# settings.json into CLAUDE_CONFIG_DIR (claude/auth.go). On docker / native a host bind or a
# direct EFS mount covers these, so the owner does not matter; but an ECS EFS access point
# cannot carry a PosixUser (see the comment on ensureAccessPoint) and relies on Docker copying
# the image path's ownership onto a fresh volume, so a root-owned image path leaves dev
# (uid 1000) unable to write anything (seen on the dev deployment as EACCES: permission
# denied). That copy happens only the first time the access point's root gets content, so an
# existing EFS directory already initialised as root is not fixed by a corrected image — it
# needs `docker exec -u root … chown -R 1000:1000 /var/lib/af/claude`. keep (/var/lib/af/keep)
# is chowned too so a newly created keep access point (decision 3-6) starts dev-owned.
# docs has no bind-mount path on ECS: the Agent fetches the role's docs from the CP and
# unpacks them here (docs/build/04 §4.9, agent docs_sync.go).
RUN chown 1000:1000 /var/lib/af/claude /var/lib/af/keep /usr/local/share/agent-fleet/docs
# /scratch is the task-local work disk (ADR 0044 decision 3). On ECS it is a plain directory
# on ephemeral storage (an EBS mount point only above 200 GiB), and the entrypoint moves the
# piles of small files (go-build / uv / go modules) there. It is useless unless dev can write
# it, hence the chown. On docker / native the CP does not inject AF_WS_SCRATCH, so it stays
# empty and unused.
RUN mkdir -p /scratch && chown 1000:1000 /scratch
# Google Cloud SDK pin (ADR 0107 decision 4). Never baked: it unpacks to ~486 MB, which every
# cold image pull would pay for a tool a fraction of members use. `workspace-agent
# install-gcloud` installs this version into ~/.local on demand and refuses an archive whose
# sha256 differs from the one recorded for the build arch. Google publishes checksums only for
# the current release, so a bump downloads both archives
# (dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-<ver>-linux-{x86_64,arm}.tar.gz)
# and records `sha256sum` of each here.
ARG GCLOUD_VERSION=587.0.0
ARG GCLOUD_SHA256_X64=57df2448d259c654796a3703af8e5b53a02d439715b2034d6bb811efc2d6dd7b
ARG GCLOUD_SHA256_ARM64=8349b151da42f07136294da0908624fe8ea16fea200cb2f4412ad081a86e890c
RUN set -eux; mkdir -p /usr/local/share/agent-fleet; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) agy_sha="${AGY_SHA256_X64}"; cursor_sha="${CURSOR_SHA256_X64}"; kiro_sha="${KIRO_SHA256_X64}"; muse_sha="${MUSE_SHA256_X64}"; pg_sha="${POSTGRES_SHA256_X64}"; mysql_sha="${MYSQL_SHA256_X64}"; gcloud_sha="${GCLOUD_SHA256_X64}" ;; \
      arm64) agy_sha="${AGY_SHA256_ARM64}"; cursor_sha="${CURSOR_SHA256_ARM64}"; kiro_sha="${KIRO_SHA256_ARM64}"; muse_sha="${MUSE_SHA256_ARM64}"; pg_sha="${POSTGRES_SHA256_ARM64}"; mysql_sha="${MYSQL_SHA256_ARM64}"; gcloud_sha="${GCLOUD_SHA256_ARM64}" ;; \
      *) agy_sha=""; cursor_sha=""; kiro_sha=""; muse_sha=""; pg_sha=""; mysql_sha=""; gcloud_sha="" ;; \
    esac; \
    printf '{"claude":"%s","opencode":"%s","codex":"%s","agy":"%s","agy_build":"%s","agy_sha256":"%s","copilot":"%s","cursor":"%s","cursor_sha256":"%s","kiro":"%s","kiro_sha256":"%s","muse":"%s","muse_sha256":"%s","postgres":"%s","postgres_sha256":"%s","mysql":"%s","mysql_sha256":"%s","rtk":"%s","go":"%s","gh":"%s","bkt":"%s","jira":"%s","confluence":"%s","chromium":"%s","chromium_cft":"%s","chromium_dl":"%s","noto_cjk":"%s","mcp_grafana":"%s","cloudwatch_mcp":"%s","aws_mcp_proxy":"%s","awscli":"%s","session_manager_plugin":"%s","gcloud":"%s","gcloud_sha256":"%s"}\n' \
      "$CLAUDE_CODE_VERSION" "$OPENCODE_VERSION" "$CODEX_VERSION" "$AGY_VERSION" "$AGY_RELEASE_BUILD" "$agy_sha" "$COPILOT_VERSION" "$CURSOR_VERSION" "$cursor_sha" "$KIRO_VERSION" "$kiro_sha" "$MUSE_VERSION" "$muse_sha" "$POSTGRES_VERSION" "$pg_sha" "$MYSQL_VERSION" "$mysql_sha" "$RTK_VERSION" "$GO_VERSION" "$GH_VERSION" "$BKT_VERSION" "$JIRA_CLI_VERSION" "$CONFLUENCE_CLI_VERSION" "$CHROMIUM_VERSION" \
      "$CHROMIUM_CFT_VERSION" "$CHROMIUM_DL_VERSION" "$NOTO_CJK_VERSION" "$MCP_GRAFANA_VERSION" "$CLOUDWATCH_MCP_VERSION" "$AWS_MCP_PROXY_VERSION" "$AWSCLI_VERSION" "$SESSION_MANAGER_PLUGIN_VERSION" \
      "$GCLOUD_VERSION" "$gcloud_sha" \
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
# autoUpdate, so it needs no analogous flag.) opencode 2 also has the `update` config key for the
# same thing (`"disable"`), which entrypoint.sh seeds.
ENV OPENCODE_DISABLE_AUTOUPDATE=true

# Muse Code self-update off (ADR 0095 decision 8). The distributed image does not ship muse
# (proprietary, BAKE_AGENT_CLIS=0 by default), and what AF installs is the manifest's binary,
# which has no self-update path. This line is for something else: a user who runs the vendor's
# install.sh gets a bash launcher at `~/.local/bin/muse` that checks the channel hourly by
# default and rewrites itself (MUSE_UPDATE_INTERVAL_SECONDS=3600, measured). It stops an
# unmanaged self-updating binary from quietly drifting.
#
# Detecting and repairing that shadow is the job of Check in agents/muse/install.go, which
# decides by version match, not by path: the vendor's default location is the same
# `~/.local/bin/muse` AF uses, so a path check would report AF's own binary as the shadow.
ENV MUSE_NO_AUTO_UPDATE=1

# NEVER let git fall back to an interactive /dev/tty credential prompt. This fixes a
# real "the claude session accepts NO keyboard input" bug (gate k, docs/log/35 §35.9-10):
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
# b1c1c41 / an earlier revision of docs/log/35 §35.9-10 — the real cause is the git
# credential prompt handled just above; these flags never stop `gh pr view`). Kept only
# as harmless hardening for restricted/air-gapped networks and privacy, and it aligns
# with the version-pinned CLI story (DISABLE_AUTOUPDATER above).
#
# NOT harmless any more, and NOT free to remove: these also turn OFF cross-session
# messaging (claude sessions messaging each other directly). Measured on 2.1.226 —
# NONESSENTIAL_TRAFFIC *and* DISABLE_TELEMETRY each gate it on their own, so BOTH must
# be unset for the feature to come back, and DISABLE_ERROR_REPORTING is irrelevant to
# it. (The upstream env-var table says DISABLE_TELEMETRY does not affect feature-flag
# evaluation; it does. docs/log/58 §58.12 has the 3-runs-per-config matrix.)
#
# ⚠️ THAT NO LONGER HOLDS (measured 2026-08-31 on 2.1.251, docs/log/58 §58.16). With BOTH
# vars set to 1 in the live process, claude still advertises ListAgents / SendMessage and
# still delivers over /tmp/cc-socks/<pid>.sock — verified end to end on a real pair of
# sessions. **So do NOT read this block as the cross-session block any more.**
#
# The block now lives in the Agent, as launch settings on every claude session it starts:
# workspace/agent/internal/agents/claude/program.go `nativePeerSettings`
# (permissions.deny on the two tools + crossSessionInbound:"refuse", docs/log/58 §58.17).
# It is code, so it takes effect on an Agent restart rather than an image rebuild — which
# is also why it is no longer here. Keep these vars anyway: they still do what the
# paragraph above says about optional/background traffic. ADR 0041 decision 1 is unchanged
# (the native path stays off); only the mechanism moved.
#
# NONESSENTIAL_TRAFFIC also turns off feature-flag evaluation, and Remote Control will not
# start while it is set (silent: the claude session runs, no Remote Control session is created).
# So while the Console's Remote control toggle is on, the Agent removes this one variable
# (`env -u`) from the claude processes it starts afterwards (agents/claude/program.go
# `launchCommand`); the other three stay set, and with the toggle off the environment is
# exactly this one. docs/log/58 §58.18 has the measurement.
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

# UTF-8 locale. Unset (POSIX), tmux disables UTF-8 and Japanese and other non-ASCII text
# turns to garbage. C.UTF-8 ships with Debian and needs no locale generation.
ENV LANG=C.UTF-8 LC_ALL=C.UTF-8

# tmux: confines an attached client to one session. It goes in /etc/tmux.conf because /etc
# is not covered by a bind mount (unlike home, it always survives). It is read when the agent
# first runs tmux and starts the server.
COPY tmux.conf /etc/tmux.conf

# Rename node:22's default user node (uid/gid=1000) to dev, since "node" is easily confused
# with Node.js. uid/gid stay 1000 (matching the host's uid 1000, so bind mounts pass through).
# -m moves home from /home/node to /home/dev (at run time a bind mount covers home anyway).
RUN usermod -l dev -d /home/dev -m node && groupmod -n dev node

# claude installs into ~/.local/bin; add it to the default PATH for docker exec and the like
# (non-login shells, the Agent). Go prepends /usr/local/go/bin (go itself) and ~/go/bin
# (`go install` target).
ENV PATH="/home/dev/.local/bin:/home/dev/go/bin:/usr/local/go/bin:${PATH}"
# A login shell (bash -l) overwrites PATH in /etc/profile, so prepend in profile.d too, or a
# terminal may not find claude / go.
RUN echo 'export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"' > /etc/profile.d/claude-path.sh

# ── From here on: frequently changing, cheap inputs (mostly COPY), grouped at the end so
# they do not invalidate the go/npm layers above. All run as root, so before USER dev.
COPY --from=agentbuild /out/workspace-agent /usr/local/bin/workspace-agent
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
# gh transparent-auth wrapper (real binary in /usr/local/libexec/gh); see docs/build/08 §8.3.
COPY gh-auth-wrapper.sh /usr/local/bin/gh
# bkt (Bitbucket CLI) transparent-auth wrapper, same idea (real binary in /usr/local/libexec/bkt).
COPY bkt-auth-wrapper.sh /usr/local/bin/bkt
# jira (Jira CLI) transparent-auth wrapper, same idea (real binary in /usr/local/libexec/jira).
COPY jira-auth-wrapper.sh /usr/local/bin/jira
# confluence (Confluence CLI) transparent-auth wrapper over the same Atlassian connection
# (real CLI in /usr/local/libexec/confluence).
COPY confluence-auth-wrapper.sh /usr/local/bin/confluence
# svn transparent-auth wrapper. Same shape as the gh one, except the real binary stays
# where apt put it (/usr/bin/svn) and the shim only takes precedence through PATH order —
# /usr/local/bin comes first. It resolves the credential from the encrypted store, so an
# `svn update` typed in a session authenticates like the Console's own update button
# (docs/log/41 amendment).
COPY svn-auth-wrapper.sh /usr/local/bin/svn
# Helper that moves build output onto the task-local work disk (ADR 0044 decision 3).
COPY af-scratch.sh /usr/local/bin/af-scratch
# ADR 0086 P0 shim: /usr/local/bin/af-db forwards to `workspace-agent af-db`. Same shape as
# af-scratch; the implementation lives in workspace-agent (L2 lane).
COPY af-db.sh /usr/local/bin/af-db
# AF agent memory from the command line (ADR 0108): same shape as af-db, the implementation is
# `workspace-agent af-memory`.
COPY af-memory.sh /usr/local/bin/af-memory
# The Console's low-disk cleanup from a terminal (#3882): same shape, `workspace-agent af-clean`.
COPY af-clean.sh /usr/local/bin/af-clean
# After an architecture change, reinstalls the user's own ~/.local installs at the same
# versions (called from the entrypoint). A separate file rather than part of the entrypoint
# so it can be run and tested on its own.
COPY af-arch-repair.sh /usr/local/bin/af-arch-repair
# Runs one command under one SSO profile's short-lived credentials, never the workload
# role (issue #998). Same shape as af-db: the implementation is `workspace-agent aws-exec`.
COPY af-aws-exec.sh /usr/local/bin/af-aws-exec
# Runs one command with one Google Cloud profile's access token from the Agent's own gcloud
# store, never the VM's or node's identity (ADR 0107 decision 2): `workspace-agent gcloud-exec`.
COPY af-gcloud-exec.sh /usr/local/bin/af-gcloud-exec
# Webhook hooks for private repositories (ADR 0133 step 4): `workspace-agent watch-hook`.
COPY af-watch-hook.sh /usr/local/bin/af-watch-hook
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/gh /usr/local/bin/bkt /usr/local/bin/jira /usr/local/bin/confluence /usr/local/bin/svn /usr/local/bin/af-scratch /usr/local/bin/af-db /usr/local/bin/af-memory /usr/local/bin/af-clean /usr/local/bin/af-arch-repair /usr/local/bin/af-aws-exec /usr/local/bin/af-gcloud-exec /usr/local/bin/af-watch-hook
# opencode plugins source (seeded into ~/.config/opencode/plugin at runtime):
# agent-fleet-status.js (session working/idle bridge — seeded by the entrypoint) +
# agent-fleet-caller.js (stamps the calling opencode session on af's MCP tools so a
# Managed session can be told apart from its worktree mates, #989 — seeded likewise) +
# rtk.ts (token-saving command rewrite, the opencode analog of claude's rtk hook —
# seeded/removed by the agent per the ~/.config/agent-fleet/rtk.json toggle).
COPY opencode-plugin/ /usr/local/share/agent-fleet/opencode-plugin/

# Claude reads the baked managed policy; other CLIs receive it through reconciliation
# because their instruction files live on persistent home storage. Topic files are
# read on demand through the policy index and registered as skills where supported.
COPY workspace-notes.md /usr/local/share/agent-fleet/workspace-notes.md
COPY notes/ /usr/local/share/agent-fleet/notes/
RUN mkdir -p /etc/claude-code \
 && cp /usr/local/share/agent-fleet/workspace-notes.md /etc/claude-code/CLAUDE.md

# Docker grants SYS_ADMIN only in the capability bounding set so Chromium's
# root-owned setuid helper can create its PID/network namespaces. Keep that path
# exclusive: the normal USER dev process has no effective capabilities, and no
# unrelated setuid/setgid utility may acquire the enlarged bounding set.
# With BAKE_OPTIONAL_TOOLS=0 there is no chromium, so the sandbox helper is not expected and
# the check is only that no setuid/setgid file remains (docs/log/35 §35.7.1-5).
RUN find / -xdev -type f -perm /6000 ! -path /usr/lib/chromium/chrome-sandbox \
      -exec chmod a-s {} + \
 && if [ "${BAKE_OPTIONAL_TOOLS}" = "1" ]; then \
      test "$(stat -c '%u:%g:%a' /usr/lib/chromium/chrome-sandbox)" = "0:0:4755"; \
    else \
      test ! -e /usr/lib/chromium/chrome-sandbox; \
    fi \
 && test -z "$(find / -xdev -type f -perm /6000 \
      ! -path /usr/lib/chromium/chrome-sandbox -print -quit)"

# fontconfig's binary cache under /var/cache/fontconfig is written by the apt trigger at
# build time and carries mtimes and checksums, so its bytes differ on every build. The
# release scanner folds every byte, so a short forbidden term can match inside it by
# chance and fail a publish (#2600). Drop it last, after the final apt-get / fc-cache:
# fontconfig rebuilds it on first use into ~/.cache/fontconfig (the dir is root-owned and
# the process runs as dev); measured ~0.2s once per home for DejaVu + Noto CJK.
# workspace/dockerfile-fontconfig-cache-test.sh pins this step's position.
RUN rm -rf /var/cache/fontconfig/*

USER dev
WORKDIR /home/dev
EXPOSE 7700
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
# Name it by absolute path. A bare `workspace-agent` goes through PATH, and a build someone
# left in `~/.local/bin` (which survives a recreate) permanently hijacks the image's binary —
# seen live, with the Agent still running an old local P0-era build (ADR 0086, the on-hardware section).
CMD ["/usr/local/bin/workspace-agent"]
