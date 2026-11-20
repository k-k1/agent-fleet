---
audience: "People who want memo auto-run to work through items with nobody watching, and want the habits that make it hold up"
source_of_truth: "The behaviour in [Run memos automatically](../chat/memo-auto-run.md), and a category's \"Auto-run settings\" and \"Run history\" in the Console's Memo queue view"
---

# Run work unattended with memo auto-run

English | [日本語](memo-auto-run-practices.ja.md)

Habits that make [memo auto-run](../chat/memo-auto-run.md) work through a pile of items without you watching each one. They come from running it for a while, and are one way to do it, not the only one.
Every screen and every state is in [Run memos automatically](../chat/memo-auto-run.md). This page says what to write and how to set it up, and links there for the rest.

## How it works

| What you do | What the agent does |
|---|---|
| Keep the detailed procedure in the repository, and write a short standing instruction that points at it | Reads the instruction when the session starts, then follows the procedure it points at |
| Write one memo per work item, with its scope and what is out of scope | Takes the memos waiting to run in order, as many per run as "Memos per session" says |
| Put what is left of a large task on the issue, and cap how many parts it may queue | Does one part, records the rest, and ends |
| Read the run history, approve what sessions add, end a run that hangs | Ends the run when its session has nothing left to wait for, and stops the session |

## Choose auto-run

| For this | Use |
|---|---|
| Items that pile up, each done by following the same instruction | Memo auto-run |
| The same prompt at set times | A [schedule](schedules.md) |
| Splitting one task you are watching into parts | [Child sessions](child-sessions.md) |
| Steering several sessions through chat, with you deciding along the way | The [Fleet Operator](operator.md) |

Auto-run suits work that is small enough to finish in one run and clear enough to need no answer from you on the way. When most items need a person's decision, it only moves the waiting around.

## First setup

1. Write a short instruction (next section) and save the settings with "Run by itself" off. Saving starts nothing
2. Add one or two memos you are sure about
3. Press "Run now" once, and read the session and the result
4. Fix the instruction, then run once more if you changed it much
5. Turn "Run by itself" on

