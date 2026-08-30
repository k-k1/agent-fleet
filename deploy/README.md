# deploy/ — deployment targets and release tooling

Agent Fleet is a self-hosted web console for running AI coding agents (Claude Code,
Codex CLI, GitHub Copilot CLI, Antigravity CLI, Cursor CLI, Kiro, OpenCode) as a managed fleet: each member gets an
isolated workspace with a persistent home and git working copies, and drives agent
sessions from the browser. This directory contains everything needed to deploy it
and to build/publish its release artifacts.

Workspace isolation depends on the target: the Docker-based targets (compose, AWS
ECS, local dev) run each workspace in its own container with cgroup CPU/memory
quotas; the native runtime uses a bubblewrap (user-namespace) sandbox instead and
applies no cgroup limits.

| Path | Purpose |
|------|---------|
| [`compose/`](compose/README.md) | **On-prem Docker Compose deployment** (the primary self-host target; per-user workspace containers with cgroup quotas). Runbook: setup, keys, backup/restore, upgrades. |
| [`native/`](native/README.md) | **Docker-less native runtime** for WSL2 / single-user Linux hosts. Ships as the `agent-fleet-native-*` tar (`af` launcher; workspace runs in a bubblewrap sandbox). |
| [`aws/ecs/`](aws/ecs/README.md) | **AWS ECS deployment** (CloudFormation templates + `release-ecr.sh` image publishing). |
| [`aws/ec2-single/`](aws/ec2-single/README.md) | Single-EC2 variant (CloudFormation). |
| [`release/`](release/) | Release build & publish tooling: `build.sh` (artifact orchestrator), `publish-dist.sh` (GitHub Releases publish), `dist-repo/` (seed of the public distribution repo incl. `install.sh`). |
| [`local/`](local/) | Local development helpers (`run-dev.sh` etc.), WSL personal-use guide ([README-wsl.md](local/README-wsl.md)), and CI test scripts (stub tests, e2e smoke). |

Release engineering design and gates: [docs/log/35-packaging.md](../docs/log/35-packaging.md). Deployment architecture
and portability: [docs/dev/09-deploy.md](../docs/dev/09-deploy.md).
