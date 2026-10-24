---
audience: "anyone finding their way around the Console"
updated: "2026-10"
---

# 02. Console basics — shortcuts, command palette, panes, phones

English | [日本語](02-console-basics.ja.md)

This chapter is about moving around the Console itself rather than any one kind of session: keyboard shortcuts, the command palette, laying out several views as panes, and using the Console on a phone. Behaviour specific to the terminal (copy & paste, font size) is in [08 Terminal](08-terminal.md).

## Shortcuts

- **Alt** covers the everyday actions, one keypress each: **Alt+N** start a session,
  **Alt+W** close this tab / pane, **Alt+Shift+W** close them all, **Alt+1..8** jump to a
  pane by its number, **Alt+[ / Alt+]** previous / next pane, **Alt+PgUp / Alt+PgDn**
  previous / next tab, **Alt+A** add a memo, **Alt+G** cycle working sets, **Alt+/** jump to
  the rail's filter box, **Alt+Q** mute the read-aloud, **Alt+,** open settings.
  All of them are rebindable in Settings › Keyboard.
- **Alt+U**: jump to the next session that **needs you**: first the ones waiting on your
  answer (question, plan approval, permission), most recent first, then the ones with an
  unread notification (a finished turn, a report). Press again to walk the rest. In the
  left rail a session waiting on you is ringed, and one with unread news has a bold title
  and a red dot; a pane showing a session that waits on you is ringed too. Opening the
  session clears the unread mark; the ring goes away when you answer.
- **Alt+= / Alt+- / Alt+0**: make the text you're looking at **bigger / smaller / default
  size**. The terminal, the file viewer, the conversation (mirror) and the read-aloud view
  each keep their own size, and the one belonging to the focused pane is what moves (the
  same number you see in Settings › Display). Zooming the whole browser is still
  **Ctrl+= / Ctrl+-**.
- When the file tree has focus: **↑ ↓ ← → / Enter** to move and open/close, **Ctrl+↑ ↓** to jump between folders, **Shift+↑ ↓** to scroll the viewer ([07 Files](07-files.md)).

In fullscreen, keys the browser would normally intercept (Ctrl+W to close, etc.) are
automatically locked so they reach the terminal. Esc is not locked, so you can always leave
fullscreen with Esc.

## Command palette

**Ctrl+P** (macOS: **⌘P**) opens the **command palette**, an entry point for searching
sessions, in-screen actions, and repositories by name, and running or navigating with the
keyboard alone. You can change this key in Settings →
**Keyboard**.

### Sessions

It opens on **"Sessions"**, the list of your sessions with their working copy, worktree and
status badge (on **"Commands"** when the workspace has no sessions at all). The order is the order you most likely want:

1. sessions **waiting for you** (a question, a plan to approve, a permission prompt), the one
   that started waiting most recently at the top;
2. the rest of the running sessions;
3. stopped sessions at the foot (the ones that were folded away with an unanswered question
   first). Archived sessions are not listed.

The order is fixed at the moment you open the palette, so a session that changes state while
you are reading does not move under your cursor; only its badge follows.

With nothing typed, a session started by another session is listed under it, indented, and the
whole family sits where its most urgent member would: a stopped parent comes up with a child that
is waiting for you. Children follow in the order they were started. A session started while the
palette is open stays at the foot until you open the palette again. Once you type, the list is
flat again and a child's row names its parent (**↳** and the parent's title); typing the parent's
title also finds its children.

### Keys inside the palette

- **↑ ↓ / Enter**: pick a result and run / open it. **PageUp / PageDown** move the highlight by one screenful of rows (stopping at the first and last row). For sessions, files and changed files, **Ctrl+Enter** (macOS: **⌘Enter**) opens them in another pane.
- **Tab**, or **Ctrl+P / ⌘P** again: switches the search scope between "Sessions", "Commands", "Changed files", "Files", "Conversations", "Memory" (while Agent memory is on) and "Session changes" (the files changed by the session in the active pane, when that agent has a conversation the Console can read). **Shift+Tab**, or **Ctrl+Shift+P / ⌘⇧P**, goes back one scope instead, and wraps from the first to the last. The reverse chord is your palette key plus Shift (rebind the palette key to Alt+O and it is Alt+Shift+O); if your palette key already includes Shift there is no reverse chord, and Shift+Tab is the way back. Scopes that are not offered (for example "Session changes" without an active session) are skipped in both directions. Some browsers keep Ctrl+Shift+P for themselves (for example a private window in Firefox) and do not let a page cancel it; the palette only gets the key when the browser passes it on, so use Shift+Tab there.
- **Esc**: closes it and returns to where you were working before opening it.

When the terminal has focus and **"Terminal input priority"** is turned on in settings, Ctrl+P is
passed to the terminal. In that case, open it with the default **Ctrl+K → ;** (macOS: **⌘K → ;**).
**?** shows a list of the other shortcuts as well.

### Conversations

**"Conversations"** searches what was said in every session of this workspace (any agent kind,
running, stopped and archived). Type a few words (all of them must occur; Japanese works from two
characters) and each hit shows the session, who said it and when, and the matching text.
**Enter** opens that session scrolled to the turn. Only the conversation itself is searched, not
tool output or the agent's thinking. A hit in an archived session opens the archive: restore it there, then open it from the list
and it opens at the same turn. The first search after an update can take a minute or
two to cover every session; the palette says "Building the index" until it has.
A turn older than the part of the conversation the mirror has loaded is fetched first: the mirror
loads earlier conversation until it reaches the turn, up to 8,000 positions back in at most four
requests. Further back than that, a notice says the turn is too far back to jump to, and
scrolling up loads it. Switching session or scrolling yourself while it loads stops the jump. If the
search itself fails, the palette says why and offers to retry, rather than reporting no match.

### Memory

**"Memory"** searches the memories agents saved in Agent Fleet's own agent memory
([Settings → Agent memory](18-agent-memory.md#memories)); it appears only while that switch is on. Names, descriptions, types and authors match as you
type, and the text of each memory is searched a moment later; a hit that matched in the text shows the matching line.
**Enter** opens Settings → Agent memory → Memories with that memory selected. The workspace has to be running (the
memory lives inside it), and a memory withheld by the secret scan is only counted, never listed. If the
search itself fails, the palette says why and offers to retry, rather than reporting no match.

## Arranging multiple views (panes)

The main area can be **split into multiple panes**. This suits parallel work: pushing one task
forward while keeping an eye on another.

- **Open in a new pane**: **Ctrl+click** (or **middle-click**) a session, repository, or file item to open it **in a new pane** without replacing the current one. The assistant's right-click menu also has "Open in a new pane". For per-item differences, see [Icons, badges, and menus](badges-and-menus.md).
- **Rearrange**: drag the handle at the top of a pane to swap it with another pane. Drag the borders to change width and height too.
- **Close**: the pane's close button (middle-click / Ctrl+click closes it directly without confirmation).
- **Pop out into another tab**: the pop-out button at the top right of a pane **moves that pane into a browser tab of its own** (it leaves the original screen). The popped-out tab gets a slim title bar, and **"Expand to full Console"** turns it into the normal Console. Some panes cannot be popped out. Good for parking one session on a second monitor.

**There are two ways to arrange them**, switched from **Appearance** (the paint can) in the top bar,
or in ⚙ Settings → Display → **main area layout**.

- **Split panes**: arranged side by side, with draggable dividers for size.
- **Tabbed grid** (default): each cell switches by tab, so a lot of open items fit without adding cells.
  Closing the tab you are on brings back **the tab you were on before it**, not the one next to it
  in the strip: open a file from a chat, close it again, and you are back on that chat.
  **Right-clicking a session's tab** (or the **Menu key** / **Shift+F10** while it has focus) opens
  the same menu as that session's row in the left pane, so you can rename, hand over, archive and
  the rest without going back to the rail. Tabs that are not sessions keep the browser's own menu.

The setting is stored **on this device only**, and the two layouts remember their arrangements
separately, so moving between them disturbs neither. On desktop split panes go up to 4 columns
× 2 rows, and the tabbed grid up to 3 columns × 2 rows with 24 tabs in all. On phones it's 1 column, up to 2 panes.

Each pane's content is independent. For claude / codex / cursor / copilot / kiro / agy / opencode running as Terminal (CLI),
the **Chat ⇄ Terminal** toggle at the top switches between the conversation's Markdown view and
the terminal view. Managed sessions use only the chat view ([10 Chat and memos](10-chat-memo.md)).

## Using it on a phone

On phones, the left pane is hidden to make the most of the screen.

- **Show the left pane**: pull it out with **≡ (menu)** at the top left of the screen. It closes automatically when you pick an item. The back gesture opens it again.
- **Control key row**: below the terminal sits a row of keys that are hard to press on a soft keyboard: **`Esc` `Tab` `←` `↑` `↓` `→` `^C` `⏎`**. You can send these without bringing up the keyboard.
- **Scrolling back through past output**: **swipe vertically with one finger** on the terminal to go back through past output (drag down for older lines, up for newer). In apps that use the whole terminal, like vim, this becomes that app's own scrolling.
- **Stepping through the running sessions**: **swipe right** for the running session one row below the current one in the left pane, and **swipe left** for the one above, wrapping around at either end. The order is the left pane's, including worktrees nested under the session that started them; shell and SSM sessions are skipped. Each switch briefly shows where you landed, as in "2/3 session name". While a working set is selected, the rotation stays inside that set. **A right swipe that starts at the very left edge** still pulls out the left pane, as before; start away from the edge when you mean to move down a row. A left swipe with the left pane open still closes it. Over a text field, the browser pane, or anything that scrolls sideways (a code block, say), that surface keeps the gesture and no switch happens.

- **The workspace action bar**: on a narrow screen the buttons that no longer fit move into its
  **⋯** menu, keeping their labels there: first **Home**, **Sessions** and **Images**, then also
  **Split down** and **Close all**.
- **Notifications (toasts)**: on a phone they sit just below the bars at the top, never over the composer, and a tap outside dismisses them. Ones with a button, such as **Update** or an AWS login, close only with their ×.

(Phones are for "checking progress and quick replies". For involved editing, a PC is recommended.)
