---
audience: "Tenant administrators who decide whether members may run their memos automatically"
source_of_truth: "The Console's Tenant settings → \"Operations\" → \"Memo auto-run\", and the Control Plane's memo auto-run rules"
---

# Allow memo auto-run

English | [日本語](memo-auto-run.ja.md)

Decide, for the whole tenant, whether members may give their memo categories auto-run settings, so that the memos waiting in them are run as sessions.
It is blocked to begin with. Turn it on only after the deployment has been updated, as described below.

## How it works

| What you do | What happens on the members' side |
|---|---|
| Leave it on "Block" (the initial state) | Members cannot make new auto-run settings. The screen says an administrator has to allow it |
| Switch to "Allow" | Members can give their own memo categories auto-run settings ([Run memos automatically](../../user/chat/memo-auto-run.md)) |
| Switch back to "Block" | Nothing new can be set up, and nothing starts. The settings that exist are kept and can still be read and removed |

## Before you turn it on

Allow it only when all of these hold.

1. Every Control Plane of the deployment runs the version that has memo auto-run. An older Control Plane would send, delete or rename the memos of an auto-run category from inside a workspace, without the approval that category requires
2. The Console is updated to the same version
3. The deployment's schedule check is on. Auto-run starts on the same check as schedules, so where schedules are turned off, nothing starts, "Run now" included. Ask the deployment administrator

Workspaces need the updated Agent as well. A workspace picks it up when it is restarted.

- Until a member's workspace is restarted, no run starts by itself there. The category shows a notice, and "Run now" still works
- Members restart their own workspace from the Console. Ask the members who use auto-run to do so

## Switch it

1. Open **Operations** → "**Memo auto-run**" in Tenant settings
2. Press "**Allow**" or "**Block**". It is saved when you press it

Each switch is recorded in the audit log as `tenant.memo_queues`, with `enabled=true` or `enabled=false` ([Read the audit log](../audit.md)).
Only tenant administrators and the deployment administrator can switch it.

## What happens while it is blocked

- A member cannot make new auto-run settings, and cannot change existing ones. The screen says "This organization does not allow changing auto-run settings. You can still remove them."
- Nothing starts: no run by itself, and "Run now" is refused with "This organization has not allowed auto-run settings yet. Ask an organization administrator."
- A run that was already started is not stopped by the switch
- The existing settings and memos are kept. Switching back to "Allow" brings them back into use

## What auto-run uses

- Each run is a session in the member's own workspace, and it counts against the member's session limit ([Set session limits](../resources/session-limits.md)). A run that would go over the limit is not sent, and its memos wait to run again
- The spend limit of a run is set by the member in the category's settings. 0 means no limit
- The limits that auto-run itself has are fixed, and are listed in [Limits and defaults](../../ref/limits.md)
- A memo a session adds to an auto-run category always waits for the member's approval. An administrator cannot approve on a member's behalf

## Related

- [Run memos automatically](../../user/chat/memo-auto-run.md): what the member sees
- [Set session limits](../resources/session-limits.md)
- [Read the audit log](../audit.md)
