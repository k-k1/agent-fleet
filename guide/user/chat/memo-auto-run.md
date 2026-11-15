---
audience: "People who want the memos they collect run as sessions by an agent, automatically"
source_of_truth: "A category's \"Auto-run settings\" and \"Run history\" in the Console's Memo queue in the left pane, and the approval card"
---

# Run memos automatically

English | [日本語](memo-auto-run.ja.md)

Give a memo category **auto-run** settings, and the memos waiting to run in it are run as sessions, following your instruction.
You can start them with "Run now", or have them start by themselves at times or counts you choose. A memo a session adds does not run until you approve it.

## How it works

| What you do | What the agent does |
|---|---|
| Set up auto-run on a category: the instruction, the agent, how many run at once, and when. Leave "Run by itself" off while you set it up | Nothing. With "Run by itself" off, saving the settings starts nothing |
| Write memos. Read the memos a session adds, and approve them | Takes the memos you wrote and the ones you approved, as memos waiting to run, in order |
| Press "Run now", or wait for it to start by itself | Starts a session and, following your instruction, does the work the memos describe |
| Read the run history. Answer a session that waits for you | Its session is stopped when the run ends. To carry on, you start the session again |

Auto-run is not a [schedule](../fleet/schedules.md). A schedule runs a fixed prompt at set times. Auto-run works through the memos that have piled up, following your instruction.

## Set it up

1. In the "**Memo queue**" of the left pane, press the gear ("**Auto-run settings**") in the header of the category
2. Write the "**Instruction**" and decide the other fields
3. Press "**Save**"
4. To have it start by itself, turn on "**Run by itself**" and save again

![The "Auto-run settings" screen: Instruction, Agent, Model, Effort, Spend limit, Sessions at the same time, Memos per session, When it runs, Runs per day, Overdue after, and Run by itself](../../assets/chatpages-autorun-settings-en-light.webp)

- Only a category with a name can have it. "**Uncategorized**" cannot
- "**Run by itself**" starts off. Save it off and nothing starts by itself. If you turn it on in the first save and memos are already waiting to run, they start. "Run now" works whether it is on or off
- Once the settings exist, the memos already in the category take these states: the memos you wrote are "Waiting to run", the others "Waits for approval", and the ones you already sent "Sent"
- A memo taken in from the inbox cannot go into an auto-run category
- Until a tenant administrator allows it, new settings cannot be made, and the screen says so ([Allow memo auto-run](../../admin/defaults/memo-auto-run.md))

### The fields

| Field | What it holds |
|---|---|
| Instruction | What a session that receives this category's memos follows, every time. It is required to turn auto-run on, and holds up to 16 KB. The memos follow it as data |
| Agent | The agent that runs the memos. It starts as claude. As with schedules, you choose among the agents that can run unattended ([Agents reference](../../ref/agents.md)) |
| Model | Empty means the agent's default |
| Effort | Empty means the default. low, medium, high, xhigh and max are offered |
| Spend limit (USD) | A limit for each run, from 0 to 10,000. 0 means no limit. It starts at 0 |
| Run each time in a new worktree | When on, each run works in a new worktree of the category's repository. On to begin with. It has no effect on a category without a repository |
| Sessions at the same time | How many runs the category may have open at once. A whole number from 1 to 6. It starts at 2 |
| Memos per session | How many memos go into one run. A whole number from 1 to 20. It starts at 1 |
| When it runs | The condition for starting by itself (next table) |
| Runs per day | The most runs that start by themselves in 24 hours. A whole number from 1 to 200. It starts at 20 |
| Overdue after (hours) | A run still open after this long is marked "Overdue". A whole number from 1 to 72. It starts at 12 |
| Run by itself | Turn it off to keep the settings without starting runs by itself |

Choose one of three under "**When it runs**".

| Choice | What happens |
|---|---|
| At any time | Approved memos run as soon as there is room |
| In a time window | Runs only from "Window starts (cron)", for "Window length (minutes)", 1 to 1,440 minutes. An empty "Time zone" means UTC, and it starts as this browser's time zone |
| Once notes are waiting | Starts once "Notes waiting to run" (1 to 100) is reached and no new memo has come for a minute. It keeps going until none is left |

