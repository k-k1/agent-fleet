# 01. First day — from login to your first session

English | [日本語](01-first-day.ja.md)

> Audience: members opening the Console for the first time. We walk in order through logging
> in, the welcome card (2 steps + picking a goal), cloning your first repository, and
> whether it's OK to stop the workspace at the end of the day.

## Log in

Open the Console URL in your browser and you'll first be asked to **sign in**. Which button
you see depends on what your company set up — Google, Microsoft, or another sign-in provider;
if there are several, use your company account with whichever one your administrator told you
to use (only permitted accounts can get in).

Once you're logged in, a **tenant selector** appears at the very top of the screen. A tenant
is a group such as a department, and your workspaces are separated per tenant. In most cases
there is only one, so you can just proceed. If you belong to several, pick the tenant you
want to work in (see the [README](README.md) for the names of the screen areas).

## The welcome card — 2 steps + picking a goal

The first time, a card titled **"Welcome to Agent Fleet"** appears in the main area
("Two steps first, then just pick your goal"). It is a **checklist**, and completed items
are checked off automatically. Only the top two items are required for everyone.

1. **Start workspace** — the "Start" button brings up your own private work environment. While it's starting you'll see "Starting…", and when it finishes it shows "Running" and the item is checked off. Nothing that follows (including connecting) works until you do this.
2. **Connect an agent** — from "Connect", sign in to Claude, Codex, or opencode. Connecting at least one checks the item off (see [06 Agents](06-agents.md) for connection details).

Next comes a two-way choice: **"Where do you want to start?"** (you can use both later).

- **Ask AI a question or for a translation** — "Start chatting" opens a chat with the
  assistant right away. No git and no terminal needed ([07 Chat and memos](07-chat-memo.md)).
- **Develop in a repository** — "Go to dev setup" expands the remaining steps.
  1. **Connect a git provider** (optional) — sign in to GitHub / Bitbucket. Required if you'll clone or push private repositories ([04 Repositories and git](04-git.md)).
  2. **Clone a repository and start a session** — from **"Start"** on the workspace action bar you can clone and launch in one go ([02 Sessions](02-sessions.md)).

The card disappears once you create your first session or start your first chat.

### It's fine to close the card

Pressing **"Later"** at the bottom right dismisses the card. You can reopen the same
checklist at any time from **"Getting-started guide"** in the account menu at the top right.
The regular guide for looking up operations opens from **"User guide"** in the same menu.
Each item maps to a regular operation as follows.

| Welcome card item | Same operation in the regular UI |
|--------------------------|--------------------|
| Start workspace | Start / Stop workspace button on the workspace action bar |
| Connect an agent | ⚙ Settings → "Agents" tab |
| Start chatting | "＋ (New chat)" in the left pane's Assistants section |
| Connect a git provider | ⚙ Settings → "Git hosting" tab |
| Clone a repository and start a session | "Start" on the workspace action bar ("Clone" in the left pane's "Repositories" section can also clone, clone-only) |

## Clone your first repository

To put an agent to work, first bring the repository you'll work on into the workspace.
Open **"Clone"** from **Repositories** in the left pane.

- If you have a connected GitHub / Bitbucket, **"Pick from connections"** (the default) lets you choose the repository and branch from a list.
- For anything not in the list, or not connected, use **"Enter URL"** and paste the clone URL.
- Private repositories require the "Connect a git provider" step from the development track.

Press **"Clone"** and the fetch starts; when it finishes, the repository appears in the
**Repositories** list. From **"Launch"** on that row you can start your first session right
away. Detailed steps and common pitfalls are collected in
[04 Repositories and git](04-git.md).

## At the end of the day — is it OK to stop the workspace?

**Yes, stop it if you like.** Even if you stop the workspace from the workspace action bar,
the following remain:

- The repositories you cloned, and the changes inside them (including uncommitted work).
- The list of sessions you were running. They remain as **stopped**, and the next time you start the workspace you can resume them with a click (for the cases that can't be resumed, see [02 Sessions](02-sessions.md)).
- Your git and agent **connections (logins)**. They persist across workspace stop / start.

Even while the workspace is stopped, **the session list is still visible** in the left pane
(you can't operate on their contents). So glancing at "which sessions did I have?" from your
phone during your commute works even with the workspace stopped.

Don't worry if you forget to stop it. **It stops automatically after a period of
inactivity** (idle auto-stop). It saves resources, so if stopping it explicitly feels like a
chore, it's fine to just leave it.

However, **uncommitted changes exist only inside the workspace**. For long-running work,
committing and pushing frequently is the safe habit ([04](04-git.md)).

### "A new version is available" and the **Restart needed** badge

When the Console is updated, a toast offers an **Update** button. That button only reloads
the browser — **it does not stop your running sessions**.

If the update also moved the backend, the workspace you have running is still on the old
one, and a **Restart needed** badge appears next to the power button in the workspace action
bar. Clicking it explains the cost and offers **Restart now** (a stop→start). Unlike the
reload, this **does stop running sessions** — they become *stopped* and are resumable — while
repositories and files are left untouched. There is no hurry: pick a moment that suits you.
The badge disappears on its own once the workspace is back on the current version.

### End-of-first-day checklist

- Did you commit and push the changes that matter? (Only what you pushed survives outside the workspace.)
- Sessions you want to keep running can simply stay as they are. If you've reached a good stopping point, stopping them via "Stop (resumable later)" in the ⋯ menu means a single click resumes them next time ([02](02-sessions.md)).
- The workspace can be stopped by you, or left alone for idle auto-stop — either is fine.

The things that tend to trip people up on day one (claude showing a login screen, a session
that won't resume, a failed clone, and so on) are collected in
[09 Troubleshooting](09-troubleshooting.md).

---

For those who want to know how it works: [dev/01 Overall architecture](../../dev/01-architecture.md)
