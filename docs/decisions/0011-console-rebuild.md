# 0011. Console rebuild — a parallel entry point, zustand, and a freeze on the old side

English | [日本語](0011-console-rebuild.ja.md)

- Status: decided (2026-07-07)
- See also: [22-console-rebuild.md](../log/22-console-rebuild.md) (the design proper) / [0004-vanilla-to-react.md](0004-vanilla-to-react.md) (the previous rebuild)

## Context

Even after moving to React (ADR 0004), features kept being bolted on (split panes, SCM, the
viewers, assistant chat, the memo queue, SSM, admin, …) and `console/src` swelled to about
31.5k lines. A single `AppContext` (110+ keys, 31 consumers, coordination through `bump*()`
counters), the triple coupling of pane↔xterm↔history by paneId, and one 8.8k-line CSS file had
solidified into "a structure that absorbs every extension" — and we judged that refactoring
would not undo it. We do a rebuild that preserves feature parity (same framework: React + Vite +
TS; no backend changes).

## Decision

1. **Migration by parallel entry point.** Using multi-entry in the same Vite project, `next.html`
   (the new one) lives in the same dist as `index.html` (the current one), so old and new can be
   compared side by side against the real backend. When it is finished they are swapped and the
   old code is deleted. Zero CP changes; day-to-day operation is not disturbed. A big bang
   (replacing a whole separate directory at once) is too risky with today's zero tests, and
   in-place stepwise replacement makes side-by-side comparison impossible.
2. **State management = zustand.** The single Context is split into stores by domain
   (tenant/workspace/sessions/layout/dialogs/settings), with selector subscriptions controlling
   re-renders. Coordinating with things outside React (the term layer) is straightforward, and
   ref mirrors and wired-once flags all disappear. Maintenance is cheaper than a hand-rolled
   mini store (one ~1KB runtime dependency is acceptable).
3. **New features on the old Console are frozen for the duration of the rebuild** (bug fixes
   only). New features go to the new side once that area has been ported. This zeroes the cost
   of double implementation and gets us finished fastest.

## Consequences

- The design, the phase plan and the feature parity checklist are collected in docs/22. P1
  (terminal + layout core) goes first, so the hardest part is dealt with first.
- The xterm internals in `term.ts`, the `api.ts` core, the theme machinery and the
  flat-absolute pane strategy are moved rather than rewritten.
- vitest is introduced, giving the pure logic (layout computation, the transcript parser, …) its
  first automated tests. Visual verification stays what it was: the user looking at a browser.
- localStorage and ui-prefs keys stay as they are, so users' settings and layouts carry over at
  the swap.
