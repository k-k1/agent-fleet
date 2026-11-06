// The translate affordance in a rendered turn (docs/log/97): when it is offered, what a press
// swaps out, and what it must NOT touch.
//
// The rules under test are the ones that are invisible until they break:
//   - no wiring (the shared view) = no button at all, ever;
//   - an answer the reader can already read is not offered one;
//   - a shown translation replaces the prose but leaves tool traces, code and marks alone;
//   - flipping back to the original never calls the server.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// MarkdownView pulls in all of remark/rehype; the source string is all this file needs to see.
vi.mock("../../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source, markRoot }: { source?: string; markRoot?: string }) => (
    <div className="markdown" data-markroot={markRoot ?? ""}>
      {source}
    </div>
  ),
}));

import { TranscriptView, resetWatchStarts } from "./TranscriptView.tsx";
import { groupTurns } from "./model.ts";
import { useStableBlockIds } from "./blockIdentity.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import type { Turn } from "./types.ts";
import type { TranscriptTranslateWiring } from "../useTranslate.ts";
import { translateHash } from "../translate.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function render(turns: Turn[], caps: TranscriptCaps, working = false) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={caps} working={working} />));
  return host;
}

/** The assistant's prose is the LAST rendered Markdown block: the first belongs to the user's
 *  own prompt, which is never translated. */
const prose = (): HTMLElement => Array.from(host!.querySelectorAll<HTMLElement>(".markdown")).at(-1)!;

afterEach(() => {
  act(() => root?.unmount());
  resetWatchStarts(); // the boundary outlives a mount on purpose; each case is a fresh page
  host?.remove();
  root = null;
  host = null;
});

const ENGLISH = "Done — the tests are green and the branch is pushed.";
// A second English answer, so a test can tell the turn that arrived from the one already there:
// the cache key is the text itself, and two turns with the same prose share one entry.
const ENGLISH2 = "Rebased onto the base branch and pushed again; the pipeline is green.";
const ENGLISH3 = "The release notes are drafted; I left the version bump for you to confirm.";
const JAPANESE = "テストは全部緑で、ブランチも push 済みです。";

const answer = (text: string, withTool = false): Turn[] => [
  { role: "user", text: "やって", idx: 1, anchorId: "u1", ts: "2026-09-13T10:00:00Z" },
  {
    role: "assistant",
    idx: 2,
    anchorId: "a1",
    ts: "2026-09-13T10:01:00Z",
    parts: withTool
      ? [{ kind: "tool", tool: "Bash", info: "go test", output: "ok" }, { kind: "text", text }]
      : [{ kind: "text", text }],
  },
];

/** A wiring whose state the test drives by hand, so a press is observable without a server.
 *  `autoPress` latches exactly as useTranslate's does — once per turn, for good — because that
 *  is what keeps a once-a-second re-render from pressing again behind the reader. */
function wiring(
  entries: Record<string, string> = {},
  auto = false,
): TranscriptTranslateWiring & { calls: string[][]; autoKeys: string[] } {
  const held = new Map(Object.entries(entries));
  const shown = new Set<string>();
  const calls: string[][] = [];
  const autoKeys: string[] = [];
  const fired = new Set<string>();
  const self: TranscriptTranslateWiring & { calls: string[][]; autoKeys: string[] } = {
    lang: "ja",
    calls,
    autoKeys,
    auto,
    autoPress: (key: string, texts: string[]) => {
      autoKeys.push(key);
      if (fired.has(key)) return;
      fired.add(key);
      self.toggle(key, texts);
    },
    get: (text: string) => held.get(translateHash(text)),
    shown: (key: string) => shown.has(key),
    busy: () => false,
    error: () => undefined,
    toggle: (key: string, texts: string[]) => {
      if (shown.has(key)) {
        shown.delete(key);
        return;
      }
      if (!texts.every((t) => held.has(translateHash(t)))) calls.push(texts);
      for (const t of texts) if (!held.has(translateHash(t))) held.set(translateHash(t), "訳: " + t);
      shown.add(key);
    },
  };
  return self;
}

const capsWith = (tx?: TranscriptTranslateWiring): TranscriptCaps => ({
  agentName: "Claude",
  session: "s1",
  translate: tx,
});

