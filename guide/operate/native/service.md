---
audience: "someone who wants the native Agent Fleet to keep running after the terminal closes"
source_of_truth: "`deploy/native/af` and the distribution repo's `install.sh` (a contradiction is a bug in this page)"
---

# Run it as a systemd service

English | [日本語](service.ja.md)

Instead of keeping `af start` in the foreground, make it a systemd **user** service.
It restarts when it fails and survives your shell. systemd is on by default in WSL2.

## How it works

| What you do | What happens |
|---|---|
| Write the unit file | `af start` becomes a user service |
| Enable it with `systemctl --user enable --now` | It starts now, and at every login |
| Enable linger if you need it | It keeps running after you close the WSL session |

## 1. Write the unit

Create `~/.config/systemd/user/agent-fleet.service`:

```ini
[Unit]
Description=Agent Fleet (native)

[Service]
ExecStart=%h/.local/bin/af start
Restart=on-failure
# Lets the Console's "restart to apply" button (auto-update, below) find this
# unit; %N expands to the unit name (agent-fleet).
Environment=AF_SYSTEMD_UNIT=%N

[Install]
WantedBy=default.target
```

- `%h` is your home directory. The `ExecStart` path matches an installer install (the `~/.local/bin/af` symlink)
- If you extracted the tar by hand, point it at that copy, e.g. `ExecStart=%h/agent-fleet-native-<v>-linux-<arch>/af start`. `readlink -f "$(command -v af)"` finds the real path
- `AF_SYSTEMD_UNIT` lets the Console's "**Restart to apply**" find this unit ([Update the native package](update.md#switch-to-the-staged-version))
- Environment variables for read-aloud, images or borrowed engines go into `[Service]` as `Environment=` lines (e.g. `Environment=AF_VOICEVOX_URL=...`)

## 2. Enable it

Stop the foreground `af start` first if it is still running. Otherwise the service fails to bind port 8099.

```bash
systemctl --user daemon-reload && systemctl --user enable --now agent-fleet
systemctl --user status agent-fleet          # expect: Active: active (running)
```

## 3. Keep it running after you close the session

To keep it running after you close the WSL session, enable linger.

```bash
loginctl enable-linger "$USER"
```

## Read the logs

```bash
journalctl --user -u agent-fleet -f
```

## When it goes wrong

| Symptom | Where to look |
|---|---|
| `systemctl --user` reports `Failed to connect to bus` | User systemd is not running. On WSL2, set `systemd=true` under `[boot]` in `/etc/wsl.conf`, then `wsl.exe --shutdown` and reopen |
| The service fails right after it starts | Is a foreground `af start` still holding port 8099? |
| The `ExecStart` command is not found | If you extracted the tar by hand, use the path `readlink -f "$(command -v af)"` prints |

## Related

- [Install the native package](install.md)
- [Update the native package](update.md)
- [Reset or uninstall](uninstall.md)
