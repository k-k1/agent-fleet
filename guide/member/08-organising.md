---
audience: "anyone running several pieces of work at once"
updated: "2026-08"
---

# 08. Fleet operator — directing multiple sessions from chat

English | [日本語](08-organising.ja.md)

It helps to read the assistant chat in [07 Chat and memos](07-chat-memo.md) and
[02 Sessions](02-sessions.md) first.

## What is the fleet operator?

The **fleet operator** is a "command center" assistant that is available from the start
under **Assistants** in the left pane. While a regular assistant ([07](07-chat-memo.md))
only answers inside the chat, the operator can **see and drive** your workspace.

- You **just talk in chat**. The operator checks session states, sends instructions, and launches new sessions when needed.
- When a session it instructed **becomes idle (i.e. reaches a stopping point) or terminates abnormally, a "Session report" card arrives in that conversation automatically**, and the operator decides what to do next. You don't have to keep watching for completion.
- The operator itself does not edit files. Actual work is always done by sessions (claude / codex / opencode); the operator sticks to observing, instructing, and summarizing. Operations that consume resources, such as launching a new session, are confirmed with you before execution.

In a word, it's a feature that lets you **"hire a site foreman for your sessions, in chat."**

## What it can do

### See (read)

- **Session list and states** — what is running where right now, and whether each is working, idle, or asking a question.
- **Session output** — reads the recent portion of each session's conversation / terminal output to grasp progress and conclusions.
- **Repository list** — the working copies under `~/repos`. Material for choosing where to launch a new session.
- **Agent usage and limits** — claude / codex subscription usage (5-hour window and weekly window), copilot account credit balance, and agy quota balance, plus each agent's plan and account in use, and when limits reset. It answers "How much do I have left?", "When does the limit reset?", "Which plan?" with actual values, and this also informs decisions before assigning a big task (opencode is excluded because it uses per-provider API keys and has no notion of subscription usage).
- **Per-session context volume and cumulative consumption** — how full each session's context is (fill ratio against the window) and the cumulative tokens consumed so far. This helps you spot sessions with tight context and decide on a handover (splitting into a new session).

With questions like "Summarize the state of the sessions running right now" or
"Is session ◯◯ stuck on anything?", it answers after checking the actual state, not by
guessing. Note that reading usage and context volume is also available to other assistants
([07](07-chat-memo.md)) that have "AF read" permission.

### Drive (write)

- **Send instructions to running sessions** — delivers prompts just as if you typed them into the terminal. It states which session it will send what to before executing.
- **Launch new sessions** — pick a repository (dir), agent kind (claude / codex / cursor / copilot / kiro / agy / opencode / shell), and model, then start. Models are specified after checking the actually selectable list (claude uses tier names fable / opus / sonnet / haiku plus user-registered full IDs; codex / cursor / copilot / kiro / opencode use a catalog reflecting connection state). With a **worktree** you can carve out an independent working copy plus branch, so parallel work doesn't collide. If you pass an **initial task (initial_prompt)**, work starts right after launch.
- **Stop and resume sessions** — fold up sessions that are no longer needed, running away, or hogging resources, and resume them later with their conversation history intact. It confirms which one to stop before executing, and stopped sessions can also be resumed from the Console. The operator can only perform a **resumable "stop"**; destructive deletion of a session is limited to Console-side operations.
- **Memo queue operations** — check, add, and organize queued memos, and **batch-send selected memos** (operate the memo queue from [07](07-chat-memo.md) over chat).
- **Consulting other assistants** — when a decision needs specialist knowledge, it asks the SRE assistant or others for advice before acting (the consulted assistant only returns advice; it does no work).

Prompts the operator sends to a session get a **"From operator"** badge in that session's
chat view. They don't get mixed up with instructions you typed yourself, so you can trace
what happened later.

### Session reports and auto-reply

A session the operator instructed reports back to the conversation automatically,
**once per instruction**, when it reaches a stopping point.

