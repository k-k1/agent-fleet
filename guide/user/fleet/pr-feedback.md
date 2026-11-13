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

Events it can watch include checks finishing, a merge conflict, the pull request becoming ready to merge, being merged or closed, a reviewer asking for changes, and new unresolved review threads. If the pull request itself is deleted (or its repository is, or you lose access to it), the watch ends with a notice saying so instead of waiting until it expires; a passing GitHub error or rate limit never ends a watch.

- **GitHub and Bitbucket.** The pull request is read with your own connection, so an agent can watch only what your connection can read
- **When it is told** is the same as PR feedback. A condition that already holds when the agent asks is told at once
- **No double notice.** With PR feedback on, a session that watches its own pull request for failed checks, a change request or new unresolved threads hears that from its watch only, and PR feedback does not repeat it after the watch ends. What it does not watch still comes from PR feedback
- In the session it arrives as a line starting with `[agent-fleet:notice kind=watch`. Text from the pull request (check names, reviewer names) is quoted and marked as data, not instructions

### CI runs: GitHub Actions and Bitbucket Pipelines

A workflow run that is not tied to a pull request (a nightly or release workflow, or one the agent started by hand) can be watched too. The agent passes a run or a workflow instead of a pull request, with the types `ci.run.completed` (every finished run) or `ci.run.failed` (only failures). The same two types work on GitHub and on Bitbucket, so one subscription fits both. The older names `github.workflow_run.completed` and `github.workflow_run.failed` still work and mean the same; a run is told once even if both spellings are asked for. Pull request types are refused for these.

GitHub Actions:

| `ref` | Fires when |
|---|---|
| a run URL (`https://github.com/o/r/actions/runs/123`, optionally with `/attempts/2`) or `o/r:run/123` | that run completes. A run that has already completed is told at once. While the watch is still live (the agent asked to keep it until its deadline), a re-run of the run is told again as a new attempt; a watch that names an attempt reports only that attempt, and the default watch ends at its first notice |
| `o/r:cli-pin-bump.yml`, or with a branch `o/r:cli-pin-bump.yml@main` | the next run of that workflow completes. A run that started after the watch was made counts, and so does one that started about 10 minutes before it (the window is rounded down to the minute, so up to a minute longer) (a run chained from the one you just waited for starts a few seconds before your notice arrives); one inside that window that has already finished is reported at once, and the same watch never repeats a completion (if several runs finished by the first check, the newest is reported). A new watch made inside the window reports the same run again. A run that started before the window never counts. Without a branch, runs on every branch and event count; add `@main` to narrow it. A branch can be given only in the `o/r:file.yml@branch` form, not with the workflow URL. A re-run of a run that existed before the watch is not reported; watch that run directly. To follow a run you started a moment ago, watch that run instead. If what you wait for is a pull request the run opens, watch that pull request instead of the workflow |

- The workflow is read with your own GitHub connection. A repository or run your connection cannot read is refused when the agent asks, and a run that is deleted (or a repository you lose access to) later ends the watch with a notice saying so, instead of waiting for it forever
- A workflow that starts many runs between two checks (up to 100) is read in full, so a run that finished behind newer ones still counts; beyond that the oldest of the burst waits for the next check
- What counts as "after" is decided from GitHub's own run numbers and timestamps, not this workspace's clock
- The notice says the run number, the attempt and the result. The workflow's name and the names and links of failed jobs are quoted data, as for check names
- Reads are conditional (an unchanged run costs no request quota), quick while the run is queued or running, and count in the same limits as pull request watches

Bitbucket Pipelines works the same way, with `source` set to `bitbucket` (a `bitbucket.org` run URL needs no source). The `ref` shapes follow the Actions ones:

| `ref` | Fires when |
|---|---|
| a run URL (`https://bitbucket.org/w/r/pipelines/results/123`) or `w/r:run/123` | that run completes. A run that has already completed is told at once |
| `w/r:pipelines`, with a custom pipeline `w/r:pipelines/nightly`, with a branch `w/r:pipelines@main` or both `w/r:pipelines/nightly@main` | the next run (of that custom pipeline, on that branch) completes. A run that started after the watch was made, or about 10 minutes before it (rounded down to the minute, so up to a minute longer), counts (same rule as for GitHub, including the one-notice and repeat behaviour) |

