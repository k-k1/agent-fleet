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
| `o/r:cli-pin-bump.yml`, or with a branch `o/r:cli-pin-bump.yml@main` | the next run of that workflow completes. Only a run that started after the watch was made counts; a run already running or finished then never does. Without a branch, runs on every branch and event count; add `@main` to narrow it. A branch can be given only in the `o/r:file.yml@branch` form, not with the workflow URL. A re-run of a run that existed before the watch is not reported; watch that run directly. To follow a run you started a moment ago, watch that run instead |

- The workflow is read with your own GitHub connection. A repository or run your connection cannot read is refused when the agent asks, and a run that is deleted (or a repository you lose access to) later ends the watch with a notice saying so, instead of waiting for it forever
- A workflow that starts many runs between two checks (up to 100) is read in full, so a run that finished behind newer ones still counts; beyond that the oldest of the burst waits for the next check
- What counts as "after" is decided from GitHub's own run numbers and timestamps, not this workspace's clock
- The notice says the run number, the attempt and the result. The workflow's name and the names and links of failed jobs are quoted data, as for check names
- Reads are conditional (an unchanged run costs no request quota), quick while the run is queued or running, and count in the same limits as pull request watches

Bitbucket Pipelines works the same way, with `source` set to `bitbucket` (a `bitbucket.org` run URL needs no source). The shapes are the Actions ones:

| `ref` | Fires when |
|---|---|
| a run URL (`https://bitbucket.org/w/r/pipelines/results/123`) or `w/r:run/123` | that run completes. A run that has already completed is told at once |
| `w/r:pipelines`, with a custom pipeline `w/r:pipelines/nightly`, with a branch `w/r:pipelines@main` or both `w/r:pipelines/nightly@main` | the next run (of that custom pipeline, on that branch) completes. Only a run that started after the watch was made counts |

- A stopped (cancelled) or expired run counts as failed, as a cancelled Actions run does. The notice says the run number and the result; the branch and the names of failed steps are quoted data
- Bitbucket allows few requests an hour, so a workspace can hold at most 5 Pipelines watches, and a run is checked about once a minute while it runs (every 5 minutes while waiting for the next one). Your Bitbucket connection needs permission to read Pipelines; if it lacks it, the agent is told to reconnect
- A new run is recognised from Bitbucket's own build numbers and timestamps, and the next 250 runs of the repository are searched for the branch; a branch that is quiet among very busy ones can be missed

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

- **Public repositories**: when the deployment's GitHub App webhook is set up (`AF_GITHUB_WEBHOOK_SECRET`, see [Settings reference](../../ref/settings.md)), deliveries for public repositories speed up watches.
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