Decide these fields before you do ([the fields](../chat/memo-auto-run.md#the-fields)):

| Field | A starting point |
|---|---|
| Spend limit (USD) | Set a number. 0 means no limit, and nothing else bounds a run that goes wrong ([budget](../sessions/budget.md)) |
| Memos per session | Set 1, so one run is one work item and one pull request to review. At 2 or more, a run takes several memos and the separation this page relies on is lost |
| Sessions at the same time | Start at 1 or 2. A category allows at most 6 open runs, and all your auto-run categories together at most 4 |
| Runs per day | A little above what you expect. It holds back a runaway |
| When it runs | "In a time window" when you want runs only while you are away, or when the host is quiet |
| Accept notes from sessions I started without approval | Leave it off until you have read a few memos sessions added ([approval](../chat/memo-auto-run.md#skip-the-approval-for-your-own-sessions)) |

A memo a session adds waits for your approval, and a session an auto-run started is never one that skips it. Read the memos on the approval card as instructions to the next session, because that is what they become ([Approve](../chat/memo-auto-run.md#approve)).

## Write the standing instruction for nobody watching

**Keep the instruction short, and keep the procedure in the repository.** A skill or an instructions file in the repository is versioned, reviewed and shared. The instruction says which one to follow:

```text
Follow <the skill or file that holds the procedure> for each memo.
Work only in this repository. Work on one memo's item and nothing else.
Open a pull request for the change. <Say here whether the session may merge.>
Before your last answer: stop any watch you started and any child session.
Your last answer is a summary that starts with "Done:", "Partial:",
"Deferred:" or "Too large:", then one line on what a person must decide.
```

A session reads its instructions when it starts. Editing the instruction or the file does not change a run that is already going; the next run reads the new text.

Write the procedure for a reader who is not there:

- **Never end on a question.** "Tell me if I should continue" is never read in time, and the run stays open "waiting for you" and holds its place. Decide, or stop and say why
- **Put what a person must decide on the pull request or the issue**, and say so in the summary. The chat of a stopped session is not where anyone looks
- **Say when to skip an item**: the issue is closed, the problem is already fixed, or it waits on a decision that is not made. Skipping ends the run with a summary that starts with `Deferred:`, and that is a good result
- **Clean up before the last answer.** Stop the watches and timers you started and the child sessions you started. A run ends only when nothing is left to wait for ([How long a run stays open](../chat/memo-auto-run.md#how-long-a-run-stays-open))
- **Say what the session must not do**, as in [Use it more safely](../chat/memo-auto-run.md#use-it-more-safely). Do not put a password or a token in the instruction

## Write the memos

Each memo is one work item. A memo says what to work on and cannot widen the instruction ([What the session is given](../chat/memo-auto-run.md#what-the-session-is-given)).

```text
<Name of the item, or a link to the issue>
Scope: <what to change>
Out of scope: <what to leave alone, and which other item covers it>
Depends on: <item, or "none">
```

- **State scope and out-of-scope.** An item with no edge grows. Naming what is left out is what keeps one run to one pull request
- **Keep a fixed dependency line** such as `Depends on: …` in the text, so a reader and the next session see it. Where order matters, also set the memo to wait for the other one ([Make a memo wait for another](../chat/memo-auto-run.md#make-a-memo-wait-for-another)). The line is for people; the waiting is what the queue obeys
- **Link a work item instead of copying its text.** The session reads the issue itself ([Add a work item](../chat/memo-auto-run.md#add-a-work-item))
- **Keep the memo short.** The item's detail belongs on the issue, where a person can read it too

## Order and concurrency

- **Queue order is priority.** A run takes the oldest memos waiting to run, so put the item you want first at the top
- **Do not run items that touch the same files at once.** Two runs working on the same files conflict when their pull requests meet. Set "Sessions at the same time" to 1 for such a category, or make one memo wait for the other
- **A memo that waits for another takes no place.** The queue holds it, and memos behind it still run. It is released once the memo it waits for ended well and every pull request of that run is merged
- **A session that waits on a watch keeps its run open and holds a place.** A run that waits for a long time on a review or a build uses one of the "Sessions at the same time" the whole while. Where the order is known, prefer making the memo wait for the other one over a session that waits

## Large tasks

A task that does not fit in one run goes through the queue in parts.

- **One part per run.** Say in the memo which part this is
- **Each part leaves the main branch releasable on its own.** New behaviour is unreachable, or sits behind today's default; no half-wired screen is merged. A part that cannot be released alone is too big, so cut it differently
- **Record what is left on the issue before ending.** A checklist on the issue is enough. The queue does not run a started memo again by itself, so the issue is where the remainder survives. The one exception is a run that hands its memo back because the work cannot be done yet ([A run hands its memo back](../chat/memo-auto-run.md#a-run-hands-its-memo-back)); the memo then runs again later as a new run
- **Let the run queue the next part, with a cap in the memo**, for example `Part 2 of at most 5`. The memo the run adds waits for your approval, because a session auto-run started cannot add a memo that skips it. You see each part before it runs, and at the cap the run says so and adds nothing
- **Start the summary with `Partial:`** for a part that left work, `Deferred:` for an item it did not do, and `Too large:` for an item that needs cutting first. They read at a glance in [the run history](../chat/memo-auto-run.md#read-the-run-history)

## Review and merge without a person per pull request

This is one way to run it. It suits changes you are willing to merge on a second model's review; for the rest, keep the merge for yourself.

1. **Review with another model.** A reviewer session on a different model reads the pull request and reports to the session that opened it ([Child sessions](child-sessions.md))
2. **Merge only the commit that was reviewed.** Pin the merge to the head the reviewer saw, for example `gh pr merge --match-head-commit <sha>`. A merge of a changed head is refused
3. **Treat the last review round with care.** A commit after the last review leaves the head unreviewed. In the last round fix only what blocks the merge, and file the minor findings as a follow-up issue
4. **Keep some merges for a person**: changes to CI or release workflows, and migrations that drop or rewrite data. A forward-only migration cannot be rolled back with a release
5. **Move human checks to a release gate or a staging environment.** Label the pull requests that need a live check and list them there, so the check happens once per release and not once per pull request

The instruction states which of these the session may do. A session that must not merge says so in the summary and leaves the pull request.

## Watch it

- Read the [run history](../chat/memo-auto-run.md#read-the-run-history). The summary opens with the words above, so a day of runs reads as a list
- A run that stays open shows why under "Not finished". For one you no longer need, press "End" on its row: the place is freed and the session and the sessions it started are stopped ([End a run yourself](../chat/memo-auto-run.md#end-a-run-yourself))
- Two failed first turns in a row pause the category. Fix the cause and resume ([Pause and resume](../chat/memo-auto-run.md#pause-and-resume))
- Check the "Auto-run" row of the notification settings, so an overdue run and a pause reach you ([Get notified](../settings/notifications.md))

## Pitfalls

- **Editing the instruction does not change a run that is going.** It applies from the next run
- **Issues left open after their pull request merged waste runs.** The next run picks them, finds nothing to do and ends with `Deferred:`. Close the issue, or check before you queue it
- **A waiting run holds a place.** Four runs waiting on watches leave a category with 4 places full and nothing else starts. End them, or make the order explicit with a memo that waits
- **A started memo does not run again by itself**, except when its run handed it back ([A run hands its memo back](../chat/memo-auto-run.md#a-run-hands-its-memo-back)). When a run ended with work left, the remainder is on the issue. Put it back with "Return to queue" only after you have checked the pull request ([Return a run's memos to the queue](../chat/memo-auto-run.md#return-a-runs-memos-to-the-queue))
- **Do not turn on "Run by itself" first.** Try once with "Run now", then switch it on, and set the spend limit before you do

## Related

- [Run memos automatically](../chat/memo-auto-run.md): every screen, state and limit
- [Ways to use the Fleet Operator](operator-patterns.md)
- [Run at a set time](schedules.md)
- [Start child sessions from a session](child-sessions.md)
- [Stop a session that spends too much with a budget](../sessions/budget.md)
