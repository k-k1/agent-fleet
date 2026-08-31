# 0017. The Console keyboard system — one capture-phase dispatcher, a Leader key and a palette, plus rebinding

English | [日本語](0017-keyboard-system.ja.md)

- Status: decided; P0–P5 implemented (2026-07-16) — P0 (dispatcher + registry) / P1 (moving
  between areas and panes) / P2 (Leader + which-key + the command palette) / P3 (modal
  focus-trap, roving in menus and the rail) / P4 (the `?` cheat sheet + inline hints on buttons) /
  P5 (the rebinding UI in settings + the terminal-input-priority toggle)
- See also: [29-keyboard-system.md](../log/29-keyboard-system.md) (the design proper and the implementation map) /
  [0011-console-rebuild.md](0011-console-rebuild.md) (the Console foundation this system sits on) /
  [0016-i18n.md](0016-i18n.md) (where command wording will eventually be collected, in lib/i18n)

## Context

The Console drives one workspace through several simultaneous panes (terminal / chat / files /
diff, …). But **xterm, the centre of interaction, swallows almost every key to the PTY while
focused** (`attachCustomKeyEventHandler` in `terminal/term.ts` takes `Ctrl+*` and F1–F10 across
the board with `preventDefault`; the only carve-outs passed through are Ctrl+C/V and zoom). So
there was effectively no way to "operate the app while the terminal has focus". Interaction
depended heavily on the mouse and could not be completed from the keyboard alone.

## Decision

1. **A single capture-phase dispatcher** (`features/keys/dispatcher.ts`) subscribes to `window`
   keydown exactly once, in the capture phase. Both the xterm handler and React's onKeyDown are
   descendants of window in the DOM tree, so it takes **only registered keys** first, with
   `preventDefault + stopPropagation`, and passes unregistered keys through (the shell is
   untouched). This is the only mechanism that pierces xterm's grab.

2. **A hybrid interaction model**: Leader (`Ctrl/⌘+K`) + which-key + the command palette
   (`Ctrl/⌘+P`) + a small number of direct accelerators (the `Alt` modifier). `Ctrl≡⌘`
   (`e.ctrlKey || e.metaKey` collapsed into one token, `mod`). Nothing fires during IME
   composition (`isComposing` / keyCode 229) or on auto-repeat.

3. **Key normalisation is based on `KeyboardEvent.code`** (`lib/keys/chords.ts`). `.key` is not
   used because it mutates under ⌥/Shift on a Mac (⌥+1→"¡", Shift+k→"K", while code stays
   Digit1/KeyK). Shift is kept as an independent modifier (`k` and `shift+k` are different
   bindings — used for hjkl/HJKL pane movement).

4. **Escape is not taken in the capture phase.** Closing an overlay is escLayer's job in the
   bubble phase by design, so stopping Escape during capture would break Esc for every modal and
   menu. The only exception is while a leader key is pending. While an overlay is open the
   dispatcher itself is deactivated (`escLayer.hasOpenOverlay()`).

5. **Layering**: pure logic lives in `lib/keys/` (`chords.ts`, `registry.ts` — importing neither
   the store nor the DOM, and covered by vitest); store coupling lives in `features/keys/`. The
   command DATA is collected in one place (`ALL_COMMANDS` in `commands.ts`), and the dispatcher,
   which-key, the palette, the cheat sheet and the button hints all read from it — so display and
   behaviour can never drift.

6. **[P5] Only the direct accelerators and three app-wide keys can be rebound** (Leader,
   palette, cheat sheet). The sequences under the leader (`p r`, `w t`, …) are navigation through
   a tree, and arbitrary rebinding would complicate collision management in that tree and risk
   making the app unusable, so they are fixed. Overrides are saved in `Settings.keybindings`
   (`id → chord`, with `""` meaning explicitly disabled) and resolved by `effectiveCommands()` /
   `boundChord()` in `features/keys/bindings.ts`, layered over `ALL_COMMANDS` and the reserved
   keys. The layering is done by the pure function `applyOverrides()` (`lib/keys/registry.ts`),
   so existing consumers are unchanged — they still just receive a `Command[]`. It rides the
   existing localStorage + server (ui-prefs) sync, i.e. cross-device (it is **not** in
   `DEVICE_LOCAL` alongside `theme` — a key layout is a preference about how you work, not
   something environment-dependent).

7. **[P5] A terminal-input-priority toggle** (`Settings.terminalPriority`, off by default). When
   on, every app shortcut passes through to xterm while the terminal has focus and **only the
   Leader** stays live (tmux's prefix approach). Everything remains reachable from the Leader via
   which-key or the palette. The Leader itself can also be rebound, so disabling it gives you a
   "completely pure terminal" if you want one. Keeping exactly one key is the **guaranteed
   escape** from being trapped in the terminal.

## Options rejected

- **An editor-style system of global accelerators (many Ctrl combinations)**: collides with
  xterm across the board, so the more you use the terminal the more shortcuts die. A Leader
  approach folds the collision surface down to one key.
- **onKeyDown on each component instead of capture**: cannot pierce xterm's grab and is powerless
  while the terminal has focus.
- **Making even the leader sequences fully rebindable**: the UI and collision detection across
  the tree would be excessive. Direct keys plus reserved keys cover most of the practical value,
  and the sequences carry meaning (p = pane, and so on), so fixing them is right (decision 6).
- **Defaulting to terminal priority with no escape key at all**: once you enter the terminal you
  cannot leave with the keyboard, which is an accident waiting to happen. The default keeps the
  Leader, and a completely pure terminal stays an explicit opt-in (disabling the Leader)
  (decision 7).

## Impact

- The dispatcher and each overlay go through `effectiveCommands()` / `boundChord()`, so a
  rebinding takes effect immediately.
- A "keys" tab is added to settings (`features/settings/KeysTab.tsx`): rebinding rows, key
  recording (capture), collision warnings and the terminal-priority toggle. The settings modal is
  an overlay, so the dispatcher is inactive and the recording capture listener can safely take
  every key (only things like Ctrl+P's print are suppressed with preventDefault).
- Wording can later be routed through lib/i18n from [0016](0016-i18n.md) — the registry is
  already centralised, so it is a single place.
