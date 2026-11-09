---
audience: "anyone who wants to use skills or slash commands from a session's input box"
source_of_truth: "the input box in the Console's chat view of a session (the mirror)"
---

# Call a skill or a command

English | [日本語](skills.ja.md)

From a session's input box you can pick from the skills and commands that session can actually call.
You do not need to remember their names: insert one while reading its description.

## How it works

| What you do | What the agent does |
|---|---|
| Pick a skill or a command from the list, add arguments, send | Takes it like any prompt, and runs that skill or command |
| Pick a skill written for another agent | Gets "read that file and follow its instructions", reads it and follows it |

## Open the list

The button to the left of the input box opens the list. It shows the trigger character.

| Agent | Button | Typed at the head of the input box |
|---|---|---|
| One with a trigger character | **`/`** or **`$`** | The same character. The full-width `／` and `＄` a Japanese IME produces are accepted too |
| One without | **✦** | None (open it from the button) |

![The list opened from the "/" button to the left of the input box. /handoff and /proofread are listed with their descriptions, followed by "Show 8 more bundled with the CLI"](../../assets/conv-skills-en-light.webp)

- Each entry carries its description, an argument hint, and where it came from: **user**, **CLI** or **shared**
- The ones bundled with the CLI are folded away; "Show N more bundled with the CLI" opens them
- Which kinds offer the list is in the "Skill / command picker" row of the [Agents reference](../../ref/agents.md)

## Pick one and send it

Picking one **only inserts it into the input box; nothing is sent.** Add the arguments, then send.

| Key | What it does |
|---|---|
| **↑↓** | Move |
| **Enter** (or **Tab**) | Insert into the input box |
| **Esc** | Close the list |

## Use a skill written for another agent

Skills written for another agent and left in the repository are listed too.
Picking one inserts "Read ‹file› and follow that skill's instructions.",
so even an agent with no skill mechanism of its own can use the content.

## When it goes wrong

### The list is empty

"No skills/commands for this session" means nothing can be called from that session.
Add your own skills in **⚙ Settings → "Skills"**.

### There is no button

That kind does not offer the list. Check the "Skill / command picker" row of the [Agents reference](../../ref/agents.md).

## Related

- [Reply to a session](reply.md): using the input box