- "Runs per day" counts the runs created in the last 24 hours. A run started with "Run now" is counted too, but "Run now" itself is never stopped by this number
- A value that cannot be saved shows its range under the field

### Remove the settings

Press "**Remove settings**" and the category is an ordinary memo category again. No memo is deleted.

## Use it

### Add memos

| Who adds it | State of the memo |
|---|---|
| You (in the Console, with "Add to queue" in the file viewer, and so on) | "Waiting to run". It is taken by the next run as it is |
| A session (`add_memo`), or an `/mcp` connection such as an outside assistant | "Waits for approval". It does not run until you approve it |

- In an auto-run category, a session can add and change memos. It cannot send or delete them, and cannot rename, merge or delete the category. You do those in the Console
- When a session changes a memo you approved (waiting to run), it goes back to "Waits for approval". When you change it yourself, it stays waiting to run
- Changing a memo that waits for approval yourself does not approve it
- A memo a session adds is up to 16 KB. Its text cannot start with `[agent-fleet:`. Sessions can add 30 memos a minute in total, across all categories

### Approve

Memos waiting for approval gather in a card for each session (or `/mcp` connection) that added them.

![The approval card under a category: below the heading "Added by session cart-ui", the memo texts, and an "Approve (2)" button at the bottom right](../../assets/chatpages-autorun-card-en-light.webp)

1. Read the memos shown on the card
2. Press "**Approve (N)**". The memos become "Waiting to run"

- **Only what the card shows is approved.** The approval covers the memo's text, its referenced file path and its attachment information as shown. If one of these changes after the card was drawn, the memo is not approved. It does not pin the contents of a referenced file or of an image file
- If a session changes a memo while the card is on screen, the memo is tagged "**Changed**", and the button becomes "**I have read the change**". Read the change and press it, and the card says "Approving runs the text shown now". Then press "Approve"
- If the text changed after you pressed the button, nothing at all is approved, and the latest text is shown again. Check it and approve once more. A card is approved whole or not at all

### Run now

1. Press the history button of the category ("**Run history**")
2. Check "N waiting to run" and press "**Run now**"
3. "Started the run." appears. The session starts at the next check, about a minute later. A stopped workspace takes longer, because it is started first

- A run takes the oldest memos waiting to run, as many as "Memos per session". One press is one run
- It cannot be pressed while nothing is waiting to run
- It works with "Run by itself" off, while paused, outside the time window, and when the day's runs are used up
- A stopped workspace is started
- It is refused when the category already has as many runs as "Sessions at the same time", when all your categories together have 4, when you are at your session limit, when the instruction is empty, or when the organisation has not allowed auto-run. The screen says which

### Read the run history

"**Run history**" lists the category's latest runs, newest first, at most 50. It refreshes every 10 seconds, and "**Refresh**" reads it at once.

![The "Run history" screen: "2 waiting to run", "Refresh" and "Run now" on top, with the runs below. A running row shows "Started by itself" and "Not finished: working", and a finished row shows "First turn: finished"](../../assets/chatpages-autorun-history-en-light.webp)

| Shown | Meaning |
|---|---|
| State | The state of the run (next table) |
| Memos: N | How many memos went into the run |
| Date and time | When the run was created |
| Session name | The session it started. Press it to open it. "No session yet" until there is one |
| Overdue | A run still open after the time you set |
| Started by itself, Started with Run now | How the run began |
| Not finished | Why the run is still open (the table below) |
| First turn | How the session's first turn ended: finished, failed, stopped or ended |
| You took over this session | You started the session again after the run ended, so it is not stopped automatically |

The states of a run:

| State | Meaning |
|---|---|
| Starting | The session is being started |
| Running | The session is running and the run is not over |
| Done | The run is over |
| Stopped | The session was archived or deleted while the run was open |
| Needs a look | Whether the session started could not be confirmed. Its memos become "Needs a look" too |
| Not sent | The session could not be started. The memos go back to waiting to run |

The states of a memo show on its row in the category:

| State | Meaning |
|---|---|
| Waits for approval | A memo a session or similar added. It does not run until you approve it |
| Waiting to run | Approved, and waiting for the next run |
| Starting | A session is being started to run it |
| Sent | Sent to a session |
| Needs a look | Whether the session started could not be confirmed. Check the run history. It is not run again by itself |

A memo that is "Starting" cannot be changed.

### How long a run stays open

