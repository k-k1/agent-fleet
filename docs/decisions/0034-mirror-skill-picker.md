# 0034. The mirror's skill picker — a per-session API plus in-composer completion

English | [日本語](0034-mirror-skill-picker.ja.md)

- Status: **adopted and implemented** (2026-07-28). The design and the implementation record are in [50-mirror-skill-picker.md](../log/50-mirror-skill-picker.md).
- See also: [0017](0017-keyboard-system.md) (the keyboard system) / [0015](0015-agent-managed-driver.md) (the driver abstraction — the premise that turns pass through)

## Context

There was no route from the mirror view to invoke the skills (`.claude/skills`) and custom commands
(`.claude/commands`) defined for a session; you either typed the full name or relied on the TUI's
completion on the terminal side. The launch modal already has per-repo template collection
(`repo_prompts.go`), but it is fixed to repo name → `~/repos/<name>` and can see neither the real
path of a worktree session nor the user level. The requirement was "completable by keyboard alone,
by mouse alone, or by tap alone".

## Decision

### 1. The listing is a new per-session API (not a reuse of the per-repo one)

`GET /sessions/{name}/skills`. It walks the worktree's real path via
`session.ReadMeta(name).Dir` and the user level via `claude.ConfigDir()`. Reusing repo_prompts was
rejected because it cannot express a worktree (`repo@branch`) or the user level; only the
frontmatter parser (`splitFrontmatter`) is shared. `argument-hint` is newly interpreted, and
`user-invocable: false` is excluded. Duplicates are resolved by slash name, first-wins with
project > user and skill > command. Kinds other than claude return **empty rather than an error**
(forward compatible — cursor's and kiro's ACP `available_commands` can be returned in the same shape
later). No cache (walking each time is cheap enough, and it responds immediately to writing a
SKILL.md mid-session).

### 2. The UI is two routes: inline completion in the composer, plus a permanent "/" button

- For keyboard users: typing `/` at the start of the input opens it, typing filters, ↑/↓ then
  Enter/Tab.
- For mouse/tap users: a "/" button to the left of the composer (the same size as, and next to, the
  attach button).
- Confirming **only inserts** (`/name ␣` plus the existing draft preserved as arguments) and does not
  send — you check the arguments before sending. The one exception is modifier+click to send
  immediately (the same idiom as reply suggestions).
- The selection list uses the CommandPalette's sel-index approach (focus stays on the textarea). On
  a touch confirmation, focus is not stolen (the existing GBoard convention).
- The kind gate is `AgentCaps.slashSkills` (no ternaries on kind — centralised in the registry).

### 3. The send path is untouched

The slash string passes through the existing path on both tui and managed, and turn suppression
(`slashCmdRe`) keeps its existing behaviour. This feature adds only recognition and input assistance.

## Options rejected

- **Reusing `repoPromptTemplates(sessionMeta.repo)`**: a worktree name (`repo@branch`) does not pass
  `resolveRepoDir`'s repo-name validation, and there is no user level or argument-hint.
- **Sending immediately on selection**: it misfires on skills with arguments (those with an
  argument-hint). Insertion only, with immediate sending left to an explicit modifier action.
- **The focus-moving ring used by reply suggestions**: on a phone, blurring the textarea drops the
  soft keyboard. Switched to the sel-index approach.
- **Showing an empty list when triggered by typing too**: it would cover up hand-typed commands that
  are not enumerated, like `/plan`. Hidden when there are no matches (only the button route shows
  "there are none").
- **A TTL cache on the agent side**: the walk is cheap, and freshness (a SKILL.md added mid-session)
  is worth more.

## Addendum (same day, v2): making it cross-agent

Following the user's request to "use it with things other than Claude", codex / opencode / cursor
were added. Every source and invocation form was measured live before implementing (docs/50 §7 is
the evidence).

### 4. The API returns the invocation string as `invoke` (the UI does not know about kinds)

The invocation form differs by kind (claude/opencode/cursor use `/name`; codex uses a `$name`
mention). Rather than bring a kind branch into the UI, the contract has the Agent return `invoke`
(the exact string to insert). Only the trigger character for opening on typing lives in the registry
(`skillTrigger`).

### 5. For cursor, the CLI's advertised list is canonical (not an FS walk)

ACP's `available_commands_update` streams a complete list of built-in skills plus global plus
project (measured). The driver's onNotify (which previously discarded it) publishes into an
in-memory store (`agents.PublishCommands`) that the handler reads. **The GET does not Resume the
driver** (that would have the side effect of waking the runtime) — if nothing has arrived, it falls
back to the project FS.

