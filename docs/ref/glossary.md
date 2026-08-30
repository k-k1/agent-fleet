# Glossary

English | [日本語](glossary.ja.md)

Audience: everyone, and especially anyone translating between a screen and the code
Source of truth: the Console's own strings for the screen column; the code for the implementation column
Updated: 2026-08

Two columns on purpose. The reader-facing shelves may use only the **screen** word;
[build/](../build/README.md) uses the **implementation** word. Keeping the mapping in
one place is what lets a support conversation and a stack trace be about the same
thing.

| Screen | Implementation | Means |
|---|---|---|
| Workspace | | |
| Working copy | | |
| Session | | |
| Execution method — Managed | | |
| Execution method — Terminal (CLI) | | |
| Mirror | | |
| Pane | | |
| Working set | | |
| Work item | | |
| Handoff | | |
| Fork | | |
| Assistant | | |
| Connection | | |
| Tenant | | |
| Slot | | |
| Deployment | | |

## Words to avoid on the reader-facing shelves

`driver`, `runtime`, `TUI`, `PTY`, `tmux`, `pane` as an implementation term, `kind` —
these are how it is built, not what the reader sees. Use them in
[build/](../build/README.md), and in a user-facing document only when the reader
explicitly asked how the machinery works.

## Status

Axis fixed, cells to be filled in phase P1.