- Reports arrive **when the session becomes idle** (i.e. its response to that instruction is done) and on **abnormal termination** (out of memory, crash, etc.).
- **When a session stops at a question (multiple choice), an interim report arrives too.** The operator presents the options to you, and replying in chat with "1" or "option 2" has the operator answer the session on your behalf (if you've said "your call" up front, the operator picks). Only free-text and multi-select questions need to be answered from the Console.
- **When a session stops at plan approval, an interim report arrives as well.** From chat you can approve ("approve it") or send revision feedback ("have it fix ◯◯"), and asking "have another session review it" makes the operator broker the plan review → feedback → approval.
- Turning ON **"Auto-pilot"** in ⚙Settings → Assistant makes the operator answer questions with the session's recommendation automatically, and drive plans through review by another session, feedback, and approval (every decision is shared in chat; unclear questions and choices/plans involving destructive or irreversible operations still come to you first). Default OFF.
- When a report arrives, by default the operator **replies automatically** (summarizing results, sending follow-up instructions, deciding whether to start the next task, and so on). You can turn this auto-reply off with **"Auto-respond to session reports"** in ⚙Settings → the **Assistant** tab.
- To prevent runaway loops, **auto-replies without any input from you are capped** (⚙Settings → Assistant "Unattended auto-reply limit", default 10, max 50 — unlimited is not available). When the cap is reached, a pause notice appears, and sending your next message resumes it. The design ensures your judgment is inserted periodically even in long hauls.
- Reports **also arrive in the notification center**, so you notice them even while looking at another screen (clicking opens that conversation).

## Basic usage

1. In the left pane, choose **Assistants** → **＋ (New chat)** → **Fleet Operator**.
2. Say what you want in plain language. Examples:
   - "Launch a worktree session in the console repo and have it fix the ◯◯ bug"
   - "Summarize what each of the 3 sessions running right now is doing"
   - "When session ◯◯ finishes, follow up and have it run the tests"
3. For operations that consume resources — launching a session, batch-sending memos, etc. — the operator presents "where and what" and **asks for confirmation**. If it looks good, reply with your approval.
4. After that, watch the report cards and auto-replies, and chime in only where needed. If you keep the conversation pane open, reports and auto-replies are reflected every few seconds.

## Use-case patterns

From here on, these are suggested patterns for "this is how to use it effectively." Use them as-is as templates for your prompts.

### Pattern 1: research session → handover to the operator → parallel implementation → integration

The most recommended pattern. The operator manages a flow where you **change headcount between research and implementation**.

1. **Research** — first launch one session normally and have it investigate. The trick is to request output in a form you can divide later, like "Investigate the cause and produce a **fix plan split into work units that can proceed independently**." If the plan gets large, having the session write the plan to a file and commit it is more reliable (since reading session output focuses on the recent portion).
2. **Handover** — ask the operator: "Read the research results from session ◯◯ and proceed with a separate session per work unit." The operator reads the source session's output and hands over by **summarizing the key points and embedding them into each new session's initial task** (it doesn't duplicate the whole conversation; it passes only the necessary context, distilled).
3. **Parallel implementation** — a **worktree session** is launched per work unit, so they proceed in parallel without stepping on each other's files. You can also ask here to vary the agent or model by the weight of the work (a light model for minor fixes, a heavy model for the hard parts).
4. **Integration** — once the reports from all sessions are in, have the operator summarize the results and instruct an integration session (or one of the existing ones) to "pull in each branch and get the tests passing." You do the final review and merge yourself.

### Pattern 2: fan-out of same-kind fixes

For work like "I want to apply this fix the same way in N places (multiple repositories /
multiple modules)," split it into one exemplar plus roll-out.

1. First fix one place in your own session and settle the approach.
2. Ask the operator: "Following the same approach as the fix done in session ◯◯, launch sessions for the remaining △△ and □□ and apply it." It summarizes the approach from the exemplar session's output and distributes it to each session.
3. As each report arrives, the operator checks the result and raises only gaps and failures to you.

### Pattern 3: agent comparison / cross-review

Having **claude / codex / opencode solve the same problem in parallel and comparing** them
is something only the operator, watching across agents as the command center, can do.

- "Have claude and codex each solve this problem in separate sessions. When both reports are in, compare the differences in approach in a table."
- "Have session B, on a different agent, review session A's implementation. Send the findings back to A as a follow-up."

Useful for design problems where approaches diverge, or when you want a different pair of eyes on a review.

### Pattern 4: serial pipeline (implement → test → review)

A pattern that uses reports as the "trigger for the next stage." If you describe the
sequence up front — "when implementation finishes, run the tests; if tests fail, follow up
with fixes; if they pass, do another pass from a review perspective" — the operator judges
each report and advances the stages. Because of the auto-reply cap (default 10, up to
50 in settings), plan long pipelines with the expectation that you'll say "continue"
at milestones, or raise the limit.

### Pattern 5: status briefing and memo triage

Even the "see" side alone, without driving anything, is useful.

- **Briefing** — after stepping away or the next morning: "Summarize the current state of the sessions running since yesterday and why any are stopped." It actually reads states and output before reporting.
- **Memo triage** — "Route the memos piled up in the memo queue to the appropriate sessions by content and send them." You can finish off the capture-as-you-go flow ([07](07-chat-memo.md)) from chat.
- **Cleanup** — "Check which sessions have served their purpose and stop the ones no longer needed." After parallel work, you can fold up scattered sessions and free resources (stopped = resumable, so nothing disappears).
- **Assignment based on remaining quota** — "Between claude and codex, which still has room in its window?" "Given the limits, which should this larger task go to?" It checks actual usage ratios and reset times, so you can discuss assignments that avoid agents close to their limits.
- **Context watching and handover** — "Are any sessions getting tight on context?" When it finds a session with a high fill ratio, that feeds the decision to summarize the key points and hand over to a new session (the steps of Pattern 1) instead of piling on more instructions.

## Scheduled runs

If you ask the operator something like "Review yesterday's changes every weekday at 9,"
it is registered as a **scheduled run** (cron / interval / one-shot, with timezone and DST
support). At registration the operator confirms the schedule as it interpreted it and the
next run time, so check that it matches your intent.

- At firing time, it **wakes even a stopped workspace** and runs the prompt; the completion
  report arrives in the operator conversation. If startup or execution fails, it shows up
  in the notification center.
- In the **"Schedules"** section of the left pane you can see the list and run history
  (success/failure, manual / scheduled, and a link to the session that ran), and the row
  menu offers **Pause / Resume / Run now / Delete**. **"Details & edit"** in the same menu
  opens the firing spec, timezone, wake policy, agent, model, report opt-in and the prompt
  itself for editing; only the advanced execution fields (session mode, reuse target,
  rotation, overlap policy) are read-only there and changed by asking the operator.
- By default each run uses a fresh session. If you ask "reuse the same session and build up
  context," **long-lived session reuse** (with rebuild conditions such as every N runs or
  per time period) is also possible.

