---
audience: "People who want a memo category's auto-run settings kept in its repository, reviewed like code, instead of only in the Console"
source_of_truth: "The \"Repository settings file\" part of a category's \"Auto-run settings\" in the Console's Memo queue view, and .agent-fleet/autorun.yaml on the repository's default branch"
---

# Keep auto-run settings in the repository

English | [日本語](memo-auto-run-repo-file.ja.md)

A repository can carry the [auto-run](memo-auto-run.md) settings of its memo categories in one file, `.agent-fleet/autorun.yaml`.
The file is reviewed and versioned like the rest of the code, and nothing in it is applied until you have seen the difference from your own settings and approved it in the Console.

## How it works

| What you do | What Agent Fleet does |
|---|---|
| Commit `.agent-fleet/autorun.yaml` to the repository's default branch, with an entry per category name | Nothing yet: the file is only read when you ask, or when the deployment re-reads it |
| Open the category's "**Auto-run settings**" and press "**Check the file**" | Your workspace reads the file from the repository's default branch on the remote, and the Console lists every setting where the file differs from yours |
| Tick the settings you agree with and approve them | Saves the file's values for those settings to your category, and records which commit you approved |
| Leave a difference unticked | Your own value stays in force |

- The settings are yours. Each member who has a category with that name in that repository approves for themself, and an approval changes nobody else's settings
- The file is read from the default branch on the remote, never from your working copy or a working branch. A session can change the files it works on, but it cannot change the settings its own runs are started with
- Some settings are never read from the file (below). Turning auto-run on is one of them

## Before you start

- The category must belong to a repository. "**Repository settings file**" does not show for a category without one
- Your workspace must be running when you check the file: the file is read there
- The deployment can switch the feature off. Then "Repository settings file" does not show ([deployment variables](../../ref/settings.md#deployment-variables))

## Write the file

The quickest start is your own settings. In "**Auto-run settings**", press "**Export as YAML**", then "**Copy**" or "**Download**", and commit the result as `.agent-fleet/autorun.yaml` through the repository's usual review.

A file looks like this:

```yaml
version: 1
categories:
  issue-rounds:
    agent_kind: claude
    model: opus
    effort: high
    spend_cap_usd: 30
    max_active: 2
    batch: 1
    open_after: 3
    max_fires_per_day: 20
    max_task_hours: 12
    continuation_max: 4
    instruction_file: docs/runbooks/issue-rounds.md
```

- `version: 1` is required. A file of another version is refused, not guessed at
- Each key under `categories` is a category name, exactly as the Memo queue shows it. Renaming the category in the Console makes it match another entry, or none
- A key the file leaves out is not compared: your value stays
- An unknown key is an error, never silently ignored, so a typo cannot pass for a setting that applies
- The file may be at most 64 KB

### Keys

| Key | Setting in "Auto-run settings" |
|---|---|
| `agent_kind`, `model`, `effort`, `route` | Agent, Model, Effort, and the agent's route |
| `spend_cap_usd` | Spend limit (USD). `0` is no limit |
| `max_active` | Sessions at the same time |
| `batch` | Memos per session |
| `open_cron`, `open_minutes`, `tz` | When it runs: in a time window (start as cron, length in minutes, time zone) |
| `open_after` | When it runs: once notes are waiting (how many) |
| `max_fires_per_day` | Runs per day |
| `max_task_hours` | Overdue after (hours) |
| `worktree` | Run each time in a new worktree. The file may only set `true` |
| `tidy` | Tidy the waiting memos before a run. The file may only set `false` |
| `continuation_max` | Follow-ups a run may add without approval |
| `instruction` | Instruction, written in the file |
| `instruction_file` | Instruction, read from another file of the repository (next section) |

Values must be within the same ranges as in the Console ([the fields](memo-auto-run.md#the-fields)). A value the Console would refuse is shown as an error on its line.
Strings are quoted when YAML would read them as something else (`model: "1.5"`).

### Keep the instruction in its own file

`instruction_file` names a file of the repository whose whole content becomes the instruction, so a long runbook can be reviewed as an ordinary Markdown file.

- The path is relative to the repository's root, with `/` between folders, and no `..`
- It is read from the same commit as `autorun.yaml`, never from another branch
- It must be a regular text file in UTF-8, at most 16 KB, the same limit as the instruction field
- Set `instruction` or `instruction_file`, not both

### What the file cannot set

These stay in the Console, whatever the file says. A file that sets them gets a warning under "**Never applied from the file**", and the rest of the file still works.

| In the file | Why it is Console-only |
|---|---|
| `enabled` | Turning auto-run on, or off |
| `accept_own_sessions` | "Accept notes from sessions I started without approval" removes an approval |
| `tidy_destinations` | Which queues a tidy may move memos to, ready to run |
| `worktree: false` | Running in your working copy instead of a worktree |
| `tidy: true` | Turning the tidy run on |
| Taking away both the time window and the threshold (`open_cron: ""` with `open_after: 0`) | The category would then start as soon as a memo waits |

The file holds names, never secrets. A value that looks like a credential (an API key, a token, a private key, a password in a URL) is refused, and so is an instruction file that holds one.

## Approve the difference

1. In the "**Memo queue**" view, open the category's "**Auto-run settings**"
2. Under "**Repository settings file**", press "**Check the file**". It shows the branch and commit it read, and the commit you approved last
3. Under "**Repository settings differ**", each row shows your value, the file's value and the line it is on
4. Tick the rows to apply and press "**Approve N selected**"

- A row marked "**Increase**" raises a limit: a higher spend limit or none at all, more sessions at the same time, more follow-ups, more runs per day. Read those twice
- "**When it runs**" is one row: the window and the threshold settings are approved together, or not at all
- The approval applies exactly what you were shown. If the file got a new commit, or your settings changed, in the meantime, the approval is refused and the card reads the file again: look at the new difference and approve again
- Save or cancel your own edits in the dialog before approving
- After the approval the dialog closes. Open it again to see the saved settings

When the file has an error, the card says "**The file cannot be applied. Your current settings stay in force.**" and lists each error with its line. Nothing is compared until it is fixed on the default branch.

## See that the file changed

The category keeps the result of the last read. When it found a difference, or an error, the category's header in the Memo queue shows a mark, and "Auto-run settings" says when it was read and at which commit.

That record is made when you press "Check the file", and, on deployments that turn it on, by a periodic re-read of every category with "Run by itself" on whose workspace is running. The re-read never starts a workspace and never applies anything. Without it, a commit to the file shows only after you check again.

## When it goes wrong

| What you see | What to do |
|---|---|
| "The owner's workspace must be running." | Start your workspace and check again |
| "The default branch has no .agent-fleet/autorun.yaml" | The file is not on the remote's default branch yet. Merge it there; a working branch is never read |
| "The file has no entry for …" | The file has no key with the category's exact name |
| An error on `instruction_file` | The file is not in that commit, is not a regular UTF-8 text file, is larger than 16 KB, or looks like it holds a credential |
| "The repository settings file or your settings changed since you looked at the difference." | The file or your settings moved after the check. The card reads the file again: approve what it shows now |

## Related

- [Run memos automatically](memo-auto-run.md): every auto-run setting and what it does
- [Run work unattended with memo auto-run](../fleet/memo-auto-run-practices.md): keeping the procedure in the repository and the instruction short
- [Settings reference](../../ref/settings.md#deployment-variables): the deployment variables that switch the file and the periodic re-read
