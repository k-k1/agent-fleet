# Control Plane image (P3-10 段1). Multi-stage, self-contained:
#   1. console  — builds the Vite + React Console into /app/console (served no-store)
#   2. build    — compiles the CP into a static binary (CGO off; modernc/sqlite is pure Go)
#   3. runtime  — debian-slim + the `docker` CLI for docker-out-of-docker (DooD)
#
# Build context is the REPO ROOT (needs both console/ and control-plane/):
#   docker build -f control-plane/Dockerfile -t agent-fleet/control-plane:dev .
# or via deploy/compose/docker-compose.yml (`build.context: ../..`).
#
# DooD note: the CP shells out to `docker` (runtime.go) against the HOST daemon
# via the mounted /var/run/docker.sock — see docs/history/p3-10-packaging.md §20.3.
# syntax=docker/dockerfile:1

# --- 1. console (Vite + React) ----------------------------------------------
FROM node:22-bookworm AS console
WORKDIR /src/console
COPY console/package.json console/package-lock.json ./
RUN npm ci
COPY console/ ./
# mermaid/marp are heavy; raise the Node heap so the prod build doesn't OOM.
RUN NODE_OPTIONS=--max-old-space-size=4096 npm run build   # -> /src/console/dist

# --- 2. control-plane (Go) ---------------------------------------------------
FROM golang:1.26-bookworm AS build
WORKDIR /src/control-plane
COPY control-plane/go.mod control-plane/go.sum ./
RUN go mod download
COPY control-plane/ ./
# Static binary: modernc.org/sqlite is pure Go, so CGO can stay off. Migrations
# (migrations/*.sql) are go:embed'd into the binary — no external files needed.
# VERSION stamps the binary (startup log + GET /api/version, docs/35 §35.6.1);
# only the release pipeline passes a real value — dev builds stay "dev".
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.buildVersion=${VERSION}" -o /out/af-cp .

# --- 3. runtime --------------------------------------------------------------
FROM debian:bookworm-slim
# Links the GHCR package to this repository (ADR 0037): the container registry
# resolves org.opencontainers.image.source to decide which repo a package belongs
# to, which is what puts the README on the package page and lets the package
# inherit the repository's access. Without it the package is orphaned.
LABEL org.opencontainers.image.source="https://github.com/k-k1/agent-fleet" \
      org.opencontainers.image.description="Agent Fleet control plane — web console for running AI coding agents as a managed fleet" \
      org.opencontainers.image.licenses="Apache-2.0"
# git provides git-http-backend (/usr/lib/git-core/git-http-backend), which the
# CP wraps to self-host tenant repos over smart-HTTP (docs/reference/internal-git-provider).
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git \
 && rm -rf /var/lib/apt/lists/*
# docker CLI (DooD). Pulled from the official docker image so it matches the
# host engine's client; pin the major to keep releases reproducible.
COPY --from=docker:29-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build         /out/af-cp            /usr/local/bin/af-cp
COPY --from=console       /src/console/dist     /app/console
# Docs, baked so the CP can stage a role-scoped subset into each Workspace container
# at start (workspace_docs.go). Context is the repo root, so docs/ is reachable.
# DOCS_SRC lets the release pipeline point this COPY at a staged tree with the
# internal denylist (docs/.distignore) applied — distribution images must not
# carry HANDOFF/talk/history etc. (docs/35 §35.4.3). Self-hosted dev/ops builds
# keep the default (full tree).
ARG DOCS_SRC=docs
COPY ${DOCS_SRC}/         /usr/local/share/agent-fleet/docs/
# HOME is set so the docker CLI has a writable config dir when the container runs
# as an arbitrary uid (compose sets user: "1000:1000"; that uid has no /etc/passwd
# home). CONSOLE_DIR points the CP at the baked-in Console assets.
ENV HOME=/tmp \
    CONSOLE_DIR=/app/console \
    CP_ADDR=127.0.0.1:8099
EXPOSE 8099
ENTRYPOINT ["/usr/local/bin/af-cp"]
