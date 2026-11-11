---
audience: "Tenant administrators who want members' workspaces to start when they open the Console"
---

# Start the workspace at sign-in

English | [日本語](start-at-signin.ja.md)

Have a stopped workspace start by itself when a member opens the Console and starts using it.
Nobody has to press Start first thing in the morning and wait.

## How it works

| What you do | What happens |
|---|---|
| Ask the deployment administrator to turn on the tenant's **Start at sign-in** | When a member opens the Console and uses it, their stopped workspace starts |
| Tell members they can turn it off for themselves | Members who prefer to start it themselves switch it off in Settings |

Every start uses resources, so it is off by default.

## Have it turned on

The switch is in the tenant limits, and only the deployment administrator can change it. It does not appear on your screen, so ask them.

- The setting is **Start at sign-in** ("Start a member’s stopped workspace when they sign in")
- Once on, it works from the next time a member opens the Console
- On a deployment where automatic starts are turned off altogether, nothing starts even when it is on

## When it starts and when it does not

It starts when a member whose workspace is stopped opens the Console in a visible tab and uses it.

| Starts | Does not start |
|---|---|
| Right after arriving at the Console by signing in or typing the address | A reload |
| In a visible tab, after a key press, click, touch or scroll in the last minute | A tab nobody is using, or a tab in the background |
| | A workspace that stopped while the tab was open (idle auto-stop, the member's own stop, a force-stop) |
| | When the workspace's state cannot be read |
| | When the tenant's max workspaces is reached |

- One page asks only once. After a start, other tabs in the same browser and reloads do not ask again for 30 minutes
- A refusal (for example while the switch was still off) does not start the 30-minute wait, so it works right after it is turned on
- It is the same start as creating a session, so [the tenant's max workspaces](session-limits.md#check-the-tenant-wide-limits) applies

While it starts, the member's Home shows **Starting your workspace… (you can keep using Home)**.

### When the limit is reached

When the tenant's max workspaces is reached, nothing starts, and the member's Home shows:

> Your workspace was not started automatically: your organization’s workspace limit is reached. Start it yourself once another one stops.

If this happens all the time, ask the deployment administrator to raise max workspaces.

## Let members turn it off for themselves

When the tenant has it on, members see a **Start at sign-in** row under **Settings → Toolchains**.

- Turning off "Start my workspace automatically when I sign in" stops it for that member only
- It is on by default, and can be changed while the workspace is stopped
- When the tenant has it off, the row does not appear

## Related

- [Stop workspaces left idle](idle-stop.md)
- [Set session limits](session-limits.md)
- [Stop the workspace at the end of the day](../../user/start/end-of-day.md)
