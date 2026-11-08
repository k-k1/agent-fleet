---
audience: "People who want to know when their workspace was running and what it costs in the cloud"
source_of_truth: "The Console's Settings > Running time and Cloud cost"
---

# See running time and cloud cost

English | [日本語](cost.ja.md)

See when your workspace was running and what it costs in the cloud. It also shows a workspace you forgot to stop.

## How it works

| What you do | What you see |
|---|---|
| Open **Running time** | For each hour, whether it was running and how many sessions were open |
| Open **Cloud cost** | For each day, the cloud cost attached directly to your workspace |
| Pick a period and press **Apply** | The figures for that period |

The two tabs do different jobs.

| Tab | Unit | Money |
|---|---|---|
| Running time | One hour | None: time running and the number of sessions |
| Cloud cost | One day | Yes. Cloud cost is only available per day |

When one day costs more than the others, the running time of that day shows why. Token usage is on the **Agent usage**
tab.

## See the running time

Open **⚙ Settings → Running time**. It is a grid of 24 hours down and dates across; one cell is one hour.

| Cell | Meaning |
|---|---|
| Grey | Stopped |
| Coloured | Running. The darker, the more sessions were open at once |
| Blank | Not recorded (that hour was not being recorded) |

- Blank and grey mean different things. Hours before recording began stay blank and cannot be filled in later
- **What the shading shows** switches between **Sessions open**, **Sessions working** and **Running time**
- Hover over a cell to see that hour's breakdown (minutes running, the most sessions)
- Switch to **Table** for the same figures as numbers
- Times are in your device's time zone
- Recording samples every few minutes, so a cell is accurate to one sample

A band of colour through the night is more often a workspace left running than work being done.

## See the cloud cost

Open **⚙ Settings → Cloud cost**. The tab exists only on deployments that run on AWS.

It shows **the cost attached directly to your workspace**.

| Included | Not included (shared infrastructure) |
|---|---|
| Slot hours, the persistent home volume, its snapshots | NAT, DNS, load balancers, databases, idle slots |

Shared infrastructure is not divided between members, so this amount is not the whole cost of running your workspace.

- It shows the total, the day-by-day trend and **What it was spent on**
- Amounts are in the currency of the bill (US dollars) and are not converted
- The figures are about 24 hours behind. The most recent days are not final yet and can still change

## See the month-by-month trend

Both tabs show the totals for the last six months (**Running hours by month**, **Cloud cost by month**). They do not follow
the period picked above; they always cover the last six months.

- Months are cut in UTC
- The change against the previous month appears as a percentage
- A month that is not over, such as the current one, is marked **so far**
- A month before cost allocation began shows **no data**, not zero

## When it goes wrong

### There is no Cloud cost tab

The deployment does not run on AWS, or it does not collect costs. Running time is available on every deployment.

### "Nothing is available before …"

Cost allocation counts only from the day it was switched on. Earlier days are never filled in.

### Yesterday shows zero although you used it

Cloud costs arrive about 24 hours late. Look again a day later.

### "Cost Explorer could not be read, so these figures are stale or empty"

The costs could not be read, so the figures are old or empty. If it persists, tell an administrator.

## Related

- [Change the machine size](machine.md)
- [Recreate the workspace](recreate.md)