describe("the mirror's translate button", () => {
  it("is offered on an answer the reader cannot read", () => {
    const el = render(answer(ENGLISH), capsWith(wiring()));
    expect(el.querySelector(".mt-translate")).not.toBeNull();
  });

  it("is not offered when the answer is already in the reader's language", () => {
    const el = render(answer(JAPANESE), capsWith(wiring()));
    expect(el.querySelector(".mt-translate")).toBeNull();
  });

  it("is not offered at all without the wiring (a shared session is read as written)", () => {
    const el = render(answer(ENGLISH), capsWith(undefined));
    expect(el.querySelector(".mt-translate")).toBeNull();
  });

  it("is not offered while the turn is still streaming", () => {
    // working=true hands the live exchange an unfolded work trace; translating a half-written
    // answer spends tokens on text that is about to change.
    const el = render(answer(ENGLISH), capsWith(wiring()), true);
    expect(el.querySelector(".mt-translate")).toBeNull();
  });

  it("swaps the prose for the translation on a press, and back on the next one", () => {
    const tx = wiring();
    const caps = capsWith(tx);
    const el = render(answer(ENGLISH), caps);
    expect(prose().textContent).toBe(ENGLISH);

    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    // The wiring the mirror supplies re-renders through caps; here the test re-renders itself.
    act(() => root!.render(<TranscriptView groups={groupTurns(answer(ENGLISH))} caps={{ ...caps }} />));
    expect(prose().textContent).toBe("訳: " + ENGLISH);
    expect(tx.calls).toHaveLength(1);

    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    act(() => root!.render(<TranscriptView groups={groupTurns(answer(ENGLISH))} caps={{ ...caps }} />));
    expect(prose().textContent).toBe(ENGLISH);
    // Going back to the original is a display flip, never a request.
    expect(tx.calls).toHaveLength(1);
  });

  it("shows a held translation without asking the server again", () => {
    const tx = wiring({ [translateHash(ENGLISH)]: "保持していた訳" });
    const caps = capsWith(tx);
    const el = render(answer(ENGLISH), caps);
    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    act(() => root!.render(<TranscriptView groups={groupTurns(answer(ENGLISH))} caps={{ ...caps }} />));
    expect(prose().textContent).toBe("保持していた訳");
    expect(tx.calls).toHaveLength(0);
  });

  it("drops the mark root off translated prose", () => {
    const tx = wiring();
    const caps = { ...capsWith(tx), marks: { byRoot: new Map(), authorSlot: () => 0 } as never };
    const el = render(answer(ENGLISH), caps);
    expect(prose().dataset.markroot).not.toBe("");

    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    act(() => root!.render(<TranscriptView groups={groupTurns(answer(ENGLISH))} caps={{ ...caps }} />));
    expect(prose().textContent).toBe("訳: " + ENGLISH);
    // A mark anchors by quoted text (docs/log/69 §69.3), so it cannot be painted over a
    // translation: it would underline whatever happened to match.
    expect(prose().dataset.markroot).toBe("");
  });

  it("anchors marks on the final answer of a tool-using turn", () => {
    // Regression: a work split hands renderAssistantParts a SLICE, and foldParts numbers from 0
    // inside it, while turn.origins is indexed by the position in the whole turn. Without the
    // offset the answer took the first part's root — a tool's, i.e. "" — and the paragraph
    // readers most want to mark could not be marked at all.
    const caps = { ...capsWith(undefined), marks: { byRoot: new Map(), authorSlot: () => 0 } as never };
    render(answer(ENGLISH, true), caps);
    expect(prose().dataset.markroot).toBe("a1#1");
  });

  it("translates the prose of a tool-using turn and leaves the work trace alone", () => {
    const tx = wiring();
    const caps = capsWith(tx);
    const el = render(answer(ENGLISH, true), caps);
    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    act(() => root!.render(<TranscriptView groups={groupTurns(answer(ENGLISH, true))} caps={{ ...caps }} />));
    expect(prose().textContent).toBe("訳: " + ENGLISH);
    // Only prose is ever sent: the trace is the agent's own output, and a translated command
    // line is not something a reader can run.
    expect(tx.calls).toEqual([[ENGLISH]]);
    expect(el.textContent).toContain("go test");
  });

  it("does not send the folded work process's intermediate replies to translate", () => {
    // Regression: a long-running turn can hold dozens of short intermediate replies between
    // tool calls (the "作業過程 ... 途中応答31件" fold). translatableTexts used to see the whole
    // turn, so those replies rode along with the final answer and could alone push a short
    // answer over the per-request part/byte caps (session_translate.go), failing translation for
    // an answer that on its own would be well under them.
    const intermediate1 = "Checking the test output now.";
    const intermediate2 = "That looks correct, continuing.";
    const turns: Turn[] = [
      { role: "user", text: "やって", idx: 1, anchorId: "u1", ts: "2026-09-13T10:00:00Z" },
      {
        role: "assistant",
        idx: 2,
        anchorId: "a1",
        ts: "2026-09-13T10:01:00Z",
        parts: [
          { kind: "tool", tool: "Bash", info: "go test ./...", output: "ok" },
          { kind: "text", text: intermediate1 },
          { kind: "tool", tool: "Read", info: "main.go", output: "..." },
          { kind: "text", text: intermediate2 },
          { kind: "tool", tool: "Bash", info: "go build ./...", output: "ok" },
          { kind: "text", text: ENGLISH },
        ],
      },
    ];
    const tx = wiring();
    const caps = capsWith(tx);
    const el = render(turns, caps, false);
    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={{ ...caps }} />));
    expect(prose().textContent).toBe("訳: " + ENGLISH);
    expect(tx.calls).toEqual([[ENGLISH]]);
    expect(el.textContent).toContain(intermediate1);
    expect(el.textContent).toContain(intermediate2);
  });
});

