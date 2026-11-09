---
audience: "anyone who has sessions open pull requests"
source_of_truth: "The Console's Settings > Agents > Tell sessions about their PR's feedback, and the af tool watch"
---

# Tell a session about its pull request's review

English | [日本語](pr-feedback.ja.md)

When a pull request's CI fails or a review asks for changes, Agent Fleet tells the session that opened it and prompts it to fix and push.
The session no longer sits idle until you notice.

## How it works

| What you do | What the agent does |
|---|---|
| Turn on "Tell sessions about their PR's feedback" in Settings | It receives the PR's CI failures and review comments, fixes them and pushes |
| Decide on merge conflicts yourself, from the notification | — |
| (nothing to set) | It watches a pull request it cares about and is told when it changes |

## Turn it on

Under "Session" in **⚙ Settings → Agents**, turn on **Tell sessions about their PR's feedback**. The screen is shown in [Start child sessions from a session](child-sessions.md#turn-it-on).

It spends turns and starts stopped sessions without your asking, so it is off by default.

## What it tells

| Event | How it is told |
|---|---|
| CI failed on the latest commit | Once all of that commit's checks have finished, with the name of every failed check. At most once per pushed commit |
| A review asked for changes, or unresolved review threads increased | At most 3 times per PR |
| A merge conflict | Not told to the session; it goes to **Pull requests** in the notification center. A conflict usually needs a decision about the base before any edit |
| Feedback for an archived session | It goes to the notification center |

- In the session it arrives as a line starting with `[agent-fleet:notice kind=pr-feedback reply=none]`. The conversation marks it as a notice from Agent Fleet, not an instruction from you
- What was told is remembered across Agent restarts, so nothing is told twice

## Which session is told

- The pull request of the branch each session is on
- Up to 5 PR links a session holds as "started from", "created" or "working on". Both GitHub and Bitbucket
- Not a session that holds the PR only to review or watch it
- When several sessions hold the same PR, the one with the strongest link is told: started from, then created, then working on, and the newer link on a tie

## When it is told

- Only while the session is idle and can take input
- A session that is working, or waiting on your answer (a question, a plan, a permission), is told once it is free
- **A stopped session is started, then told.** That includes sessions you stopped. They are started one at a time

## A session watches a pull request itself

Instead of polling a pull request, an agent can ask to be told when it changes. There is nothing to set; the agent decides when to use it.

| af tool | What it does |
|---|---|
| `watch` | Watches a pull request (`owner/repo#45`, `#45` in its own repository, or the GitHub URL) for the events it names |
| `list_watches` | Lists its watches |
| `unwatch` | Ends one watch |

Events it can watch include checks finishing, a merge conflict, the pull request becoming ready to merge, being merged or closed, a reviewer asking for changes, and new unresolved review threads.

- **GitHub and Bitbucket.** The pull request is read with your own connection, so an agent can watch only what your connection can read
- **When it is told** is the same as PR feedback. A condition that already holds when the agent asks is told at once
- **No double notice.** With PR feedback on, a session that watches its own pull request for failed checks, a change request or new unresolved threads hears that from its watch only, and PR feedback does not repeat it after the watch ends. What it does not watch still comes from PR feedback
- In the session it arrives as a line starting with `[agent-fleet:notice kind=watch`. Text from the pull request (check names, reviewer names) is quoted and marked as data, not instructions

### Send the notices to the parent

A child started by another session can have the notices go to its parent (`notify: parent`), so the session handing out the work hears about its children's pull requests.

- The watch then belongs to the parent. The parent lists and ends it, and it lasts until the parent is archived, not the child
- The notice says which child it is for

### Timers

An agent can also set a timer (`watch` with `source: time`): "tell me in 20 minutes" or "at 15:00 JST". Times are kept in UTC; the agent gives either a full time with an offset (`2026-11-09T15:00:00+09:00`) or a wait (`in 20m`, `3d`), at most 30 days ahead.

- **While the workspace is running** the timer fires from the workspace, and the session is told the same way as for a pull request
- **`wake`** also asks the Control Plane to start the workspace if it is stopped at that time, so the agent can be told then. It does nothing else: no prompt is sent, and the workspace may go back to stopped afterwards as usual. It needs the Control Plane's scheduler (the scheduled-execution feature) to be running; where it is not, the agent's request is refused rather than accepted and never fired
- **Without `wake`**, a timer that falls due while the workspace is stopped is told at the next start, marked late. It is dropped if its watch has expired by then (a timer lives until a day after it is due)
- The booking made for `wake` does not appear under Scheduled execution and cannot be edited there. It is removed when the timer fires, when the agent ends the watch, or when the session holding it ends
- `list_watches` shows each timer's due time and whether it has a wake

### Limits on watches

| Rule | Value |
|---|---|
| When a watch ends | After its first notice (unless the agent asks to keep it), and in any case after 7 days (30 at most) or when the session holding it is archived |
| Watches per session | 20. A watch for the parent counts against the parent's 20, and against the child's 20 together with the child's own watches (`list_watches` names the child each came from) |
| Watches per workspace | 200 |
| Timers with `wake` | 250 outstanding wake bookings per member, shared by all that member's workspaces |
| Bitbucket watches | 10 per workspace, read about every 5 minutes (to stay within Bitbucket's hourly request limit) |

- On Bitbucket, new unresolved review threads cannot be watched: Bitbucket does not report which threads are resolved
- On Bitbucket, a pull request counts as ready to merge once someone approved it and nobody asks for changes
- Watches survive Agent restarts

## Related

- [Connect a Git service](../code/connect-git.md)
- [Use the GitHub CLI](../integrations/github-cli.md)
- [Use the Bitbucket CLI](../integrations/bitbucket-cli.md)
- [Start child sessions from a session](child-sessions.md)