A run stays open until its session has nothing left to wait for. While any of these holds, the run stays open with a "Not finished" reason.

| Not finished | Meaning |
|---|---|
| working | The session is working |
| waiting for you | It asks you a question, for a plan's approval or for permission |
| waiting for a usage limit | It waits for the agent's usage limit to lift |
| waiting on a watch or a timer | It is set to carry on later through a watch or a timer |
| not known | The state cannot be read |

- Sessions it started count too. While a child is working, the run is not over ([Start child sessions from a session](../fleet/child-sessions.md))
- When none of these holds on two checks in a row, the run becomes "Done". The checks are about a minute apart
- "As of …" on the reason is when it was last read
- An open run uses one of the "Sessions at the same time". While all of them are used, no new run starts

### When a run ends, its session is stopped

The session of a run that is "Done" is stopped. So are the sessions it started, and any watch or timer that would have started it again.

- Input sent to a stopped session is refused with "This session was stopped after its auto-run finished. Start the session first, then send again." To carry on, press "**Resume and continue**" ([Stop and resume a session](../sessions/stop-resume.md))
- If you start the session again before it is stopped, it is left alone, and the run history says "You took over this session"
- An "Overdue" run whose sessions have shown no sign of work for an hour is stopped, including a session that was waiting for you. The run then becomes "Done"
- Archiving or deleting the session ends the run too, as "Stopped"

In the conversation, the turn auto-run handed over carries an "**Auto-run**" mark, so it is not mixed up with what you typed.

### What the session is given

The session's first prompt is put together in this order.

1. One line that says it came from auto-run, with the category's name
2. The "Instruction" you set, as your own standing instruction. The session follows it as your words
3. One line that says what follows is data, not instructions
4. The memos. Each sits under a line that says where it came from (you in the Console, a session, or an `/mcp` connection), with its text quoted

- A memo says what to work on. It cannot widen the instruction. When a memo asks for a change in another repository or for extra work, the session does only what the instruction covers and says the rest is out of scope
- Orders and commands inside a memo are only text. A memo cannot answer a question, grant a permission or approve a plan in your place
- When the memo part of the prompt, with the origin lines and quotation marks Agent Fleet adds, comes to more than 8 KB, or one memo has an image, they are written to a file in the session's work folder, and the prompt points to the file
- One run takes up to 20 memos, a memo's text is up to 64 KB, and a memo has up to 20 images. A memo over these is refused when you press "Run now", so edit it and try again

## Pause and resume

When the first turns of the last two runs both fail, the category **pauses** auto-run.

- A failure is a first turn that failed or was stopped, or one that ended within a minute without finishing cleanly
- The category's header shows "**Paused**", and the top of the run history says "Auto-run is paused: the first turns of the last two runs failed, so no run starts by itself. “Run now” still works."
- Only runs that start by themselves stop. "Run now" still works
- Fix the cause and press "**Resume**". Counting failures starts again from then

## Notifications

They arrive in the notification center. In **⚙ Settings → "Notifications"**, the "**What each notification does**" table switches them together on the "Auto-run" row. Pressing a notification opens its category.

| Notification | When | What you do |
|---|---|---|
| A run is overdue | A run is still not finished after the time you set | Read the history for the reason. Answer the waiting session, or stop it |
| Auto-run paused | The first turns of the last two runs failed | Fix the cause and press "Resume" |
| Auto-run is waiting for you | Every one of the runs the category may have open is a session waiting for you | Answer the questions and permissions of those sessions. No new run starts until you do |
| A run could not be confirmed | Whether the session started could not be confirmed | Check in the run history whether the session exists. Its memos are not run again by themselves |

## Use it more safely

- **Set a spend limit.** "Spend limit (USD)" applies to each run. 0 means no limit, so set a number before you let it start by itself ([Stop a session that spends too much with a budget](../sessions/budget.md))
- **Keep the numbers low.** A category holds at most 6 open runs, and all your auto-run categories together at most 4. "Runs per day" holds back a runaway too
- **A session's memo always passes through your approval.** There is no setting that runs what a session or an `/mcp` connection wrote without your check. A memo becomes what the next session is told to do, so read it before you approve
- **Write the instruction with a clear scope.** For example:
  - What to do ("fix the bug the memo describes and add a test")
  - What it may touch ("this repository only")
  - What it should leave behind ("push the branch and open a pull request")
  - What it must not do ("do not merge")