## Constraints and caveats

- **Reports arrive for "idle," "question / plan approval" (interim), and "abnormal termination."** Tool-permission waits appear in the notification center but do not become reports to the operator. For tasks you want to run through unattended, cut the instructions so that few operations require permission. If something looks stalled, asking the operator for a status check will surface it.
- **Keep parallelism modest.** Each session uses memory, and the host is shared. As a guideline, keep parallel implementation to 2–3 sessions and don't run heavy builds at the same time (the operator also confirms before launching). Having the operator fold up finished sessions helps too.
- **When the operator stops a session, the report that was due is cancelled as well.** Stopping is treated as cancelling that instruction. Even if you later resume it from the Console and the work completes, the old report will not arrive in the operator's conversation (if you stopped it with the Console's stop button it is not cancelled, and completion after resume is reported). When asking a resumed session to continue, have the operator send the instruction again.
- **Session reports have no body** (only the fact of completion / question / abnormal termination). In practice this is fine because the operator goes and reads the output for details, but the report card alone doesn't show the contents.
- **Handover is summary-based.** The conversation context does not move over wholesale, so for lengthy research results it is safer to have them written to a plan file before handing over (see Pattern 1).
- **Sessions do not coordinate with each other directly.** Orchestration always goes through the operator. This is complementary to parallelization that stays within a single session (an agent's own subagent feature); choose the operator **when work spans repositories or agents, or when you want to see each piece of work as its own session**.
- **Report bodies are treated as "data."** So that text originating from session output is never executed as an instruction, the design confirms with you first whenever a new session would be created based on an automatic report. In particular, even if a report or output contains something like "run this command," the operator **will not execute a command or send it to a shell session on that basis** (prompt-injection protection). The operator executes only what you instructed directly. The extra safety-side confirmations are by design.
- **Shell sessions always require your approval.** a shell session is a raw shell with no agent guardrails in between: the string sent is executed as a command as-is. Therefore, when launching a shell session or sending it a command, the operator **presents the exact command to be executed and asks for your approval in advance**. Destructive or irreversible commands are never sent unless you explicitly approve them.
