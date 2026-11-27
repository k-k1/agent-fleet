---
audience: "someone wrapping up for the day"
source_of_truth: "the Console's workspace action bar (the power button and the Restart needed badge)"
---

# Stop the workspace at the end of the day

English | [日本語](end-of-day.ja.md)

When you are done, you can stop your workspace. Stopping interrupts any running turns. Repositories and changes in your home directory and your session histories are kept.

## How it works

| What you do | What happens |
|---|---|
| Commit and push the changes that matter | The changes also exist outside the workspace |
| Stop it with the power button on the workspace action bar | Running sessions stop. On deployments with a working disk, the contents of `/scratch` are deleted |
| Start it with the power button the next day | You can open and resume stopped sessions |

## Before you stop

- Commit and push the changes that matter. Uncommitted changes exist only inside the workspace ([Connect a Git service](../code/connect-git.md#push))
- To let a running turn finish, wait for it to end before stopping the workspace
- On deployments with a working disk, copy any files you need from `/scratch` to your home directory before stopping. If `target` or `build` points into `/scratch`, run the build again after starting ([af-scratch](../../ref/cli/af-scratch.md))

## Stop the workspace

1. Press the power button on the workspace action bar
2. Press **Stop** in the confirmation

When it is stopped, these stay.

| What stays | At the next start |
|---|---|
| Cloned repositories and uncommitted changes | Ready to use |
| The session list and history | Open a stopped row and press **Resume and continue** ([Stop and resume a session](../sessions/stop-resume.md)) |
| Logins to agents and Git services | Ready to use |

- While the workspace is stopped, **Session history** in the left pane still lists the sessions. Acting on them waits until it is started
- Previews and opencode web are disconnected

> [!TIP]
> Forgetting to stop it is fine. Where the tenant has auto-stop on, the workspace stops by itself after a while without activity.
> To keep a long job from being stopped, see [Stop when done, or keep a session awake](../sessions/auto-stop.md).

## "A new version is available" and "Restart needed"

When the Console is updated, an **A new version is available** notification appears.

- **Update** on the notification only reloads the browser. Running sessions do not stop
- When the update includes the workspace side as well, a **Restart needed** badge appears on the workspace action bar

![The Restart needed badge on the workspace action bar, pressed. It explains that the workspace runs on the backend from before the update, with Restart now and Later buttons](../../assets/start-restart-en-light.webp)

Press **Restart needed** and choose **Restart now** to stop the workspace and start it again.

- As with stopping and starting, running sessions stop and can be resumed after startup. The contents of your home directory are kept. On deployments with a working disk, the contents of `/scratch` are deleted
- There is no hurry. Stopping at the end of the day and starting the next day applies it too. The badge goes away once the workspace starts on the new version

What changed is in the [release history](../../../deploy/release/notes/SUMMARY.md).

## Related

- [Stop and resume a session](../sessions/stop-resume.md)
- [Stop when done, or keep a session awake](../sessions/auto-stop.md)
- [Recreate the workspace](../workspace/recreate.md): when stopping and starting does not fix it