- A stopped (cancelled) or expired run counts as failed, as a cancelled Actions run does. The notice says the run number and the result; the branch and the names of failed steps are quoted data
- Bitbucket allows few requests an hour, so a workspace can hold at most 5 Pipelines watches, and a run is checked about once a minute while it runs (every 5 minutes while waiting for the next one). Your Bitbucket connection needs permission to read Pipelines; if it lacks it, the agent is told to reconnect
- A new run is recognised from Bitbucket's own build numbers and timestamps, and only the repository's latest 250 runs are searched for the branch; a quiet branch among very busy ones can be missed

### GitHub releases and tags

A session that waits for an upstream release (a fixed dependency, a new CLI version) can watch the repository instead of checking back by hand. The types are `github.release.published` (a new release) and `github.tag.created` (a new tag, for projects that tag without making releases); `github.release.*` and `github.tag.*` work too. Pull request and CI types are refused for these.

| `ref` | Fires when |
|---|---|
| `o/r:releases`, or the URL `https://github.com/o/r/releases` | a release is published. Drafts never count (a draft that is published later does), and pre-releases are skipped |
| `o/r:releases+pre` | the same, and pre-releases count too |
| `o/r:releases@v2.*` | the same, only for a release whose tag matches the pattern (add `+pre` before the `@` for pre-releases too) |
| `o/r:tags`, `o/r:tags@v2.*` | a tag is created (matching the pattern, if given). Tags have no pre-release state, so `+pre` is refused |

- Only a release published, or a tag created, **after the watch was made** counts; one that already exists never fires. The line is drawn from GitHub's own answer when the watch is made, not this workspace's clock
- The pattern is a simple glob on the tag name: letters, digits and `. _ - / + `, with `*` for any run of characters and `?` for one. It is at most 64 characters and anything else (spaces, brackets, non-ASCII) is refused when the agent asks. A tag name outside that set never matches a watch
- GitHub does not list tags in time order, so a tag counts as new when its name was not there when the watch was made and has not been told. A reordered list is not news. A tag that is deleted and then created again with the same name counts as created again once it has been seen missing in two checks, and is told again; the same tag is never told twice without that. A release that was told is not told again even if a newer one is later turned back into a draft
- A tags watch needs the whole list, so a repository with more than 300 tags is refused when the agent asks (watch its releases instead); if a tags watch later outgrows what it can track (2000 names), it ends with a notice saying so rather than guessing. Several tags created between two checks give one notice for the greatest name, and the others are listed as data
- The notice says the repository and the tag. A release's name and notes are quoted data. The version is the release id (for a release) or the tag name
- A repository your GitHub connection cannot read (a private one without access) is refused when the agent asks. A repository that is deleted or that you lose access to later ends the watch with a notice saying so
- Reads use your own GitHub connection, are conditional (an unchanged list costs no request quota), about every 5 minutes, and count in the same limits as pull request watches. A public repository needs no extra permission

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

### Waiting for another session

An agent can wait for another session of the workspace instead of setting a timer as a backstop (`watch` with `source: fleet`). `ref` is the session's name, taken from `list_child_sessions` (its own children) or `list_peer_sessions` (any other session, while peer messaging is on). A session cannot watch itself, and a name it could not see in those lists is refused.

| Type | Fires when |
|---|---|
| `fleet.session.idle` | the session ended a turn and is waiting for input. Every turn end is one notice, and a restart of the workspace never repeats one |
| `fleet.session.stopped` | the session is stopped (at once if it already is; stopped again later is told again) |
| `fleet.quota.reopened` | the usage limit the session was held at has lifted |

- **Idle means what the session list shows.** A session waiting on a question, a plan or a permission is not idle
- **The turn that ended before the watch does not count.** A session that is idle when the watch is made is told on its next turn end, so a parent can message a child and watch it in either order without being told about the turn before the message. A child that will never run another turn is not told; read its state with `list_child_sessions`
- **Best effort for agy, copilot, cursor and kiro.** These have no end-of-turn hook, so the end is read from a poll of the agent's own state. Just after a new message the state can still show the previous turn's idle; the workspace ignores an idle within 10 seconds of the turn starting, but one that lasts longer can be taken for the new turn's end, which gives an early notice or misses the real one. A notice can also come a poll later than the end. The agent should check the session's state after a notice before acting on it.
- **A report and the notice are separate.** `fleet.session.idle` fires whether or not the child also sent its report back, because the case it exists for is a child that ended its turn without one. A parent that already has the report ends the watch with `unwatch`
- A deleted session ends the watch with a notice. The notice names the session and its state only; nothing the session wrote is in it

