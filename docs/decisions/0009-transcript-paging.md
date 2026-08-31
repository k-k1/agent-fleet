# 0009 — Load the chat transcript as a tail window, and page backwards

English | [日本語](0009-transcript-paging.ja.md)

- Status: adopted (introduced in stages; P1 started, P2/P3 follow)
- Date: 2026-07-03
- See also: `workspace/agent/session_transcript.go` (`handleSessionMessages` / `collectTurns`), `workspace/agent/session_io.go` (`transcriptRead`), `console/src/views/MirrorView.tsx`

## Context

The first render of the chat (MirrorView) is `GET …/messages?since=0`, and the server does
`transcriptRead` (`os.ReadFile` of the whole file, then split) → `collectTurns` (`parseTurn`,
i.e. a JSON Unmarshal, on every line from `since` to the end) → return every turn at once.
Live updates after that use `since=cursor` and only take the tail delta, so they are cheap.
**The expensive part is the single first call**, which is "all the I/O + parse every line +
transfer every turn".

Measured (a Claude Code transcript on the host): up to **19.3 MB / 2,090 lines**, with a longest
single line of **≈391 KB** (images, huge tool_results), and **14 lines over 5 MB**. On a bloated
conversation the first render is slow.

Cost breakdown, in order of dominance:

1. **JSON parsing** (`Unmarshal` on every line, some of them hundreds of KB) — dominant.
2. **The transfer payload** (structured JSON for every turn).
3. **File I/O** (`ReadFile` of the whole thing; secondary once the OS cache is warm).
4. Client rendering (Markdown/highlighting for every turn).

Items 1 and 2, the dominant ones, **disappear simply by narrowing what is returned to the tail —
no reverse reading required**.

Incidentally: `collectTurns`'s 1 MiB budget accumulates **backwards** from `since` and then cuts
off, so on a huge transcript it tends to **drop the newest turns** (a latent bug).

## Decision

Fold "read backwards from the end" into **one coordinate system, the existing
`cursor` = an absolute line number from the start** (append-only, therefore stable). Three
accesses in the same coordinates:

- **① first render (tail)**: `?tail=1&limit=M` → the turns in the tail window, plus `firstLine`,
  `cursor=len` and `hasMore`
- **② backwards (before)**: `?before=<firstLine>&limit=M` → the turns before firstLine, plus a
  new `firstLine` and `hasMore` (the client prepends)
- **③ live (since)**: `?since=<cursor>` → what is new at the end (**unchanged, untouched**)

As long as the first render returns `cursor=len`, ③ is invariant. ② uses a separate parameter so
it does not pollute ③'s `since` space. It can be added with **zero breaking changes**, and it
fits in the same frame as the `reset` mechanism and the fork preview.

### Stages

| Stage | What | Effect | Effort/risk |
| --- | --- | --- | --- |
| **P1** | Server: parse only the tail window (`collectTurns` over `[lo,hi)`). Fix the budget to be **tail-first**. Add `before`/`tail`/`firstLine`/`hasMore` backwards-compatibly. Lighten `collectTasks` and `collectAnswers` (prefilter / stay within the window). | Cuts the dominant parse and transfer costs dramatically. | small / low (self-contained within the existing API, backwards compatible) |
| **P2** | Client: `tail` on first render, `before` paging, prepend with scroll-position preservation, a "load more" control. | The past loads only when asked for. The first render is pinned to the tail and light. | medium / medium (verifying position preservation) |
| **P3** | Seek and read backwards from EOF (with chunk growth across long lines). | Removes the remaining I/O. Insurance for when 20 MB becomes normal. | medium / high (reverse-read edge cases, tests) |
| alternative | Cache parse results (`sid`+`mtime`, re-parsing only the delta). Orthogonal to P1. | Re-opening is fast. | small / low |

**Recommendation: implement P1 + P2; judge P3 after measuring.** The dominant costs are parsing
and transfer, and the tail window removes them without reading backwards. I/O (P3) is secondary.

## The hard parts, and how they are handled

- **A turn is cut at the window edge**: going back with ② reconnects it. `groupTurns` is made
  idempotent under prepend.
- **Answer resolution depends on the range**: narrowing `collectAnswers` to the window can leave
  an answer that spans the edge empty. Questions and answers sit close together, so this is
  tolerable in practice. Mitigations: a wider window, or lazy resolution by line ID.
- **Preserving scroll position (the crux of the UX)**: add the difference in `scrollHeight`
  before and after the prepend to `scrollTop` to hold the viewpoint. Independent of the existing
  stick-to-bottom.
- **Compaction / file replacement**: absolute line numbers are invalidated → the existing `reset`
  re-reads from the end.
- **Summaries over the whole history (to-dos, token trend, context)**: restricting to the tail
  window would starve them. So the design is deliberately asymmetric — keep the heavy `parseTurn`
  inside the window, but **keep the cheap full-file scan (extracting Task lines and usage
  lines)** (`collectTasks` is lightened with a string prefilter).

## Consequences

- The first render scales with bloat (it is proportional to what is returned = the tail window).
- `collectTurns`'s budget now runs **tail-first**, which also removes the latent bug of dropping
  the newest turns.
- Live deltas, reset and the fork preview are unchanged. An old client (which does not send
  `tail`) still receives the whole history as before (the P1 server can be deployed on its own,
  backwards compatibly).
- Non-goals: the jsonl format and the storage method do not change. Full-text search and virtual
  scrolling are out of range (future work).
