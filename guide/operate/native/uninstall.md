---
audience: "someone wiping the data of the native Agent Fleet, or removing it"
source_of_truth: "`af reset` in `deploy/native/af` (a contradiction is a bug in this page)"
---

# Reset or uninstall

English | [日本語](uninstall.ja.md)

Wipe the native package's data and start over, or remove Agent Fleet from the host.
The program and the data live in different places, so you can remove either one alone.

## How it works

| What you do | What happens |
|---|---|
| Run `af reset` | The fixed user's home and claude-config (including the Claude login) are deleted. The DB and the rootfs are kept |
| Run `af reset --all` | All of `WS_DATA` is deleted: the DB, every home, claude-config, the extracted rootfs |
| Remove the unit, the data and the program in turn | Agent Fleet is gone from the host |

## Wipe the data

Stop the Control Plane first; `af reset` refuses while it runs.

```bash
./af reset          # dev user's data only (DB and rootfs are kept)
./af reset --all    # all of WS_DATA (full wipe incl. the extracted rootfs)
```

- It asks before deleting. `--yes` skips the question
- After an installer install, `af` works in place of `./af`

## Uninstall

```bash
# 1. If you set up the systemd user unit, stop and remove it first
systemctl --user disable --now agent-fleet 2>/dev/null
systemctl --user disable --now agent-fleet-update.timer 2>/dev/null   # auto-update timer
rm -f ~/.config/systemd/user/agent-fleet.service \
      ~/.config/systemd/user/agent-fleet-update.service \
      ~/.config/systemd/user/agent-fleet-update.timer
# otherwise just stop `af start` with Ctrl-C

# 2. Wipe all data (skip to keep it — data is separate from the program)
./af reset --all

# 3. Remove the program
#    installed via install.sh:
rm -f ~/.local/bin/af && rm -rf ~/.local/opt/agent-fleet
#    extracted from a tar directly: just delete the extracted directory
```

| Step | Notes |
|---|---|
| 1. Stop and remove the units | Without systemd, just stop `af start` with Ctrl-C |
| 2. Wipe the data | Skip it to keep the data |
| 3. Remove the program | If you extracted the tar directly, delete that directory |

### When `af` is already gone

Remove the data by hand. Go module caches inside workspace homes are write-protected, so restore write permission first.

```bash
chmod -R u+w ~/.local/share/agent-fleet && rm -rf ~/.local/share/agent-fleet
```

Use your `$WS_DATA` path if you overrode it.

## Related

- [Install the native package](install.md)
- [Run it as a systemd service](service.md)