// Automatic translation (docs/log/97 §97.12) — "the button, pressed for you when the turn ends".
// Every rule here is about WHICH turns qualify, because that is the whole cost of the feature:
// the press itself is the one already under test above.
describe("the mirror's automatic translation", () => {
  // The conversation already on screen when the reader opens the session, and the same
  // conversation one exchange later. The new answer's idx is above everything the first render
  // held, which is what makes it "arrived while watching". The reply the session was opened on
  // (the live exchange) is eligible too, so it is in the reader's language here: these cases are
  // about the turns that come after it.
  const HISTORY: Turn[] = [
    ...answer(ENGLISH),
    { role: "user", text: "続けて", idx: 3, anchorId: "u2", ts: "2026-09-13T10:01:30Z" },
    { role: "assistant", idx: 4, anchorId: "a2", ts: "2026-09-13T10:01:40Z", parts: [{ kind: "text", text: JAPANESE }] },
  ];
  const NEXT: Turn[] = [
    ...HISTORY,
    { role: "user", text: "もう一度", idx: 5, anchorId: "u3", ts: "2026-09-13T10:02:00Z" },
    {
      role: "assistant",
      idx: 6,
      anchorId: "a3",
      ts: "2026-09-13T10:03:00Z",
      parts: [{ kind: "text", text: ENGLISH2 }],
    },
  ];

  /** Re-render into the SAME root: the watch boundary is a ref, so a fresh root would re-arm on
   *  the new turn and hide the very regression these tests exist for. */
  const again = (turns: Turn[], caps: TranscriptCaps, working = false) =>
    act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={{ ...caps }} working={working} />));

  it("presses for a turn that finishes while the reader is watching", () => {
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    render(HISTORY, caps);
    again(NEXT, caps, true); // the new answer is still streaming
    expect(tx.calls).toHaveLength(0);
    again(NEXT, caps, false); // …and completes
    expect(tx.calls).toEqual([[ENGLISH2]]);
    // The press lands in an effect, so the swap shows on the next render — in the mirror that is
    // the state change useTranslate makes; here the test supplies it.
    again(NEXT, caps, false);
    expect(prose().textContent).toBe("訳: " + ENGLISH2);
  });

  it("presses nothing at all when the setting is off", () => {
    const tx = wiring({}, false);
    const caps = capsWith(tx);
    const el = render(HISTORY, caps);
    again(NEXT, caps, false);
    expect(tx.autoKeys).toHaveLength(0);
    expect(tx.calls).toHaveLength(0);
    // The reader's own button is untouched by any of this.
    expect(el.querySelector(".mt-translate")).not.toBeNull();
  });

  it("never presses for the answers that were already on screen when the session was opened", () => {
    // The cost ceiling of the whole feature: without the watch boundary, opening a session with
    // a hundred foreign answers in it would be a hundred model runs, none of them asked for.
    const tx = wiring({}, true);
    const el = render(HISTORY, capsWith(tx));
    expect(tx.autoKeys).toHaveLength(0);
    expect(el.textContent).toContain(ENGLISH);
  });

  it("presses for the reply the session was opened on, finished or not", () => {
    // What a reader opening (or coming back to) a session comes to read is its newest reply;
    // everything above it stays history.
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    const opened = [...answer(ENGLISH), ...NEXT.slice(-2)];
    render(opened, caps, true); // opened mid-turn
    expect(tx.calls).toHaveLength(0);
    again(opened, caps, false);
    expect(tx.calls).toEqual([[ENGLISH2]]);
  });

  it("presses for a reply that finished while the reader was on another tab", () => {
    // A tab switch unmounts the mirror. The boundary armed when the session was first opened
    // has to survive that, or the reply that landed meanwhile counts as history on return.
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    render(HISTORY, caps);
    act(() => root?.unmount());
    // Two exchanges ran meanwhile; only the newer one is the live exchange on return.
    const away: Turn[] = [
      ...NEXT,
      { role: "user", text: "さらに", idx: 7, anchorId: "u4", ts: "2026-09-13T10:04:00Z" },
      { role: "assistant", idx: 8, anchorId: "a4", ts: "2026-09-13T10:05:00Z", parts: [{ kind: "text", text: ENGLISH3 }] },
    ];
    render(away, caps); // back on the tab: a fresh mount, both replies already complete
    expect(tx.calls).toEqual([[ENGLISH2], [ENGLISH3]]);
    // History above the boundary is still never pressed.
    expect(tx.calls.flat()).not.toContain(ENGLISH);
  });

  it("presses once the turn is over even while background work keeps the session busy", () => {
    // A run_in_background shell or a subagent can outlive the turn by hours. Waiting for it
    // meant the reply never got its button, so the automatic press never fired.
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    render(HISTORY, caps);
    act(() =>
      root!.render(<TranscriptView groups={groupTurns(NEXT)} caps={{ ...caps }} working turnWorking={false} />),
    );
    expect(tx.calls).toEqual([[ENGLISH2]]);
  });

  const at = (turns: Turn[], caps: TranscriptCaps, logId?: string) =>
    act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={{ ...caps }} logId={logId} />));
  const reply = (idx: number, text: string): Turn[] => [
    { role: "user", text: "次", idx: idx - 1, anchorId: "u" + idx, ts: "2026-09-13T11:00:00Z" },
    { role: "assistant", idx, anchorId: "a" + idx, ts: "2026-09-13T11:01:00Z", parts: [{ kind: "text", text }] },
  ];

  it("re-arms when the session moves to another jsonl, whatever its row numbers", () => {
    // Rows are numbered per file. A boundary armed on the old file says nothing about the new
    // one, even when the new one's numbers run past it: its history must not look "new".
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    act(() => {
      host = document.createElement("div");
      document.body.append(host);
      root = createRoot(host);
      root.render(<TranscriptView groups={groupTurns(HISTORY)} caps={caps} logId="a.jsonl" />);
    });
    at([...reply(11, ENGLISH), ...reply(13, ENGLISH2)], caps, "b.jsonl");
    expect(tx.calls).toEqual([[ENGLISH2]]);
  });

  it("re-arms when the newest row drops below what was seen (an Agent that names no file)", () => {
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    render(HISTORY, caps); // boundary below the reply at 4
    again([...HISTORY, ...reply(1000, ENGLISH3)], caps); // the log grows: 1000 arrived, pressed
    // A replaced file, numbered from the start again: its old answer at 4 is history; only its
    // live reply at 8 may be pressed.
    again([...reply(2, ENGLISH), ...reply(4, ENGLISH2.replace("green", "red")), ...reply(8, ENGLISH2)], caps);
    expect(tx.calls).toEqual([[ENGLISH3], [ENGLISH2]]);
  });

  it("counts the rows a reply grew by, not just its block's first row", () => {
    // One reply written as many jsonl rows is one block named after its first row. Its last row
    // is what a replaced file has to fall below to be noticed.
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    const grown: Turn[] = [
      ...HISTORY,
      { role: "assistant", idx: 1000, anchorId: "a1000", ts: "2026-09-13T10:02:00Z", parts: [{ kind: "text", text: "そのまま続けます。" }] },
    ];
    render(HISTORY, caps);
    again(grown, caps);
    again([...reply(2, ENGLISH), ...reply(4, ENGLISH2.replace("green", "red")), ...reply(8, ENGLISH2)], caps);
    expect(tx.calls).toEqual([[ENGLISH2]]);
  });

  // Through the mirror's own block identity: a range remembered from the old file overlapped a
  // history block of the new one, which took the old (larger) id and passed the boundary.
  const switchLogs = (oldLog: string, newLog: string, live: number) => {
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    function Mirror({ turns, log }: { turns: Turn[]; log: string }) {
      const groups = useStableBlockIds(groupTurns(turns), "s1", log);
      return <TranscriptView groups={groups} caps={caps} logId={log} />;
    }
    const ja = (idx: number): Turn => ({ role: "assistant", idx, anchorId: "j" + idx, ts: "2026-09-13T09:00:00Z", parts: [{ kind: "text", text: JAPANESE }] });
    const oldTail = [ja(901), ja(1300)];
    const newTail: Turn[] = [
      { role: "assistant", idx: 301, anchorId: "n301", ts: "2026-09-13T13:00:00Z", parts: [{ kind: "text", text: ENGLISH }] },
      // The live reply: one block from row 312 to `live` — its final answer is the newest row.
      { role: "user", text: "次", idx: 311, anchorId: "u311", ts: "2026-09-13T13:01:00Z" },
      { role: "assistant", idx: 312, anchorId: "n312", ts: "2026-09-13T13:02:00Z", parts: [{ kind: "tool", tool: "Bash", info: "go test", output: "ok" }] },
      { role: "assistant", idx: live, anchorId: "n" + live, ts: "2026-09-13T13:05:00Z", parts: [{ kind: "text", text: ENGLISH2 }] },
    ];
    act(() => {
      host = document.createElement("div");
      document.body.append(host);
      root = createRoot(host);
      root.render(<Mirror turns={oldTail} log={oldLog} />);
    });
    act(() => root!.render(<Mirror turns={[ja(100), ...oldTail]} log={oldLog} />)); // older page: 100..1300 is "901"
    act(() => root!.render(<Mirror turns={newTail} log={newLog} />));
    return tx.calls;
  };

  it("lets no block of a new jsonl inherit an id from the old one", () => {
    // The new file runs PAST the old one's rows, so only its name gives the switch away.
    expect(switchLogs("a.jsonl", "b.jsonl", 2000)).toEqual([[ENGLISH2]]);
  });

  it("does the same without a file name, from the newest row stepping back", () => {
    expect(switchLogs("", "", 700)).toEqual([[ENGLISH2]]);
  });

  it("does not re-arm when a follow-up is sent while a landed prompt awaits its reply", () => {
    // The echo folds into the landed prompt's block; judged by the echo, the prompt's row
    // vanished, the newest row seemed to step back, and the re-armed boundary dropped below the
    // answer above — history that then got pressed.
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    const waiting: Turn[] = [...answer(ENGLISH), { role: "user", text: "次", idx: 3, anchorId: "u3", ts: "2026-09-13T10:02:00Z" }];
    render(waiting, caps);
    again([...waiting, { role: "user", text: "それと", idx: 1e9 + 1, ts: "2026-09-13T10:02:30Z", pending: true }], caps);
    expect(tx.calls).toHaveLength(0);
  });

  it("finds the live reply when the window has no landed prompt, only a queued one", () => {
    // A long autonomous stretch pushes the prompt out of the tail window; a queued bubble is not
    // a row of the transcript and must not take the live slot from the real reply.
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    const tail: Turn[] = [
      { role: "assistant", idx: 500, anchorId: "a500", ts: "2026-09-13T12:00:00Z", parts: [{ kind: "text", text: ENGLISH }] },
      { role: "user", text: "then deploy", idx: 2e9, queued: true, ts: "2026-09-13T12:01:00Z" },
    ];
    render(tail, caps);
    expect(tx.calls).toEqual([[ENGLISH]]);
    // An older page loaded on scroll-up is history, not news.
    // (It ends with the prompt that started the stretch, as a real page does.)
    again([...reply(101, ENGLISH2), { role: "user", text: "続けて", idx: 102, anchorId: "u102", ts: "2026-09-13T11:59:00Z" }, ...tail], caps);
    expect(tx.calls).toEqual([[ENGLISH]]);
  });

  it("learns nothing about a session from a render of the previous one", () => {
    // A pane switching sessions renders the outgoing transcript once under the incoming name.
    // Arming on it would make the incoming session's own history look newer than the boundary.
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    const outgoing: Turn[] = [{ role: "user", text: "x", idx: 0, anchorId: "o", ts: "2026-09-13T09:00:00Z" }];
    act(() => {
      host = document.createElement("div");
      document.body.append(host);
      root = createRoot(host);
      root.render(<TranscriptView groups={groupTurns(outgoing)} caps={caps} stale />);
    });
    again(NEXT, caps, false);
    expect(tx.calls).toEqual([[ENGLISH2]]); // armed on NEXT itself: only its live reply
  });

  it("does not press again once the reader has gone back to the original", () => {
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    const el = render(HISTORY, caps);
    again(NEXT, caps, false);
    again(NEXT, caps, false);
    expect(prose().textContent).toBe("訳: " + ENGLISH2);

    act(() => Array.from(el.querySelectorAll<HTMLButtonElement>(".mt-translate")).at(-1)!.click());
    again(NEXT, caps, false);
    // The mirror re-renders about once a second; an automatic press that did not latch would
    // take the original away again on the next poll, for as long as the pane stays open.
    expect(prose().textContent).toBe(ENGLISH2);
    expect(tx.calls).toHaveLength(1);
  });

  it("sends only what the reader can see, not the folded work process", () => {
    // The same trap as the manual press (§97.9), and worth its own case here: the automatic
    // press must be built from the same slice, or a long tool trace fails it silently.
    const withWork: Turn[] = [
      ...HISTORY,
      { role: "user", text: "もう一度", idx: 5, anchorId: "u3", ts: "2026-09-13T10:02:00Z" },
      {
        role: "assistant",
        idx: 6,
        anchorId: "a3",
        ts: "2026-09-13T10:03:00Z",
        parts: [
          { kind: "tool", tool: "Bash", info: "go test ./...", output: "ok" },
          { kind: "text", text: "Checking the test output now." },
          { kind: "tool", tool: "Bash", info: "go build ./...", output: "ok" },
          { kind: "text", text: ENGLISH2 },
        ],
      },
    ];
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    render(HISTORY, caps);
    again(withWork, caps, true);
    again(withWork, caps, false);
    expect(tx.calls).toEqual([[ENGLISH2]]);
  });

  it("leaves an answer the reader can already read alone", () => {
    const japanese: Turn[] = [
      ...HISTORY,
      { role: "user", text: "もう一度", idx: 5, anchorId: "u3", ts: "2026-09-13T10:02:00Z" },
      {
        role: "assistant",
        idx: 6,
        anchorId: "a3",
        ts: "2026-09-13T10:03:00Z",
        parts: [{ kind: "text", text: JAPANESE }],
      },
    ];
    const tx = wiring({}, true);
    const caps = capsWith(tx);
    render(HISTORY, caps);
    again(japanese, caps, false);
    expect(tx.autoKeys).toHaveLength(0);
  });
});

