---
audience: "someone keeping a running deployment healthy"
source_of_truth: "the scripts under `deploy/` — a command here that contradicts the script it describes is a bug in this page"
---

# 03. Day-to-Day Operations

English | [日本語](03-run.ja.md)

This chapter has moved to one page per task.

| Page | What you can do |
|---|---|
| [Back up and restore](run/backup-restore.md) | Run `backup.sh` on a schedule and restore on another host |
| [Upgrade](run/upgrade.md) | Move to a new release and move workspaces onto the new image |
| [Install into a closed network](run/air-gapped.md) | Provide images, TLS, CLIs and diagram icons on a host that cannot reach the outside |
| [Give every agent instructions](run/fleet-policy.md) | Have every agent read operating rules through the fleet policy |
| [Run opencode's shared server](run/opencode-server.md) | Manage the server behind Terminal sessions and the opencode release |
| [Expose it to the internet](run/public-internet.md) | Know what gets through when probes arrive, and narrow the entry |
| [Tell deployments apart](run/deployment-label.md) | Give each deployment a colour and a label |
| [Use idle stop and force-stop](run/idle-stop.md) | Set the auto-stop times, stop workspaces that will not stop, pause the deployment |
