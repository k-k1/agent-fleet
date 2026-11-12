# Deploy on premises with Docker Compose

This directory is the Docker Compose deployment surface: `docker-compose.yml` (the `cp` and
`caddy` services), `Caddyfile`, `.env.example` (all configuration), `docker-compose.macos.yml`
(the Mac overlay), `backup.sh` / `restore.sh`, `load-images.sh` and `release.sh`.

The procedures are in the user guide:

| Page | What it covers |
|---|---|
| [Deploy with Docker Compose](../../guide/operate/compose/install.md) | Prerequisites, `.env`, starting from source or from the release images, the first administrator |
| [Run on an Apple Silicon Mac](../../guide/operate/compose/macos.md) | The macOS overlay on colima, OrbStack or Docker Desktop, and the native package in a Linux VM |
| [When workspaces cannot reach the CP](../../guide/operate/compose/workspace-listener.md) | The workspace listener (`AF_CP_INTERNAL_LISTEN` / `AF_CP_INTERNAL_URL`) |
| [Back up and restore](../../guide/operate/run/backup-restore.md) | `backup.sh` and `restore.sh` |
| [Upgrade](../../guide/operate/run/upgrade.md) | A new `VERSION` / `WS_IMAGE`, and moving workspaces to the new image |
| [Install into a closed network](../../guide/operate/run/air-gapped.md) | A registry mirror, or `release.sh --save` with `load-images.sh` |
| [Find the cause from the symptom](../../guide/operate/diagnose/checklist.md) | The three constraints of driving Docker from a container, and troubleshooting |
| [Other operational controls](../../guide/operate/secure/controls.md) | Protecting the compose host and the host's cloud identity |
| [Self-hosting at a glance](../../guide/operate/overview.md) | What the operator is responsible for, and the disclaimer on autonomous agents |

In a release bundle these links point at the release's tag on GitHub; the same pages are
also in the Console's user guide.
