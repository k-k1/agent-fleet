---
audience: "anyone who takes over a step only a person can do, such as a login, in an agent's browser work"
source_of_truth: "the Console's pane that shows an agent's Chromium, and its \"Requested browser action\""
---

# Operate a browser the agent opened

English | [日本語](agent-browser.ja.md)

When an agent runs its own browser (Chromium) inside the workspace and reaches something only a person can do — a
login, a one-time code, a consent box — you can take that page over and operate it.

## How it works

| What you do | What the agent does |
|---|---|
| ― | Hands you the page and puts a link in its reply |
| Click the link to open the page | Hands control over to you |
| Do what is asked and press "Action complete" | Takes your report and carries on |

This is not the browser pane of [See a web app you started](browser-pane.md). That one opens a web app you started;
this one takes over a page the agent already has open.

## Take over the page

1. Click "**Open the browser and operate it (opens as a pane in this tab)**" in the agent's reply. Nothing opens until you do
2. The page opens in a pane of the tab you are looking at. At first it is "**View only**": the screen moves, but clicks, scrolling and keys do not reach it
3. When the agent hands control over, it becomes "**User control**" and you can operate it
4. Do what "**Requested browser action**" asks
5. When you are done press "**Action complete**", or "**Cancel action**" to give up. The agent is told

- If nothing responds, it is still "View only". The top of the pane says so too
- What reaches the agent is what you did, not proof that the site processed it. That is also why the final submit, purchase or consent click is never left to the agent
- When the pane limit is reached, it asks before replacing the current pane

## Close the view, or open it again

- "**Close view**" only stops showing it. The agent's browser and the page stay as they are
- Open it again from "**Attached browsers (existing Chromium an agent connected)**" under "**Preview**" in the workspace bar
- If the same Chromium has other tabs, "**Switch to another tab**" moves to one

## Deployments without it

A deployment that runs workspaces on Kubernetes (a preview) or ECS Fargate has no browser features, and an agent cannot
hand you a page there. Why is in [Browser pane — the usage contract](../../ref/browser-pane.md#where-there-is-no-browser-pane).

## Related

- [See a web app you started](browser-pane.md): open a web app you started yourself
- [Browser pane — the usage contract](../../ref/browser-pane.md): what the browser features cover
