---
audience: "anyone who wants patterns for what to ask the Fleet Operator"
source_of_truth: "The Fleet Operator under Assistants in the Console's left pane"
---

# Ways to use the Fleet Operator

English | [日本語](operator-patterns.ja.md)

Five patterns that work well when you ask the Fleet Operator for something. Use them as drafts for your own prompts.
How to start the operator, and what it can do, is in [Hand work to the Fleet Operator](operator.md).

## How it works

| Pattern | Suits |
|---|---|
| [Research, split and implement in parallel](#research-split-and-implement-in-parallel) | Work from investigation to fix that splits into several independent pieces |
| [Apply the same fix in many places](#apply-the-same-fix-in-many-places) | The same change across several repositories or modules |
| [Compare agents, cross-review](#compare-agents-cross-review) | Design problems with competing approaches, or a review by other eyes |
| [Implement, test and review in turn](#implement-test-and-review-in-turn) | Work where each step's result decides the next |
| [Get a briefing, triage memos](#get-a-briefing-triage-memos) | Knowing how things stand before moving anything |

## Research, split and implement in parallel

The pattern we recommend most. It changes how many sessions run between research and implementation.

1. **Research**: start one session the usual way and have it investigate. Ask for output that splits easily, such as "find the cause and write a **fix plan split into pieces that can proceed independently**". For a large plan, have it written to a file and committed (the operator reads mostly the end of a session's output)
2. **Hand over**: ask the operator

   > Read session ◯◯'s findings and run each piece of work in its own session

   The operator reads the original session's output and puts the key points into each new session's first task
3. **Implement in parallel**: each piece starts in a worktree session, so they do not step on each other's files. You can also ask for light models on small fixes and heavier ones on the hard parts
4. **Bring it together**: when the reports are in, have the operator summarise them and tell an integrating session to "merge each branch and get the tests passing". The final review and merge are yours

## Apply the same fix in many places

For applying one fix across several repositories or modules. Split it into one example and its rollout.

1. Fix one place in your own session first and settle the approach
2. Ask the operator

   > Apply the same fix as in session ◯◯ to △△ and □□, starting a session for each

   The operator distils the approach from the example session's output and passes it to each session
3. As each report arrives the operator checks the result and brings you only what was missed or failed

## Compare agents, cross-review

The operator sees across agent kinds, so it can have claude, codex and opencode solve the same problem in parallel and compare.

> Have two sessions, claude and codex, solve this separately. When both report, compare their approaches in a table

> Have session B, on another agent, review session A's implementation, and send the findings back to A

## Implement, test and review in turn

This pattern uses each report to trigger the next step. Lay out the steps up front, and the operator moves on as each report comes in.

> When the implementation is done, run the tests. If they fail, have it fix them; if they pass, do another round as a review

Automatic replies have a limit (10 by default, 50 at most). For a long run, say "continue" at milestones, or raise **Unattended auto-reply limit** in ⚙ Settings → Assistant.

## Get a briefing, triage memos

Just looking, before moving anything, is useful too.

| To do this | Ask |
|---|---|
| Catch up after time away or the next morning | "Summarise where the sessions running since yesterday are, and why any have stopped" |
| Sort the memos that piled up | "Send the memos in the queue to the sessions that fit each one" |
| Clear away finished sessions | "Check which sessions are done and stop the ones not needed" "Show me what can be cleared away" |
| Assign work by remaining usage | "Which of claude and codex has more usage left?" "Given the limits, which should take this big task?" |
| Find sessions with a full context | "Are any sessions running out of context?" |

- A stopped session can be resumed, so it is not lost. Approve the list and finished ones can be archived or deleted too
- For a session with a full context, rather than piling on instructions, summarise the key points and hand over to a new session (step 2 of [Research, split and implement in parallel](#research-split-and-implement-in-parallel))
- Collecting memos as ideas come is described in [10 Chat and memos](../../member/10-chat-memo.md)

## Related

- [Hand work to the Fleet Operator](operator.md)
- [Run at a set time](schedules.md)
