---
audience: "anyone using the terminal"
updated: "2026-10"
---

# 08. Terminal — copy & paste, round-trip time, font and size

English | [日本語](08-terminal.ja.md)

Even if you're comfortable with CLIs, a browser terminal has quirks of its own
(copy & paste in particular), so reading this once will keep you from getting stuck.

This chapter covers sessions running as Terminal (CLI), plus shell / SSM. When you open a
session, a **terminal** (the black screen) appears in the main area. If you started
Codex / opencode in managed execution, only the chat view opens and there is no terminal
(for the difference between execution methods, see [03 Starting sessions](03-starting-sessions.md)).
When you open a stopped session, a **"Resume"** button appears after "Resuming…"; press it to resume.

## Copy & paste

When the terminal has focus, **Ctrl+C with text selected copies it, and Ctrl+V pastes**, as in
Cloud Shell. **With nothing selected, Ctrl+C is still the interrupt (SIGINT)**, so a running command
can always be stopped. Copying with Ctrl+C clears the selection, and a selection that was already copied (auto-copy on release) does not count, so Ctrl+C right after a drag interrupts. If you would
rather send both keys to the program (Ctrl+V as character input), turn off **Ctrl+C / Ctrl+V copy and
paste** under Settings > Display > Terminal; the other [shortcuts](02-console-basics.md#shortcuts) keep working.

- **Copy**: **left-drag to select, and it is copied automatically the moment you release**. From the keyboard: `Ctrl+Shift+C` (macOS: `⌘+Shift+C`; `⌘C` also works when there is a selection), `Ctrl+Insert`. A short "Copied" notice confirms a keyboard copy, and a notice appears if the browser refuses clipboard access.
- **Paste**: **right-click** or **middle-click** to paste. From the keyboard: `Ctrl+Shift+V` (macOS: `⌘+Shift+V` / `⌘V`), `Shift+Insert`.

**Paste confirmation.** When the program is reading a plain prompt (a shell, say), a paste with a
newline in it would run line by line, and a very large one floods the pane. So if the clipboard
text contains a newline or is longer than 1,000 characters, a **"Paste into the terminal?"** dialog
shows how many characters (and lines) are about to go in, and warns that a shell may execute text
with newlines directly; **Paste** goes ahead, cancelling pastes nothing. It applies to every paste
gesture above. Short single-line text is pasted without asking, and so is anything pasted into a
program that accepts pasted text safely (the agent CLIs, vim, most readline prompts), where a
paste is not executed line by line.

URLs that appear in the terminal (like claude sign-in links) open **in a new tab on click**,
even when they're displayed wrapped. No need to copy a long URL and paste it back together.

## How far away the terminal is (round-trip time)

A terminal pane's header carries a small **round-trip time in milliseconds**: how long it takes a
byte to reach the workspace and come back. It is measured over **the same socket and the same
frames your keystrokes use**, so it is what the echo of a keypress actually costs, not a separate
network test.

- The figure is the **median** of the recent samples. Hover it for the median, the worst one, and
  how many samples that is.
- Under about **120 ms** it feels local; past about **300 ms** every keystroke is visibly behind
  your finger, and the colour says which side of that you are on.
- The terminal itself (PTY and tmux inside the workspace) is well under a millisecond, so **this
  number is the connection**, not the machine. If it reads low while the agent still feels slow,
  what you are waiting for is the agent thinking, not the link.

## Changing the font and size

Adjust the terminal's appearance in **⚙ Settings → the "Display" tab**. Under "Terminal" you can
change the **Font** and **Font size** (9–28px), and turn **Ctrl+C / Ctrl+V copy and paste** off
or on ([Copy & paste](#copy-paste)).

These settings are **saved on the server** and managed per user. On your office PC, your home PC,
or a login from another browser, the same font and size **follow you**.

(Theme, colors, and the file viewer's display settings are on the same "Display" tab. See
[07 Files](07-files.md) for details. Note that the terminal background stays dark even on the
light theme.)
