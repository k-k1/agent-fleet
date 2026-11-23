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
| Open the category's "**Auto-run settings**" and press "**Check the file**" | Your workspace reads the file from the repository's default branch on the remote, and the Console lists every setting where the file differs from yours, every difference ticked |
| Untick what you do not want, and approve the rest | Saves the file's values for the ticked settings to your category, and records which commit you approved |

- The settings are yours. Each member who has a category with that name in that repository approves for themself, and an approval changes nobody else's settings
- The file is read from the default branch on the remote, never from your working copy or a working branch, so an edit there is not picked up by itself. That is a guard rail, not a guarantee: a session in your workspace uses the same git sign-in as you. What applies is only what you approve, after the card has shown you every difference and the file's path, branch and commit. The card does not show which remote the file came from: if you doubt it, check the repository's `origin` in your workspace
- Some settings are never read from the file (below). Turning auto-run on is one of them

## Before you start

- The category must belong to a repository. "**Repository settings file**" does not show for a category without one
- Your workspace must be running when you check the file and when you approve: the file is read there each time
- The deployment can switch the feature off. Then "Repository settings file" does not show ([deployment variables](../../ref/settings.md#deployment-variables))
- To approve, a tenant administrator must have allowed memo auto-run ([Allow memo auto-run](../../admin/defaults/memo-auto-run.md)). Until then you can check the file and see the difference, but not approve it

## Write the file

The quickest start is a category you have already saved. In its "**Auto-run settings**", press "**Export as YAML**", then "**Copy**" or "**Download**". The export holds that one category only:

- When the repository has no file yet, commit it as `.agent-fleet/autorun.yaml` through the repository's usual review
- When the file exists, copy only that category's entry under `categories` into it. Committing the export over the file would delete every other category's entry

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
    open_cron: ""
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
- The file may be at most 64 KiB

### Keys

| Key | Setting in "Auto-run settings" |
|---|---|
| `agent_kind`, `model`, `effort` | Agent, Model, Effort |
| `route` | The route the agent's sessions run on. The dialog has no field for it: it comes from the file. It must be one the agent takes |
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
| `instruction_file` | Instruction, read from another file of the repository (below) |

Values must be within the same ranges as in the Console ([the fields](memo-auto-run.md#the-fields)), and the result must be a combination the Console would save. A value or combination the Console would refuse is shown as an error on its line.
Strings are quoted when YAML would read them as something else (`model: "1.5"`).

### When it runs

A time window and a threshold cannot both be set, and the file is laid over each member's own settings, which may use the other one. So write the one you do not use as off:

- A threshold: `open_after` with `open_cron: ""` (as in the example). Without `open_cron: ""`, a member who has a time window gets an error, because the window would stay
- A time window: `open_cron`, `open_minutes` and `tz` with `open_after: 0`

Switching from a window to a threshold also clears the window's length and time zone, which the file does not need to mention. Without a window, `open_minutes` other than `0` or `tz` other than `""` is an error.
The file cannot take a category that has a window or a threshold to neither, which would start a run whenever a memo is ready and there is room: that is a Console-only change (next sections). A category that already has neither can stay so.

### Keep the instruction in its own file

`instruction_file` names a file of the repository whose content becomes the instruction, so a long runbook can be reviewed as an ordinary Markdown file. Spaces and blank lines at its start and end are removed, as in the instruction field.

- The path is relative to the repository's root, with `/` between folders, and is written in its plain form: no `..`, no leading `./`, no trailing `/`
- It is read from the same commit as `autorun.yaml`, never from another branch
- It must be a regular file (not a link, a folder or a submodule) of UTF-8 text with no NUL character, at most 16 KiB, the same limit as the instruction field
- Set `instruction` or `instruction_file`, not both

### What the file cannot set

These stay in the Console, whatever the file says. A file that sets them gets a warning under "**Never applied from the file:**", and the rest of the entry still works.

| In the file | Why it is Console-only |
|---|---|
| `enabled` | Turning auto-run on, or off |
| `accept_own_sessions` | "Accept notes from sessions I started without approval" removes an approval |
| `tidy_destinations` | Which queues a tidy may move memos to, ready to run |
| `worktree: false` | Running in your working copy instead of a worktree |
| `tidy: true` | Turning the tidy run on |
| Values that leave a category with neither a time window nor a threshold, when it has one now | The category would then start a run whenever a memo is ready and there is room |

### Never put a secret in the file

The file holds names, never secrets. A value in a known credential shape (an AWS access key, a GitHub or GitLab token, a Slack, Anthropic, OpenAI or Google API key, a private key block, a JSON web token, a password in a URL, a quoted value after `password:` or `api_key:` and the like) is refused, and so is an instruction file that holds one.
That check is a net, not a scanner. It misses other shapes, and it does not read the value of a Console-only key at all. A file that passes can still hold a secret, and once committed it stays in the repository's history. Read the export and the file before you commit them.

## Approve the difference

1. In the "**Memo queue**" view, open the category's "**Auto-run settings**"
2. Under "**Repository settings file**", press "**Check the file**". It shows the branch and commit it read, and the commit you approved last
3. Under "**Repository settings differ**", each row shows your value, the file's value and the line it is on. **Every row starts ticked**
4. Untick each row you do not want, then press "**Approve N selected**". N counts settings, not rows: "When it runs" can count as several

- A row marked "**Increase**" raises a limit: a higher spend limit or none at all, more sessions at the same time, more follow-ups, more runs per day. Untick it unless you mean it
- "**When it runs**" is one row: the window and the threshold settings are approved together, or not at all
- Approving one setting without another is refused when the result is a combination the Console would not save. An agent with a route only another agent takes, for example: tick the agent and the route together
- The approval reads the file again and applies exactly what you were shown. If the default branch got a new commit (even one that does not touch the file), or your settings or the category's name changed, the approval is refused and the card reads the file again. **The new difference starts with every row ticked again**: untick what you do not want before you approve
- Save or cancel your own edits in the dialog before approving
- If the category was renamed (in another tab, for example), the card says so and the approval waits: close the dialog and open the category's "Auto-run settings" again
- After the approval the dialog closes. Open it again to see the saved settings

When the file as a whole, or this category's entry, has an error, the card says "**The file cannot be applied. Your current settings stay in force.**" and lists each error, with its line when there is one. An error in another category's entry does not stop yours from being compared. Nothing is compared until it is fixed on the default branch.
When the file cannot be read at all, the card shows a reading error in place of the difference (next table).

## See that the file changed

A category whose auto-run settings are saved keeps the result of its last read that got an answer. When that read found a difference, or an error in the file, the category's header in the Memo queue shows a mark, and "Auto-run settings" says when it was read and at which commit. A category without saved settings keeps nothing.

That record is made when you press "Check the file", and, on deployments that turn it on, by a periodic re-read of the categories with "Run by itself" on whose workspace is running. The re-read never starts a workspace and never applies anything.

The mark is about that last read, not about the file now:

- No mark when the last read found no difference, or found no file or no entry for the category
- No mark once you renamed the category or changed its settings since the last read: that record no longer applies
- A read that failed (the remote could not be reached, the file is too large or not UTF-8 text) changes nothing: the earlier record, mark or no mark, stays
- A commit to the file is not seen until the next read: by you, or by the re-read where the deployment has one

Press "Check the file" ("Check again" after the first time) to know for sure.

## When it goes wrong

| What you see | What to do |
|---|---|
| "The owner's workspace must be running." | Start your workspace and check again |
| "The default branch has no .agent-fleet/autorun.yaml." | The file is not on the remote's default branch yet. Merge it there; a working branch is never read |
| "The file has no entry for “…”." | The file has no key with the category's exact name |
| "The settings file could not be read from the repository's default branch. Your current settings stay as they are." in the card | A reading error. For example: the remote cannot be reached, the file is larger than 64 KiB or not UTF-8 text, your workspace has no clone of the repository, or the workspace's Agent is too old to read `instruction_file` (restart the workspace to update it). Fix it and check again |
| An error on `instruction_file` | The commit has no such file, or it is not a regular UTF-8 text file, is larger than 16 KiB, holds a NUL, looks like it holds a credential, or the path is not in its plain form |
| "a queue opens in a window or after a number of messages, not both" | The file sets one of the two while your settings use the other. Write the other as off ([When it runs](#when-it-runs)) |
| "The repository settings file or your settings changed since you looked at the difference. Check again before approving." | The default branch or your settings moved after the check. The card reads the file again: check the ticks and approve what it shows now. If the category was renamed, open its "Auto-run settings" again first |

## Related

- [Run memos automatically](memo-auto-run.md): every auto-run setting and what it does
- [Run work unattended with memo auto-run](../fleet/memo-auto-run-practices.md): keeping the procedure in the repository and the instruction short
- [Settings reference](../../ref/settings.md#deployment-variables): the deployment variables that switch the file and the periodic re-read
