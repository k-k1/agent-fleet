# Install the native package (no Docker, single user)

This directory holds `af`, the launcher of the Docker-less native package
(`agent-fleet-native-*` tar) for WSL2 and single-user Linux hosts: the control plane runs as
a host process and workspaces run in a bubblewrap sandbox on a downloaded rootfs.

The procedures are in the user guide:

| Page | What it covers |
|---|---|
| [Install the native package](../../guide/operate/native/install.md) | Host requirements, the installer or the tar, `af start`, data locations, Git OAuth, the differences from Docker |
| [Update the native package](../../guide/operate/native/update.md) | `af update`, the daily timer, applying with a restart, pinning, air-gapped hosts |
| [Run it as a systemd service](../../guide/operate/native/service.md) | The systemd user unit |
| [Reset or uninstall](../../guide/operate/native/uninstall.md) | `af reset` and removing the package |
| [Run the read-aloud engine](../../guide/operate/engines/read-aloud.md) | VOICEVOX on Windows or Linux (`AF_VOICEVOX_URL`) |
| [Generate images on your own ComfyUI](../../guide/operate/engines/comfyui.md) | `AF_COMFY_URL`, `AF_ENGINES_JSON` |
| [Borrow another deployment's engines](../../guide/operate/engines/borrow.md) | `AF_REMOTE_ENGINE_URL` / `_TOKEN` / `_KEYS` |
| [Self-hosting at a glance](../../guide/operate/overview.md) | The disclaimer on autonomous agents |

In the package these links do not resolve: the same pages are under `docs/operate/` in the
extracted directory, in the Console's user guide, and on GitHub under `guide/operate/`.