### Seeing and ending a session's watches

A session waiting on a watch looks idle, so the session's chat view lists its live watches in a panel just above the message box, next to the one for background commands. Each row shows what it watches (a pull request, a timer, a CI run, or an extension's own source) with the events in words ("CI finished", "merged"). A timer shows the time left. A warning mark with the error means the source is not answering right now; the watch stays and is read again. A row links out only when its address is on github.com or bitbucket.org; everything else is plain text.

- **End watch** asks first, then ends that watch, the same as the agent's `unwatch`. If the watch had already ended, the row simply goes. Someone who can only view the session sees the rows without the button
- A watch a child asked to send to its parent (`notify: parent`) shows on the parent's chat view, marked with the child's name
- The panel hides when the session has no watches

### Limits on watches

| Rule | Value |
|---|---|
| When a watch ends | After its first notice (unless the agent asks to keep it), and in any case after 7 days (30 at most) or when the session holding it is archived |
| Watches per session | 20. A watch for the parent counts against the parent's 20, and against the child's 20 together with the child's own watches (`list_watches` names the child each came from) |
| Watches per workspace | 200 |
| Timers with `wake` | 250 outstanding wake bookings per member, shared by all that member's workspaces |
| Bitbucket watches | 10 per workspace, read about every 5 minutes (to stay within Bitbucket's hourly request limit) |
| Bitbucket Pipelines watches | 5 per workspace, counted separately from the pull request watches above |

- On Bitbucket, new unresolved review threads cannot be watched: Bitbucket does not report which threads are resolved
- On Bitbucket, a pull request counts as ready to merge once someone approved it and nobody asks for changes
- Watches survive Agent restarts

### Faster notices with webhooks

A watch is always read by polling, with your own connection. An administrator can add webhooks so the Control Plane
tells your workspace to read a pull request right away instead of waiting for the next scheduled read. A webhook carries no data to the workspace,
only "this pull request changed", and it never starts a stopped workspace (a stopped workspace catches up when it starts).

- **Public repositories**: when the deployment's GitHub App has its webhook set up (a setting made by whoever runs the deployment, see [Settings reference](../../ref/settings.md)), deliveries for public repositories speed up watches.
- **Private repositories**: the deployment's shared App is ignored for them, because it serves every tenant. A private repository needs a webhook that belongs to your tenant (a repository hook, or your tenant's own GitHub App; Bitbucket always uses one). Without it the watch stays on polling, a few minutes at most behind. Create it from the workspace with [`af-watch-hook`](../../ref/cli/af-watch-hook.md).

#### Add a hook for a private repository

```sh
af-watch-hook add github owner/name      # or: af-watch-hook add bitbucket workspace/name
af-watch-hook list
af-watch-hook rm <id>
```

`add` makes the hook at the Control Plane and installs it at the repository with your own connection (Settings > Connections), then pull request, review, comment and check events poke your workspace's watches. The hook's secret goes straight to GitHub or Bitbucket; it is never shown or saved. `rm` deletes the hook at the repository and at Agent Fleet.

- **You need admin rights on the repository.** GitHub: a classic token with the `repo` (or `admin:repo_hook`) scope, or a fine-grained token with Webhooks: Read and write. Bitbucket: the Webhooks scope and repository admin. Without them `add` says so and leaves nothing behind. Ask a repository admin, or keep polling
- **The deployment must have a public URL**, or a provider could not reach the hook
- **Running `add` again** for a repository that has a hook changes nothing. To start over, `rm` it and `add` it again
- **Your own GitHub App**: a webhook secret pasted into an App's settings must be shown to you once, which `af-watch-hook` does not do (it never prints the secret). Until it can, use `af-watch-hook add`, which needs no App
- GitHub Enterprise Server and Bitbucket Data Center are not supported

## Related

- [Connect a Git service](../code/connect-git.md)
- [Use the GitHub CLI](../integrations/github-cli.md)
- [Use the Bitbucket CLI](../integrations/bitbucket-cli.md)
- [Start child sessions from a session](child-sessions.md)
