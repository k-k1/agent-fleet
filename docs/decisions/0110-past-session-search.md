# 0110. Past-session search: an FTS5 index inside the workspace, over conversation text only, offered to every kind through the af MCP

English | [日本語](0110-past-session-search.ja.md)

- Status: **accepted** (2026-10-04). The Agent side (index, REST, the `search_sessions` tool) is
  built. The Console's search box and the Settings switch are the second step of #1558. The
  figures below were measured on 2026-10-04 on one workspace (739 sessions) with a probe that
  ran the real pass against a scratch index file.
- Tracking: #1558
- Related: [0009](0009-transcript-paging.md) (left full-text search out of scope) /
  [0108](0108-af-owned-agent-memory.md) (agent memory; its P3 ties in with this) /
  [0073](0073-session-spawned-sessions.md) decision 4 (another session's raw output is for its
  parent only) / [0041](0041-cross-session-messaging.md) (peer messaging and its off-by-default
  switch) / [0097](0097-session-retention.md) (stopped sessions are archived, never deleted) /
  [0101](0101-session-delete-via-trash.md) (deletion goes through the trash)

## Context

"How did we solve this before?" has no answer in Agent Fleet today. There is file-name search
and, in the mirror, a reverse search over the current session's own prompts. Nothing searches
what was said across sessions, and each CLI keeps its conversations in its own place and
format (claude jsonl, codex rollouts, opencode's SQLite, cursor, kiro, copilot and agy files,
AF's own muse and lcpp stores — `docs/build/04-agent.md` §4.7).

Three facts shape the design.

- **The normalisation already exists.** Every kind that has a transcript is parsed into
  `transcript.Turn` for the mirror, and `sessionx.UsageTurns` returns the whole conversation for
  any kind, claude included, for the usage ledger.
- **Transcripts never leave the workspace** (`docs/build/06-data.md` §6.2: "Transcript bodies
  never leave the owner's workspace"). The CP holds session metadata only.
- **SQLite FTS5 is already linked.** The Agent depends on `modernc.org/sqlite` (SQLite 3.53.2),
  whose FTS5 offers the unicode61 and trigram tokenizers. Measured: a trigram table returns no
  row for 「認証」 in 「前回の認証エラーを直した」, because trigram cannot match a query shorter
  than three characters. Two-character words are the common case in Japanese.

The size of the problem, measured on the workspace above: 739 session metas, 738 with a
transcript, 17,696 indexable turns, 15.3 MB of conversation text.

## Decisions

1. **The index lives in the workspace, as a cache.** One SQLite file,
   `~/.local/state/agent-fleet/session-search/index.db` (`0600`, under the Agent state
   directory that the file browser hides). It is derived from transcripts that stay where they
   are, so deleting it is always safe: a schema change drops it and the next pass rebuilds it.
   It survives stop and recreate, like the transcripts. It is lost, and rebuilt, wherever the
   Agent state directory is lost (clean home, a lost volume).
2. **One reader for every kind.** The pass reads `sessionx.UsageTurns(meta)`, the same
   conversation the usage ledger folds, so there is no per-kind indexer to keep in step with
   the parsers. Sessions without a transcript (shell, ssm) are skipped. An empty read never
   erases rows already indexed: a stopped Managed cursor session reads as empty although the
   conversation happened. Only the trash removes rows (decision 7).
3. **Only the conversation is indexed.** Kept: text parts, plans, and asked questions with
   their answers. Left out:
   - tool calls and their output. They are the bulk of a transcript, mostly file contents and
     command output, and the likeliest place for a secret to sit.
   - thinking, subagent sidechains and delegation prompts. This is the agent talking to itself
     or to a helper, not to the person.
   - claude's compaction summaries, which restate turns that are already indexed.

   One turn is capped at 32 KiB, so a pasted log does not dominate the index.
4. **Japanese is cut into bigrams in Go, then tokenised by unicode61.** A run of Han, kana or
   Hangul (and the 「ー」 and 「々」 marks inside words) becomes overlapping bigrams, and a
   one-character run becomes a unigram. Fullwidth and halfwidth forms are width-folded first.
   The query goes through the same function: each whitespace-separated term becomes one quoted
   phrase of its tokens, and all terms must occur. A lone CJK character is asked for as a
   prefix, so it matches every bigram it starts. User input never reaches FTS5 syntax unquoted.
5. **Incremental, with no timer of its own.** Following `usage_fold.go`, a pass runs only when a
   search arrives, at most once a minute and in the background. The search answers from the
   index as it stands, with `indexing: true` and `indexed`/`total` counts. The pass takes
   sessions one at a time, so one transcript is in memory at once and a search waits behind at
   most one session's write.
   - A session stopped at the same `StoppedAt` as when it was last indexed is not re-read. A
     resume clears `StoppedAt` and the next stop stamps a new one, so this test is exact.
   - A transcript that grew rewrites only its last indexed row (it may still have been
     streaming) and what follows. Anything else, such as a shorter transcript or a claude sid
     that moved to a sibling jsonl, replaces the session's rows.

   Measured on the 739-session workspace:

   | Measurement | Result |
   |---|---|
   | First pass | 83 s |
   | Peak Go heap during the first pass | 210 MB |
   | Second pass | 8 s (it re-reads only the sessions not stamped stopped) |
   | Index file | 31.7 MB |
   | One search | 40–110 ms |
6. **Every kind gets it through the af MCP; a switch governs sessions, and it defaults on.**
   - The tool is `search_sessions`, one tool with two modes. With `query` it returns hits
     (session, `idx`, snippet). With `session` and `idx` it returns the indexed turns around a
     hit. These go to `GET /session-search` and `GET /session-search/turns` on the Agent.
   - The switch is the ui-pref `sessionSearch`. The `--session-search` launch argument carries
     it, and toggling it rewrites every CLI's MCP config, as the other switches do.
   - The Agent re-checks the switch on every call that carries a session's attribution (`from`,
     filled by the af server from its own binding, as for the peer peek). It logs each such call
     without the query text, because what a session searched for can itself be sensitive.
   - The Console's own search is not governed by the switch.

   Why on by default, when peer messaging (0041) defaults off and another session's raw output
   is for its parent only (0073 decision 4):
   - Those rules guard writing into other sessions and steering them, which spreads a poisoned
     instruction. This tool only reads.
   - What it reads is this user's own transcripts, which a session's shell can already open as
     the same uid.

   The reach is every session in this workspace — running, stopped and archived, the caller's
   own included — and never another workspace.
7. **Rows live exactly as long as the session's meta.**
   - Moving a session to the trash removes its rows right after the meta is removed. The trash's
     gz bundle is then the only copy, and a restore brings the meta back, so the next pass
     re-indexes it.
   - Every answer is checked against the current metas, so a session deleted since the last
     pass is never found. The pass also prunes rows whose meta is gone.
   - An archived session stays searchable. Archiving is reversible (0097), and finding old work
     is what the search is for.
8. **Ranking.** Hits are ordered by FTS5's bm25, with two adjustments:
   - Turns from scheduled runs (`origin=schedule`) weigh half. They repeat the same prompt and
     would crowd out the conversation where a person worked the problem.
   - A recency factor halves the weight every 180 days. It breaks ties; it does not filter.

   At most three hits per session are returned (adjustable), so one long conversation does not
   hide the other sessions that touched the topic.

## Rejected

- **The CP's Postgres (`tsvector`).** It moves conversation text out of the workspace against
  `06-data.md` §6.2. It needs the SQLite dialect too (the single-CP profile), and Postgres has no
  Japanese parser without an extension. It also stops working while a workspace is stopped.
- **The trigram tokenizer.** Measured to miss every two-character query.
- **Indexing tool output.** It would multiply the index and the chance of indexing a secret, for
  hits that are mostly file contents the repository already holds.
- **Summarising hits with an LLM.** It costs a model call per search, and the caller is already
  an LLM that can read the turns. hermes, whose design prompted #1558, dropped it too.
- **A per-kind search through each CLI's own store.** N implementations to keep in step with N
  formats, and no common ranking across their answers.
- **A resident timer or a file watcher.** The host is shared and memory-constrained, and a sweep
  would re-read transcripts nobody is searching.
- **Riding on the peer-messaging switch.** It would mix a read-only capability into a switch
  whose meaning is "sessions may write into each other", and leave search off for most fleets.
- **Off by default.** A tool that most sessions never see is not used. Decision 6 gives the
  reasons the read does not need the opt-in the write paths do.

## Consequences

- Any session can ask what an earlier session, of any kind, concluded, without the user relaying
  it.
- The workspace holds a second copy of conversation text, about the size of the text itself
  (31.7 MB for 15.3 MB of text here). It sits on the same storage as the transcripts, under the
  same uid, and follows the meta's life (decision 7).
- The first search after an upgrade or a clean home answers from a partial index while the first
  pass runs. The answer says so (`indexing`, `indexed` < `total`).
- An agent's search result is past context, not an instruction; the tool description says to
  check any file, command or flag a hit names before relying on it.

## Open questions

1. How often agents call `search_sessions` unprompted, and whether the description needs to say
   more or less about when to.
2. Whether a lone CJK character that ends a run (matched only as a prefix of the bigram it
   starts) is missed often enough to index unigrams as well.

## Addendum (2026-10-08) — a hit older than the mirror's window (#1663)

The jump rides the scroll mark, which restores only a turn that is already mounted, and the
mirror mounts the last 400 positions. A hit further back now pages older history in first
(`reachTurn.ts`): a turn's idx and the window's `firstLine` are the same unit, so one
`before=firstLine&limit=N` request covers the gap, and the Agent API is unchanged. It is bounded —
at most 8,000 positions and 4 requests per jump, refused before any request beyond that, with a
notice ("too far back; scroll up") instead of a silent landing at the end — and it yields to the
reader: a wheel, touch, key or click while the pages load drops the jump.