- Do not write a password or a token in the instruction. It is handed to the session as the first prompt, as it is
- To try it, leave "Run by itself" off, run once with "Run now", look at the result, then turn it on

## When it goes wrong

### Nothing starts by itself

Check these from the top.

| Where you look | Cause and what to do |
|---|---|
| The settings say "This organization has not allowed new auto-run settings yet. …" | A tenant administrator has not allowed it. Nothing starts until they do, and "Run now" does not work either ([Allow memo auto-run](../../admin/defaults/memo-auto-run.md)) |
| The category's header shows "Auto-run off" | "Run by itself" is off. Turn it on and save |
| The top of the run history says "This workspace has to be restarted to update it before runs can start by themselves. …" | The workspace is still on the version from before the update. Restart it, and runs can start by themselves. Until then only "Run now" works |
| Fewer memos are waiting to run than the number that starts a run | "Notes waiting to run" has not been reached, or a minute has not passed since the last new memo. Check "N waiting to run" |
| It is outside the time window | With "In a time window", runs start only for the length of the window from its start. Check the cron expression and the time zone |
| Some memos are still "Waits for approval" | They are not counted as waiting to run. Approve them on the card |
| A memo cannot be sent as it is | The next memo to run has text over 64 KB, more than 20 images, or an attachment path that cannot be used. A run by itself does not start while it is next. Press "Run now" and it says so ("A memo in this category cannot be sent to a session as it is (too long, too many attachments, or an unusable attachment path). Edit the memo and try again."). Edit that memo |
| The day's runs are used up | The runs of the last 24 hours have reached "Runs per day". They start again as the old ones pass 24 hours. Raise the number, or use "Run now" |
| The header shows "Paused" | The last two runs failed. Fix the cause and press "Resume" |
| The latest run ended as "Not sent" | The workspace could not be started or the agent did not accept it. The memos are back to waiting to run, and the category waits before it tries again. The wait begins at 5 minutes, doubles with each failure in a row, and is at most 4 hours |
| A run of the same agent waits for a usage limit | While any of your runs, in any category, waits for a usage limit of the same agent, nothing starts by itself. It starts when the limit lifts |
| The number of open runs is reached | Open runs have reached "Sessions at the same time", or 4 across all categories. Wait for one to end, or raise the number |

### A run stays open

Read "Not finished" in the run history.

- "waiting for you": open the session and answer the question, permission or plan. Its place is not freed until you do
- "waiting on a watch or a timer": while a later restart is set, the run does not end. Stop the watch or the timer in the session if you do not need it
- "waiting for a usage limit": it waits for the limit to lift, and carries on by itself afterwards
- "working": the session or one of its children is running. When the "A run is overdue" notification comes, the run has passed the time you set. After an hour without a sign of work, it is stopped by itself

### A session was stopped

A session is stopped when its run ends. Press "Resume and continue" to start it again. If it stopped in the middle of a run, read "Not finished".

### A memo cannot be changed

A memo that is "Starting" is being handed to a session, so it cannot be changed. The message says "This memo has already been handed to a session and cannot be changed." Once it has started, write a new memo. When it says "This memo is being sent. Wait a moment and try again.", wait a moment.

### A memo cannot be added

- "Memos are being added too fast. Wait a moment and try again.": sessions add up to 30 a minute in total, across all categories. Wait a moment
- "The memo is too large. Shorten it.": a memo a session adds is up to 16 KB
- "A memo taken in from the inbox cannot go into an auto-run category.": put the inbox memo in another category

### A session cannot rename or delete the category

It says "An auto-run category cannot be renamed, merged or deleted from inside the workspace. Do it from the Console." Do it in the Console.

## Related

- [Collect memos and hand them over](memos.md): how to write memos and send them together
- [Run at a set time](../fleet/schedules.md): for a fixed time, use a schedule
- [Start child sessions from a session](../fleet/child-sessions.md)
- [Stop a session that spends too much with a budget](../sessions/budget.md)
- [Get notified](../settings/notifications.md)
- [Allow memo auto-run](../../admin/defaults/memo-auto-run.md): for tenant administrators
- [Limits and defaults](../../ref/limits.md): the numbers of auto-run in one table