// A message from another session is a user turn nobody on this side wrote: the sending agent's
// language, not the reader's. The envelope is machine-facing (peerSenderOf reads it back), so it
// is never sent and stays verbatim in front of the translated body.
describe("translating a message from another session", () => {
  const ENVELOPE = "[agent-fleet:peer from=s-abc intent=question reply=required]";
  const BODY = "Can you check whether the migration ran on the dev deployment?";
  const peer = (text: string, source?: string): Turn[] => [
    { role: "user", text, idx: 1, anchorId: "u1", ts: "2026-09-13T10:00:00Z", source },
  ];
  const bubble = (): HTMLElement => host!.querySelector<HTMLElement>(".mirror-turn.user .markdown")!;

  it("sends only the body and shows the envelope untouched in front of the translation", () => {
    const tx = wiring();
    const caps = capsWith(tx);
    const turns = peer(ENVELOPE + "\n" + BODY, "peer");
    const el = render(turns, caps);
    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={{ ...caps }} />));
    expect(tx.calls).toEqual([[BODY]]);
    expect(bubble().textContent).toBe(ENVELOPE + "\n\n訳: " + BODY);
    // The badge still reads the sender from the (untranslated) envelope.
    expect(el.querySelector(".mt-peer")!.textContent).toContain("s-abc");
  });

  it("covers the spawn envelope too", () => {
    const tx = wiring();
    render(peer("[agent-fleet:spawn from=s-parent]\n" + BODY, "spawn"), capsWith(tx));
    act(() => host!.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    expect(tx.calls).toEqual([[BODY]]);
  });

  it("is offered while the session is working — a message is whole the moment it lands", () => {
    const el = render(peer(ENVELOPE + "\n" + BODY, "peer"), capsWith(wiring()), true);
    expect(el.querySelector(".mt-translate")).not.toBeNull();
  });

  it("is never offered on the reader's own prompt, whatever its language", () => {
    const el = render(peer(BODY), capsWith(wiring()));
    expect(el.querySelector(".mt-translate")).toBeNull();
  });

  it("is not offered on a message already in the reader's language", () => {
    const el = render(peer(ENVELOPE + "\n" + JAPANESE, "peer"), capsWith(wiring()));
    expect(el.querySelector(".mt-translate")).toBeNull();
  });
});
