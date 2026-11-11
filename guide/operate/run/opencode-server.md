---
audience: "Whoever manages the server and version behind opencode's terminal sessions"
source_of_truth: "The Workspace Agent's opencode launch code, `OPENCODE_VERSION` in `workspace/Dockerfile`"
---

# Run opencode's shared server

English | [日本語](opencode-server.ja.md)

opencode's Terminal (CLI) sessions connect to one shared server per workspace.
When you would rather have isolation than memory, switch to a dedicated server per session; when a release misbehaves, pin an earlier one.

## How it works

| What you do | What happens |
|---|---|
| Nothing | Terminal sessions share one opencode server run by the Workspace Agent |
| Start the Workspace Agent with `AF_OPENCODE_TUI_STANDALONE=1` | Terminal sessions started from then on each run on a dedicated server |
| Rebuild the image with an earlier 2.x release | You avoid the release that misbehaves |

What members see differently is in [Connect OpenCode](../../user/agents/opencode.md#good-to-know).

## Switch to dedicated servers

Start the Workspace Agent with this in its environment:

```
AF_OPENCODE_TUI_STANDALONE=1
```

- Each session uses roughly 150–250 MB more memory (measured idle)
- It applies to Terminal sessions started after the Agent started with this setting. Running sessions keep the mode they started in
- `AF_OPENCODE_SERVE_DISABLE=1` (managed opencode off) also gives dedicated servers, because there is no shared server to join
- In a dedicated server's pane, forms with several questions and multiple-choice forms do not appear in the Console. Answer them in the terminal

## Pin an earlier release

The image pins an opencode 2 release (`OPENCODE_VERSION` in `workspace/Dockerfile`). Agent Fleet supports opencode 2 only.

1. Rebuild the image with an earlier 2.x release (`--build-arg OPENCODE_VERSION=<2.x>`)
2. Stop and start the workspace

Every 2.x release reads the same database.

> [!WARNING]
> **Going back to opencode 1 is not supported.** opencode 1 sees nothing opencode 2 created, and Agent Fleet no longer handles opencode 1.

## Workspaces that came from opencode 1

| Situation | What happens |
|---|---|
| Last used with opencode 1 | On opencode 2's first start, `~/.local/share/opencode/opencode.db` is converted in place and opencode 1's sessions come in under the same IDs. However long ago the workspace was last used, Agent Fleet waits for the import to finish before resuming |
| Tried earlier with `workspace-agent opencode-major 2` | A dedicated install (`~/.local/share/agent-fleet/opencode2`) and a dedicated database (`~/.local/share/agent-fleet/opencode2-data`) are left behind. Neither is read |

Sessions that exist only in the dedicated database are **not merged** into the main one. Delete the two directories when you no longer need them; Agent Fleet does not delete them.

## Related

- [Connect OpenCode](../../user/agents/opencode.md)
- [Upgrade](upgrade.md)
