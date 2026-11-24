---
audience: "Members who hand an agent work that has no repository: documents, summaries, research"
source_of_truth: "The Console's Start screen (Start in a space) and the Spaces section of the Sessions view"
---

# Start work in a space

English | [日本語](spaces.ja.md)

A **space** is a folder of its own under `~/spaces` for work that has no repository: drafting a document, summarising a
spreadsheet, researching a question. What the agent writes stays there, apart from your home folder's own files.

## How it works

| What you do | What happens |
|---|---|
| Write what you want done under **Start in a space** and press **Start in a new space** | A new folder is made under `~/spaces` and the agent starts in it with your text as its first prompt |
| Look in the Sessions view | The session is listed under **Spaces**, below the space's name |
| Recreate the workspace | The space is kept: Recreate deletes only `~/repos` ([Recreate the workspace](../workspace/recreate.md#what-stays-and-what-goes)) |

## Start in a new space

1. Press **＋ Start** on the workspace action bar and open **Start in a space** (it is open the first time)
2. Write the task under **What would you like done?**. Paste or drop files, or attach them with **＋**
3. Under **Place**, **New space** is chosen. Give it a name if you like; left empty, it is named by its folder
4. **Details** stays folded with a summary of the agent, the model and what else it will use. Open it to change the
   agent, the model route, the model, the execution method, the reasoning effort, the start mode or the tool permissions.
   There is no branch: a space is not a repository
5. Press **Start in a new space** (Ctrl+Enter in the box does the same, or Enter, as your send key is set). With files
   attached, pick an agent that takes attachments; otherwise remove them first

The session opens in the chat view.

| Agent | How it runs in a space |
|---|---|
| One that has Managed | Managed, whatever your usual default is. You can switch it under **Details** |
| Claude | Terminal (CLI), the only way it runs; its pane opens in the chat view, and the terminal is one click away |

## Start in a space you used before

Work that goes on over several days stays in one space. Open **Place** and pick the space instead of **New space**:

- **Recent** lists the five spaces you started a session in last. A space with no session yet is ranked by when it was
  made
- **All spaces** lists the older ones
- With a space picked, the name box is gone (a space is named when it is made), and the button reads **Start in this
  space**. The agent starts in that folder and sees what earlier sessions left there

Without a pick, each start makes a new space.

## Files you attach

For a session in a space, each attached file is also copied into the space's `attachments` folder, under its own name
(`report.pdf`; a second file of that name becomes `report-2.pdf`). You find the files beside your work, and a later
session in the same space has them too. The agent is still pointed at the upload's own path in the first prompt.
If a file cannot be copied into the space, it is not attached, and the launch says which one.

## What a space keeps

- **The folder name never changes.** The session and its conversation history are tied to the folder, so a space is
  never renamed or moved. The name you gave is shown in the Sessions view and is stored apart from the folder
- **Deleting a session keeps its space**, and the files in it
- **Clean home deletes it**, like the rest of your home

> [!NOTE]
> A session in a space has no agent memory and no skills from a repository, and the box on Start offers no prompt
> templates.

## When it goes wrong

### "This workspace's Agent is too old to start in a space"

The workspace runs an older Agent, which would start the session in home instead. Stop the workspace and start it
again: it starts from the latest image ([Pick up a new image](../workspace/recreate.md#pick-up-a-new-image)). Until then, start in a
repository or use **Other → Launch an agent in home**.

### "Could not check whether this workspace can start in a space"

The workspace did not answer, often because it is still starting. Press **Check again** after a moment.

## Related

- [Start a session](start.md): the Start screen and the Start working dialog
- [Find your shells and SSM sessions](terminals.md): shells opened in a space
