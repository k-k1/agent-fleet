---
audience: "anyone having trouble with terminal keys, copy and paste, using a phone, or menus that do not appear"
source_of_truth: "the Console's Settings > Display and Keyboard, and how the terminal and menus behave"
---

# Fix the terminal, keys and phone layout

English | [日本語](terminal-phone.ja.md)

Find the cause and the fix when Ctrl+C or copy and paste in the terminal do not do what you expect, shortcuts do nothing, a phone is hard to use, or a menu does not appear.

## Ctrl+C does nothing in the terminal, or you cannot copy and paste

In the terminal, Ctrl+C with nothing selected interrupts the program (SIGINT). With a selection, Ctrl+C copies.

| To | Do this |
|---|---|
| Copy | Selecting copies by itself. Ctrl+C or Ctrl+Shift+C with the selection also works |
| Paste | Right-click, middle-click, Ctrl+V or Ctrl+Shift+V |

- Ctrl+C right after a mouse selection interrupts: the selection was already copied when you let go
- Ctrl+C and Ctrl+V copy and paste only while **"Ctrl+C / Ctrl+V copy and paste"** is on in ⚙ Settings → Display (on by default). With it off, both keys go to the program
- Ctrl+Shift+C and Ctrl+Shift+V always work

The details are in [Use the terminal](../files/terminal.md#copy-and-paste).

## App shortcuts such as the command palette do nothing

While a terminal has focus, a setting in ⚙ Settings → Keyboard may be passing your keys to the terminal.

| The setting that is on | What happens | To use the app |
|---|---|---|
| **"Prefer the terminal over the app while the terminal is focused"** | Every Ctrl key goes to the terminal | The leader still works: open the palette with Ctrl+K → ; (⌘K → ; on macOS) |
| **"Pass all keys to shell / SSM terminals (incl. Ctrl+K, Ctrl+P)"** | In shell and SSM terminals the leader and palette keys go to the terminal too | Move focus to another pane |

**?** opens the list of what each key does ([Use the keyboard and the command palette](../start/shortcuts.md)).

## On a phone, the keyboard pops up by itself or it is hard to use

- The **control key row** under the terminal (`Esc` `Tab` arrows `^C` `⏎`) sends keys without bringing up the soft keyboard
- **Swipe up or down with one finger** on the terminal to scroll back through earlier output
- The **≡ (menu)** at the top left opens and closes the left pane

Everything else is in [Use it on a phone](../start/phone.md). Involved work is easier on a computer.

## Only the terminal stays dark on the light theme

That is how it is built. The terminal background stays dark whatever the theme. The file viewer and the other screens follow the theme.

## A menu does not appear

| Situation | Why |
|---|---|
| The workspace is stopped | Menus handled inside the workspace are disabled or hidden |
| An item is missing from the menu | Actions that kind does not support, or that do not fit its current state, are not shown |
| A right-click menu closes at once | It closes on a click outside, on Escape, and when the window loses focus. Focus goes back to the item it was opened from |

A right-click menu also opens with the **Menu key** or **Shift+F10** while the item has focus.
What the menus contain is in [Icons, badges, and menus](../../member/badges-and-menus.md#right-click-menus).

## Related

- [Terminal](../../member/08-terminal.md)
- [Console basics](../../member/02-console-basics.md)
- [Use the keyboard and the command palette](../start/shortcuts.md#change-the-keys)
