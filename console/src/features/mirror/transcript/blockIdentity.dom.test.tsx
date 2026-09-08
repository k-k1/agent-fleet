// A block must keep its name when a backward page prepends older rows OF THAT SAME BLOCK.
//
// groupTurns names a block after its first row, and a reply is many jsonl rows, so the edge of the
// window normally falls inside one. Before useStableBlockIds the block the reader was in changed
// idx the moment a page landed — which is a changed React key, i.e. the subtree is destroyed and
// rebuilt, taking TranscriptTurn's fold latch, work boundary and the reader's own open/closed
// choice with it, and taking the scroll anchor's target with it too (it is looked up by that idx).
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source }: { source?: string }) => <div className="markdown">{source}</div>,
}));

import { TranscriptView } from "./TranscriptView.tsx";
import { groupTurns } from "./model.ts";
import { useStableBlockIds } from "./blockIdentity.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import type { Turn } from "./types.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const OWNER: TranscriptCaps = { agentName: "Claude", session: "s1" };

// The component under test is the pair: grouping + the id allocator, exactly as both readers
// assemble it, then TranscriptView keyed on the result.
function View({ turns, session, working, atBottom }: { turns: Turn[]; session: string; working: boolean; atBottom: boolean }) {
  const groups = useStableBlockIds(groupTurns(turns), session);
  return <TranscriptView groups={groups} caps={{ ...OWNER, session }} working={working} autoCollapseWork={atBottom} />;
}
function render(props: { turns: Turn[]; session: string; working: boolean; atBottom: boolean }) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<View {...props} />));
  return host;
}
function rerender(props: { turns: Turn[]; session: string; working: boolean; atBottom: boolean }) {
  act(() => root!.render(<View {...props} />));
  return host!;
}
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

// One reply, written the way a real transcript writes it: the narration, each tool call and the
// answer as separate rows, folded back into one block by groupTurns.
const reply = (start: number): Turn[] => [
  { role: "assistant", idx: start, ts: "2026-09-08T10:00:00Z", parts: [{ kind: "text", text: "調べます" }] },
  { role: "assistant", idx: start + 1, ts: "2026-09-08T10:00:10Z", parts: [{ kind: "tool", tool: "Read", info: "a.ts" }] },
  { role: "assistant", idx: start + 2, ts: "2026-09-08T10:00:20Z", parts: [{ kind: "tool", tool: "Bash", info: "go test" }] },
  { role: "assistant", idx: start + 3, ts: "2026-09-08T10:00:30Z", parts: [{ kind: "text", text: "終わりました。原因は設定です。" }] },
];
const ask = (idx: number): Turn => ({ role: "user", idx, ts: "2026-09-08T10:00:00Z", text: "調べて " + idx });

// The tail window BEGINS mid-reply — the window edge fell inside the block that starts at 200.
const HELD: Turn[] = [...reply(200).slice(2), ask(210), ...reply(211)];
// The page before it ends in the first half of that same block.
const OLDER: Turn[] = [ask(190), ...reply(191), ...reply(200).slice(0, 2)];

const states = (el: HTMLElement) =>
  [...el.querySelectorAll<HTMLElement>(".mirror-turn.assistant")].map((t) => {
    const head = t.querySelector<HTMLButtonElement>(".mt-work-head");
    return (t.dataset.turnIdx ?? "?") + ":" + (head ? (head.getAttribute("aria-expanded") === "true" ? "open" : "closed") : "inline");
  });

describe("a block keeps its identity across a backward page", () => {
  it("keeps the idx the reader's block was rendered under", () => {
    const el = render({ turns: HELD, session: "s1", working: false, atBottom: true });
    expect(states(el)).toEqual(["202:closed", "211:closed"]);
    // …and the reader scrolls up, which is what triggers the page load.
    const after = rerender({ turns: [...OLDER, ...HELD], session: "s1", working: false, atBottom: false });
    // The prepended rows join that block (nothing breaks the run of assistant rows), so its first
    // row is now 191 — and it is STILL rendered as 202, and still closed, because the component was
    // never re-mounted to re-decide that from defaultWorkOpen.
    expect(states(after)).toEqual(["202:closed", "211:closed"]);
  });

  it("keeps a work trace the reader opened open across the page", () => {
    const el = render({ turns: HELD, session: "s1", working: false, atBottom: true });
    act(() => el.querySelectorAll<HTMLButtonElement>(".mt-work-head")[0].click());
    expect(states(el)[0]).toBe("202:open");
    const after = rerender({ turns: [...OLDER, ...HELD], session: "s1", working: false, atBottom: false });
    expect(states(after)[0]).toBe("202:open");
  });

  it("does not carry ids across a session switch (the mirror is not remounted)", () => {
    render({ turns: HELD, session: "s1", working: false, atBottom: true });
    // Another session, whose rows happen to overlap the same numbers. Matching by range across the
    // switch would hand this session the other one's ids.
    const other = rerender({ turns: [...OLDER, ...HELD], session: "s2", working: false, atBottom: true });
    expect(states(other)).toEqual(["191:closed", "211:closed"]);
  });

  it("never hands one id to two blocks when one splits in two", () => {
    const el = render({ turns: [...reply(300), ...reply(304)], session: "s1", working: false, atBottom: true });
    expect(states(el)).toEqual(["300:closed"]); // one block: consecutive assistant rows merge
    // A row that used to be invisible turns out to be a prompt, cutting that block in two. Both
    // halves overlap the block we remembered; only the first may inherit its id — two blocks under
    // one React key would be worse than the re-mount this file exists to prevent.
    const split = rerender({
      turns: [...reply(300), ask(304), ...reply(305)],
      session: "s1",
      working: false,
      atBottom: false,
    });
    const ids = states(split).map((s) => s.split(":")[0]);
    expect(ids).toEqual(["300", "305"]);
    expect(new Set(ids).size).toBe(ids.length);
  });
});
