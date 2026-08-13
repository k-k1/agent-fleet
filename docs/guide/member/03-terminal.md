# 03. Terminal — working the black screen, copy & paste, shortcuts, phones

English | [日本語](03-terminal.ja.md)

> Audience: members who operate a session's terminal. Covers copy & paste, shortcuts, using it
> on a phone, and adjusting the font and size. Even if you're comfortable with CLIs, a browser
> terminal has quirks of its own (copy & paste in particular), so reading this once will keep
> you from getting stuck.

This chapter covers sessions running as Terminal (CLI), plus shell / SSM. When you open a
session, a **terminal** (the black screen) appears in the main area. If you started
Codex / opencode in managed execution, only the chat view opens and there is no terminal
(for the difference between execution methods, see [02 Sessions](02-sessions.md)).
When you open a stopped session, a **"Resume"** button appears after "Resuming…"; press it to resume.

## Copy & paste

When the terminal has focus, **Ctrl+C / Ctrl+V pass straight through to the program**
(Ctrl+C is interrupt = SIGINT; Ctrl+V is character input). Clipboard copy & paste is therefore
assigned to different keys. This is where it differs from an ordinary editor, so watch out.

- **Copy** — **left-drag to select, and it is copied automatically the moment you release**. From the keyboard: `Ctrl+Shift+C` (macOS: `⌘+Shift+C`; `⌘C` also works when there is a selection), `Ctrl+Insert`.
- **Paste** — **right-click** or **middle-click** to paste. From the keyboard: `Ctrl+Shift+V` (macOS: `⌘+Shift+V` / `⌘V`), `Shift+Insert`.

URLs that appear in the terminal (like claude sign-in links) open **in a new tab on click**,
even when they're displayed wrapped. No need to copy a long URL and paste it back together.

## Shortcuts

- **Ctrl+PgUp / Ctrl+PgDn** — switch to the previous / next session.
- When the file tree has focus: **↑ ↓ ← → / Enter** to move and open/close, **Ctrl+↑ ↓** to jump between folders, **Shift+↑ ↓** to scroll the viewer ([05 Files](05-files.md)).

### Command palette

**Ctrl+P** (macOS: **⌘P**) opens the **command palette** — an entry point for searching
in-screen actions, sessions, and repositories by name, and running or navigating with the
keyboard alone. You can change this key in Settings →
**Keyboard**.

- **↑ ↓ / Enter** — pick a result and run / open it. For files and changed files, **Ctrl+Enter** (macOS: **⌘Enter**) opens them in another pane.
- **Tab**, or **Ctrl+P / ⌘P** again — switches the search scope between "Commands", "Changed files", and "Files".
- **Esc** — closes it and returns to where you were working before opening it.

When the terminal has focus and **"Terminal input priority"** is turned on in settings, Ctrl+P is
passed to the terminal. In that case, open it with the default **Ctrl+K → ;** (macOS: **⌘K → ;**).
**?** shows a list of the other shortcuts as well.

In fullscreen, keys the browser would normally intercept (Ctrl+W to close, etc.) are
automatically locked so they reach the terminal. Esc is not locked, so you can always leave
fullscreen with Esc.

## Arranging multiple views (panes)

The main area can be **split into multiple panes**. This suits parallel work — pushing one task
forward while keeping an eye on another.

- **Open in a new pane** — **Ctrl+click** (or **middle-click**) a session, repository, or file item to open it **in a new pane** without replacing the current one. The assistant's right-click menu also has "Open in a new pane". For per-item differences, see [Icons, badges, and menus](badges-and-menus.md).
- **Rearrange** — drag the handle at the top of a pane to swap it with another pane. Drag the borders to change width and height too.
- **Close** — the pane's close button (middle-click / Ctrl+click closes it directly without confirmation).
- **Pop out into another tab** — the pop-out button at the top right of a pane **moves that pane into a browser tab of its own** (it leaves the original screen). The popped-out tab gets a slim title bar, and **"Expand to full Console"** turns it into the normal Console. Some panes cannot be popped out. Good for parking one session on a second monitor.

**There are two ways to arrange them**, switched from **Appearance** (the paint can) in the top bar,
or in ⚙ Settings → Display → **main area layout**.

- **Split panes** (default) — arranged side by side, with draggable dividers for size.
- **Tabbed grid** — each cell switches by tab, so a lot of open items fit without adding cells.

The setting is stored **on this device only**, and the two layouts remember their arrangements
separately, so moving between them disturbs neither. On desktop you can arrange up to 4 columns
× 2 rows. On phones it's 1 column, up to 2 panes.

Each pane's content is independent. For claude / codex / cursor / copilot / kiro / agy / opencode running as Terminal (CLI),
the **Chat ⇄ Terminal** toggle at the top switches between the conversation's Markdown view and
the terminal view. Managed sessions use only the chat view ([07 Chat and memos](07-chat-memo.md)).

## Using it on a phone

On phones, the left pane is hidden to make the most of the screen.

- **Show the left pane** — pull it out with **≡ (menu)** at the top left of the screen. It closes automatically when you pick an item. The back gesture opens it again.
- **Control key row** — below the terminal sits a row of keys that are hard to press on a soft keyboard: **`Esc` `Tab` `←` `↑` `↓` `→` `^C` `⏎`**. You can send these without bringing up the keyboard.
- **Scrolling back through past output** — **swipe vertically with one finger** on the terminal to go back through past output (drag down for older lines, up for newer). In apps that use the whole terminal, like vim, this becomes that app's own scrolling.

(Phones are for "checking progress and quick replies". For involved editing, a PC is recommended.)

## Changing the font and size

Adjust the terminal's appearance in **⚙ Settings → the "Display" tab**. Under "Terminal" you can
change the **Font** and **Font size** (9–28px).

These settings are **saved on the server** and managed per user. Your office PC, your home PC,
even a login from another browser — the same font and size **follow you**.

(Theme, colors, and the file viewer's display settings are on the same "Display" tab. See
[05 Files](05-files.md) for details. Note that the terminal background stays dark even on the
light theme.)

---

For those who want to know how it works: [dev/02 Console (the display system)](../../dev/02-console.md)