### 6. Do not stand up unverified paths or kinds

- Firing /command through opencode's managed (server API) path is unverified → the mirror side is
  gated with `slashSkillsManaged: false` (shown for TUI sessions only).
- kiro's advertised `prompts` (user-defined) had zero real entries so the shape is unverified, and
  the built-ins alone are noise → deferred. copilot and agy have unconfirmed or suspect mechanisms →
  deferred.

## Addendum (same day, v3; **withdrawn in v4**): a skill bridge = marker-tagged copy sync at startup

Following the user's requests to "bridge at run time without placing links", "have the skills in
both folders available from either agent" and "no symlinks", a **marker-tagged bidirectional copy
sync** between `.claude/skills` and `.codex/skills` was implemented (`internal/skillbridge`, using
info/exclude so `status` stays clean). But it **places real files in the project directory even
though git cannot see them**, and the user objected — "does that dirty the project?" → withdrawn
(the code was deleted; the implementation's key points and safety rules remain in this section's git
history). The lesson: "does not dirty status" and "does not dirty the directory" are different
requirements.

## Addendum (same day, v4): cross-skill injection — a bridge that touches no files (adopted)

The user's proposal — "list other agents' skills as candidates in the picker too, and when one is
selected, turn it into a prompt and inject that" — was adopted as stated (docs/50 §8).

### 7. The bridge is prompt injection, not file operations

- The API mixes SKILL.md files from the other conventions (`.claude/skills` / `.codex/skills` /
  `.agents/skills`) into the list as **foreign entries** (`path` plus `origin`, with `invoke`
  empty), and on selection the Console inserts "read `{path}` and follow that skill's instructions".
- Compared with the rejected options: symlinks (rejected by the user); copy sync (v3 — withdrawn for
  dirtying the directory); codex's `skills/extraRoots/set` RPC (writes nothing, but is codex-only and
  one-directional, has no equivalent in claude, and may not reach the TUI driver). Injection was the
  only option that satisfies **no writes, both directions, every kind, every driver** at once.
- A by-product: the picker works even for kiro / copilot / agy, which have no native skill mechanism
  (foreign only, via the button). opencode's managed gate now applies only to native entries, so
  foreign entries work under managed opencode too.
- The limit: unlike a native invocation it does not go through the CLI's skill runtime (claude's
  `context: fork`, allowed-tools restrictions and so on), and interpreting the body is left to the
  model. Where accuracy matters, put the skill on the native convention's side.

## The asymmetry that remains (known; out of scope for this ADR)

- The managed path calls `markSessionWorking` even for a slash (there is no managed version of the
  tui's guard) — a pre-existing asymmetry. Fixing it is a separate task.
- kiro's advertised list (`_kiro.dev/commands/available`) is still discarded. Taking it in is just a
  matter of routing it into the same publish path as cursor (docs/50 §7.4).
- The bridge covers only the two conventions inside the repo (`.agents/skills` and the user level are
  out of scope — codex reads `.agents/skills` natively so it is unnecessary, and that claude cannot
  see it is known).

## Addendum (2026-07-30, v5): stay open passively while arguments are typed; modifier-click send removed

Two changes following the user's observations — "typing a space to enter an argument makes the
candidates vanish; I want it to stay up so I can refer to the arguments" and "I do not need
Ctrl+click to send immediately" (docs/50 §2.2).

### 8. Split "completion" and "argument hint" into two modes of one list

- `slashTokenAt` returns a token with `args=true` while the caret is to the right of the first token
  as well (previously it returned null, i.e. closed). The display narrows to the single exact name
  match (`exactSkills`) — the purpose is "write the arguments while looking at the `argument-hint` of
  the command you have finished typing", so other partial matches are noise. Zero matches (plain prose
  that happens to start with `/`) renders nothing.
- In passive display the key point is **not to hijack the keyboard** (Enter = send, ↑/↓ = caret and
  history). Keeping the list alive while hijacking would be a fatal regression: you could not press
  Enter to send after writing the arguments. The selection highlight (sel) is not shown either, and
  only clicking works — so it does not suggest "Enter would confirm this" either.
- Right after an insertion the caret is to the right of `invoke`'s trailing space, i.e. at the
  argument position, so it enters this passive display the moment you select (selecting, then writing
  arguments while looking at the hint, is one continuous motion).

### 9. Modifier+click to send immediately is removed

The exception kept in v1 as "the same idiom as reply suggestions" (§2) is withdrawn. Users did not
use it, leaving only the risk of misfiring on a skill with arguments. Now that §8 has made writing
arguments the main route, the picker is consistently **insertion only**.
