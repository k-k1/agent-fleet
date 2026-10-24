---
audience: "someone opening the Console for the first time"
---

# 01. First day — from login to your first session

English | [日本語](01-first-day.ja.md)

## Log in

Open the Console URL in your browser and you'll first be asked to **sign in**. Which button
you see depends on what your company set up: Google, Microsoft, or another sign-in provider.
If there are several, use your company account with the one your administrator told you
to use (only permitted accounts can get in).

A tenant is a group such as a department, and your workspaces are separated per tenant. In
most cases there is only one, so you can just proceed. If you belong to several, a **Tenant**
selector appears in the account menu at the top right; pick the tenant you want to work in there (see the [README](README.md) for the names of the screen areas).

## Home and the welcome card — 2 steps + picking a goal

After you sign in, the main area shows **Home**: what is waiting for you and where you left
off. The first time, a card titled **"Welcome to Agent Fleet"** sits at the top of Home
("Two steps first, then just pick your goal"). It is a checklist, and completed items
are checked off automatically. Only the top two items are required for everyone.

1. **Start workspace**: the "Start" button brings up your own private work environment. While it's starting you'll see "Starting…", and when it finishes it shows "Running" and the item is checked off. Nothing that follows (including connecting) works until you do this.
2. **Connect an agent**: from "Connect", sign in to the agent you want to use. Connecting at least one checks the item off (see [09 Agents](09-agents.md) for connection details). If your administrator has chosen the team's agent, the step names it ("Your team's default is Claude"). Start with that one; any other agent still completes the step.

Next comes a two-way choice: **"Where do you want to start?"** (you can use both later).

- **Ask AI a question or for a translation**: "Start chatting" opens a chat with the
  assistant right away. You need neither git nor a terminal ([10 Chat and memos](10-chat-memo.md)).
- **Develop in a repository**: "Go to dev setup" expands the remaining steps.
  1. **Connect a git provider** (optional): sign in to GitHub / Bitbucket. It is required if you'll clone or push private repositories ([06 Repositories and git](06-code.md)).
  2. **Clone a repository and start a session**: from **"Start"** on the workspace action bar you can clone and launch in one go ([03 Starting sessions](03-starting-sessions.md)).

The card leaves Home once you create your first session or start your first chat. From then
on Home shows three sections:

- **Waiting for you**: sessions waiting for your answer, a permission or a plan approval.
- **Continue**: the sessions you can pick up again, most recent first.
- **Start something**: "Get started" (clone and launch) and "Start chatting".

Home is what an empty screen shows, so closing every pane ("Close all") brings you back to it.
To open it from anywhere, use **Home** on the workspace action bar (on a phone it may be inside the bar's **⋯**), or the leader key `g h`.

### It's fine to close the card

Pressing **"Later"** at the bottom right dismisses the card. This is remembered for your
membership in this tenant, so the card does not come back when you sign in from another device;
in another tenant you belong to, it is shown again. You can reopen the same
checklist at any time from **"Getting-started guide"** in the account menu at the top right.
The regular guide for looking up operations opens from **"User guide"** in the same menu.

| Welcome card item | Same operation in the regular UI |
|--------------------------|--------------------|
| Start workspace | Start / Stop workspace button on the workspace action bar |
| Connect an agent | ⚙ Settings → "Agents" tab |
| Start chatting | "＋ (New chat)" in the left pane's Assistants section |
| Connect a git provider | ⚙ Settings → "Git hosting" tab |
| Clone a repository and start a session | "Start" on the workspace action bar ("Add" in the left pane's "Repositories" section can also bring one in, without launching) |

## Clone your first repository

To put an agent to work, first bring the repository you'll work on into the workspace.
Open **"Add"** from **Repositories** in the left pane, leave **Kind** on **Git**, pick the
repository (or paste its clone URL) and press **"Clone"**. When it finishes, the repository
appears in the **Repositories** list, and **"Launch"** on that row starts your first session. Private
repositories need the "Connect a git provider" step above. The options (branch, folder name),
the pitfalls, and the built-in git provider are in [06 Repositories and git](06-code.md#clone-a-repository).

Starting something that does not exist anywhere yet? There is nothing to clone: use
**"+ Start" → "Start in a new folder…"** to create an empty folder that is already a git repository
([06](06-code.md#starting-with-nothing-to-import-from-a-new-folder)).

## At the end of the day — is it OK to stop the workspace?

Yes, stop it if you like. Even if you stop the workspace from the workspace action bar,
the following remain:

- The repositories you cloned, and the changes inside them (including uncommitted work).
- The list of sessions you were running. They remain as **stopped**, and the next time you start the workspace you can resume them with a click (for the cases that can't be resumed, see [05 Stopping and tidying up](05-stopping-and-tidying.md)).
- Your git and agent **connections (logins)**. They persist across workspace stop / start.

Even while the workspace is stopped, the session list is still visible in the left pane
(you can't operate on their contents). So glancing at "which sessions did I have?" from your
phone during your commute works even with the workspace stopped.

If you forget to stop it, it stops automatically after a period of
inactivity (idle auto-stop). It saves resources, so if stopping it explicitly feels like a
chore, it's fine to just leave it.

However, **uncommitted changes exist only inside the workspace**. For long-running work,
committing and pushing frequently is the safe habit ([06](06-code.md)).

### "A new version is available" and the **Restart needed** badge

When the Console is updated, a toast offers an **Update** button. That button only reloads
the browser. **It does not stop your running sessions.**

If the update also moved the backend, the workspace you have running is still on the old
one, and a **Restart needed** badge appears next to the power button in the workspace action
bar. Clicking it explains the cost and offers **Restart now** (a stop→start). Unlike the
reload, this **does stop running sessions**: they become *stopped* and are resumable, while
repositories and files are left untouched.
The badge disappears on its own once the workspace is back on the current version.

See the [release history](../../deploy/release/notes/SUMMARY.md) for what each release brought.

### End-of-first-day checklist

- Did you commit and push the changes that matter? (Only what you pushed survives outside the workspace.)
- Sessions you want to keep running can simply stay as they are. If you've reached a good stopping point, stopping them via "Stop (resumable later)" in the ⋯ menu means a single click resumes them next time ([05](05-stopping-and-tidying.md)).
- The workspace can be stopped by you, or left alone for idle auto-stop. Either is fine.

For the things that tend to trip people up on day one (claude showing a login screen, a session
that won't resume, a failed clone, and so on), see
[21 Troubleshooting](21-troubleshooting.md).
