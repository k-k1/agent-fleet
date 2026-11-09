---
audience: "anyone who found a bug in Agent Fleet itself or wants to ask for a feature"
source_of_truth: "the issues of Agent Fleet's GitHub repository"
---

# Report a bug or request a feature

English | [日本語](report.ja.md)

Tell the Agent Fleet project about a bug in Agent Fleet itself, or a feature you want, with a GitHub issue.

## How it works

| What you do | What the agent does |
|---|---|
| Write down the symptom and the message you saw | If you ask, drafts the issue and files it |
| Remove what must not leave your organization before you paste | If you ask, strips secrets from logs and error text |

## First decide who to ask

| What is wrong | Who to ask |
|---|---|
| Logins, connections, permissions, machine size, tenant settings | Your team's administrator or IT staff |
| Agent Fleet itself misbehaves, not your team's settings | A GitHub issue |
| You want a feature | A GitHub issue |

If the symptom pages (listed under Related in [Frequently asked questions](faq.md)) do not solve it, ask with the symptom and the message you saw.

## File an issue

1. Open <https://github.com/k-k1/agent-fleet/issues>
2. Write what happened, what you expected, and the message you saw
3. Submit it

From inside a session, you can also ask the agent to "file this bug as an issue".

## What to remove before you paste

**The repository is public.** Before you paste logs, screenshots or error text, always remove what must not leave your organization:

- Secrets (tokens, passwords, keys)
- Internal hostnames and URLs
- Tenant and customer names

Describe your environment in general words instead of real names (for example "a deployment on AWS ECS").

## Related

- [Frequently asked questions](faq.md)
