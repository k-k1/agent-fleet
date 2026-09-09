// Pins the central promise of TranscriptCaps in jsdom: an absent capability renders no control.
// The shared-session view (docs/log/59) hands over almost none of the owner-only capabilities,
// so a break here lines a recipient's screen with buttons that click and do nothing — and worse
// than being untidy, those buttons offer to open somebody else's Workspace.
//
// Also checks the fallback rendering an absent capability switches to (an inline diff, an inline
// plan): merely removing the dead button would leave a recipient unable to reach the changes.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// MarkdownView pulls in the whole of remark/rehype, so here we only check that the body renders.
vi.mock("../../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source }: { source?: string }) => <div className="markdown">{source}</div>,
}));

import { TranscriptView } from "./TranscriptView.tsx";
import { groupTurns } from "./model.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import type { Part, Turn } from "./types.ts";
import { t as tr } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

type LiveProps = { working?: boolean; autoCollapseWork?: boolean };

function render(turns: Turn[], caps: TranscriptCaps, props: LiveProps = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={caps} {...props} />));
  return host;
}

// Re-render into the SAME root — the path where React updates without remounting, which is what
// actually happens whenever polling moves the status. Rendering into a new root would wipe the
// state and detect nothing.
function rerender(turns: Turn[], caps: TranscriptCaps, props: LiveProps) {
  act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={caps} {...props} />));
  return host!;
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const EDIT_TURN: Turn[] = [
  { role: "user", text: "直して", idx: 1, anchorId: "u1", ts: "2026-08-13T10:00:00Z" },
  {
    role: "assistant",
    idx: 2,
    ts: "2026-08-13T10:01:00Z",
    parts: [
      // `file` is the coordinate the Agent always attaches to an edit-family tool
      // (docs/log/68). Without it there is no chip for the files this turn changed.
      { kind: "tool", tool: "Edit", info: "app.ts", file: "src/app.ts", edits: [{ old: "const a = 1", new: "const a = 2" }] },
      { kind: "text", text: "直しました" },
    ],
  },
];

const OWNER: TranscriptCaps = {
  agentName: "Claude",
  session: "s1",
  openDiff: () => {},
  openPlan: () => {},
  openFile: () => {},
  forkAt: () => {},
  onReauth: () => {},
};
// All a recipient gets — exactly what the shared view actually passes.
const RECIPIENT: TranscriptCaps = { agentName: "Claude" };

describe("TranscriptCaps: no capability, no control", () => {
  it("gives the owner a button to open the diff pane and a fork route", () => {
    const el = render(EDIT_TURN, OWNER);
    expect(el.querySelector(".mt-tool-diff")).not.toBeNull();
    expect(el.querySelector(".mt-fork")).not.toBeNull();
  });

  it("gives the owner chips at the end of the turn for the files it changed (docs/log/68 P1)", () => {
    const el = render(EDIT_TURN, OWNER);
    const chips = el.querySelectorAll(".mtf-chip");
    expect(chips).toHaveLength(1);
    expect(chips[0].textContent).toContain("app.ts");
  });

  it("renders no chips for a recipient (the shared DTO drops the paths, so there is nothing to open)", () => {
    expect(render(EDIT_TURN, RECIPIENT).querySelector(".mirror-turn-files")).toBeNull();
  });

  it("renders no fork route for a recipient (there is no session of theirs to call)", () => {
    const el = render(EDIT_TURN, RECIPIENT);
    expect(el.querySelector(".mt-fork")).toBeNull();
  });

  it("turns a recipient's edit trace into an inline diff rather than a dead button", () => {
    const el = render(EDIT_TURN, RECIPIENT);
    expect(el.querySelector(".mt-tool-diff")).toBeNull(); // no button to open a pane
    const head = el.querySelector<HTMLButtonElement>(".mt-tool-outhead");
    expect(head).not.toBeNull();
    expect(el.querySelector(".mt-tool-diff-inline")).toBeNull(); // collapsed by default
    act(() => head!.click());
    const inline = el.querySelector(".mt-tool-diff-inline");
    expect(inline).not.toBeNull();
    // Rendered with the same lineDiff / dv-* as the diff pane, so added and removed lines pair up
    expect(inline!.querySelectorAll(".dv-row.dv-add").length).toBe(1);
    expect(inline!.querySelectorAll(".dv-row.dv-del").length).toBe(1);
    expect(inline!.textContent).toContain("const a = 2");
  });

  it("lets a recipient expand a plan in full in place, with no pane", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        parts: [{ kind: "plan", plan: "# 移行計画\n\n最初に棚卸しする", answer: "approved" }],
      },
    ];
    const owner = render(turns, OWNER);
    expect(owner.querySelector(".mt-plan-open")).not.toBeNull();
    expect(owner.querySelector(".mt-plan-body")).toBeNull(); // the owner opens it in a pane
    act(() => root?.unmount());
    host?.remove();

    const el = render(turns, RECIPIENT);
    const toggle = el.querySelector<HTMLButtonElement>(".mt-plan-open");
    expect(toggle).not.toBeNull();
    act(() => toggle!.click());
    expect(el.querySelector(".mt-plan-body")?.textContent).toContain("最初に棚卸しする");
  });

  it("offers a recipient no route to re-authenticate the owner's agent", () => {
    const turns: Turn[] = [
      { role: "assistant", idx: 1, parts: [{ kind: "error", info: "OAuthError", text: "Please run /login", cause: "auth" }] },
    ];
    expect(render(turns, OWNER).querySelector(".mef-action")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();
    const el = render(turns, RECIPIENT);
    expect(el.querySelector(".mirror-error")).not.toBeNull(); // the failure itself is visible
    expect(el.querySelector(".mef-action")).toBeNull(); // but no route to go and fix it
  });

  it("stops asking for a re-authentication the owner has already made (docs/log/47 §4-11)", () => {
    // The transcript never changes, so without this the block goes on demanding a sign-in for
    // the rest of the conversation's life — the reader fixes it, comes back, and the last thing
    // in the mirror still says it is broken.
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        ts: "2026-08-14T03:00:00Z",
        parts: [{ kind: "error", info: "authentication_failed (HTTP 401)", text: "Please run /login", cause: "auth" }],
      },
    ];
    // A login written BEFORE the failed turn is the one that failed: keep offering the fix.
    const stale = render(turns, { ...OWNER, authOkAt: "2026-08-14T02:00:00Z" });
    expect(stale.querySelector(".mef-action")).not.toBeNull();
    expect(stale.querySelector(".mef-done")).toBeNull();
    act(() => root?.unmount());
    host?.remove();

    const el = render(turns, { ...OWNER, authOkAt: "2026-08-14T04:00:00Z" });
    expect(el.querySelector(".mef-action")).toBeNull(); // no button: there is nothing left to do
    const done = el.querySelector(".mef-done");
    expect(done).not.toBeNull();
    expect(done!.textContent).toContain(tr("mirror.error_auth_done", { agent: "Claude" }));
    // The failure itself is still shown as a failure — it did happen.
    expect(el.querySelector(".mirror-error-body")?.textContent).toContain("/login");
  });

  it("leaves a failure that is not about the login alone, however new the sign-in is", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        ts: "2026-08-14T03:00:00Z",
        parts: [{ kind: "error", info: "rate_limit (HTTP 429)", text: "You've hit your session limit" }],
      },
    ];
    const el = render(turns, { ...OWNER, authOkAt: "2026-08-14T04:00:00Z" });
    expect(el.querySelector(".mirror-error")).not.toBeNull();
    expect(el.querySelector(".mirror-error-fix")).toBeNull();
  });

  it("hides the attachment panel entirely when there is nowhere to open it (the DTO drops the paths)", () => {
    const turns: Turn[] = [
      { role: "assistant", idx: 1, parts: [{ kind: "userfile", files: ["out/report.md"], caption: "結果" }] },
    ];
    expect(render(turns, OWNER).querySelector(".mt-files")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();
    expect(render(turns, RECIPIENT).querySelector(".mt-files")).toBeNull();
  });
});

describe("the shared view folds exactly as the mirror does", () => {
  it("collapses consecutive tools into one row that expands to show them", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        parts: [
          { kind: "tool", tool: "Read" },
          { kind: "tool", tool: "Read" },
          { kind: "tool", tool: "Bash" },
          { kind: "text", text: "終わりました" },
        ],
      },
    ];
    const el = render(turns, RECIPIENT);
    const runHead = el.querySelector<HTMLButtonElement>(".mt-toolrun-head");
    expect(runHead).not.toBeNull();
    // While collapsed no individual tool row is rendered (the old version listed them all).
    expect(el.querySelectorAll(".mt-toolrun-body .mt-tool").length).toBe(0);
    expect(runHead!.textContent).toContain("Read×2");
    act(() => runHead!.click());
    expect(el.querySelectorAll(".mt-toolrun-body .mt-tool").length).toBe(3);
  });

  it("renders a compaction summary as a folded block, not a giant turn", () => {
    const turns: Turn[] = [
      { role: "user", text: "長い要約…", idx: 1, compact: true },
      { role: "assistant", text: "続けます", idx: 2 },
    ];
    const el = render(turns, RECIPIENT);
    expect(el.querySelector("details.mirror-compact")).not.toBeNull();
  });
});

// The mirror's `working` is a live polled value, so it flaps even mid-turn: claude's Stop hook
// yields a momentary idle, and working goes true before an operator / scheduled / peer prompt
// reaches the transcript. Swinging the work trace open and shut on each flap jumps the body's
// height under a reader. Folding is one-way, and open/closed belongs to the reader.
describe("folding the work trace never flaps back", () => {
  const WORK_TURN: Turn[] = [
    { role: "user", text: "調べて", idx: 1, ts: "2026-08-22T10:00:00Z" },
    {
      role: "assistant",
      idx: 2,
      ts: "2026-08-22T10:01:00Z",
      parts: [
        { kind: "tool", tool: "Read" },
        { kind: "tool", tool: "Bash" },
        { kind: "text", text: "調べ終わりました。原因は設定ミスです。" },
      ],
    },
  ];
  const workState = (el: HTMLElement) => {
    const head = el.querySelector<HTMLButtonElement>(".mt-work-head");
    return head ? (head.getAttribute("aria-expanded") === "true" ? "open" : "closed") : "unfolded";
  };

  it("never re-opens after folding on completion, even when status claims working again", () => {
    // Following the tail while the turn runs: the work trace is unfolded and fully shown.
    const el = render(WORK_TURN, OWNER, { working: true, autoCollapseWork: true });
    expect(workState(el)).toBe("unfolded");
    // Completion folds it (following the tail, so it becomes a closed summary).
    expect(workState(rerender(WORK_TURN, OWNER, { working: false, autoCollapseWork: true }))).toBe("closed");
    // Status claiming working again leaves what was folded folded.
    expect(workState(rerender(WORK_TURN, OWNER, { working: true, autoCollapseWork: true }))).toBe("closed");
    expect(workState(rerender(WORK_TURN, OWNER, { working: false, autoCollapseWork: true }))).toBe("closed");
  });

  it("keeps a work trace the reader opened open through later status and tail changes", () => {
    const el = render(WORK_TURN, OWNER, { working: false, autoCollapseWork: true });
    expect(workState(el)).toBe("closed");
    act(() => el.querySelector<HTMLButtonElement>(".mt-work-head")!.click());
    expect(workState(el)).toBe("open");
    // Losing the tail, regaining it, appearing to run — none of these overrides the reader.
    expect(workState(rerender(WORK_TURN, OWNER, { working: true, autoCollapseWork: false }))).toBe("open");
    expect(workState(rerender(WORK_TURN, OWNER, { working: false, autoCollapseWork: true }))).toBe("open");
  });

  it("carries no fold state across a session switch (the mirror is not remounted)", () => {
    const el = render(WORK_TURN, OWNER, { working: false, autoCollapseWork: true });
    expect(workState(el)).toBe("closed");
    // Another session's turn with the same idx. If React reuses the component, a running work
    // trace inherits the previous session's folded state and starts out hidden.
    const other = rerender(WORK_TURN, { ...OWNER, session: "s2" }, { working: true, autoCollapseWork: true });
    expect(workState(other)).toBe("unfolded");
  });

  it("folds a turn that completes while the reader scrolled up, but leaves it open", () => {
    // autoCollapseWork=false means reading away from the tail: show the summary row, keep the
    // body open.
    const el = render(WORK_TURN, OWNER, { working: true, autoCollapseWork: false });
    expect(workState(el)).toBe("unfolded");
    expect(workState(rerender(WORK_TURN, OWNER, { working: false, autoCollapseWork: false }))).toBe("open");
    // Returning to the tail must not close what is being read.
    expect(workState(rerender(WORK_TURN, OWNER, { working: false, autoCollapseWork: true }))).toBe("open");
  });
});

// The work trace is rendered inside a disclosure only while workSplit finds a boundary; with no
// split it is rendered INLINE, at full height and with no control. So a split that comes and goes
// is not a cosmetic flicker — it is the whole trace blowing open and shut under the reader. Both
// halves of that are pinned here: what latches the fold on a running turn, and the boundary
// surviving the polls where workSplit has nothing to say.
describe("the work boundary never disappears once a turn has one", () => {
  const bigWork: Part[] = [];
  for (let i = 0; i < 8; i++) {
    bigWork.push({ kind: "tool", tool: "Read", info: "f" + i });
    bigWork.push({ kind: "text", text: "経過 " + i });
  }
  const ANSWER: Part = { kind: "text", text: "調べ終わりました。原因は設定ミスです。" };
  const turnsWith = (parts: Part[], extra: Turn[] = []): Turn[] => [
    { role: "user", text: "調べて", idx: 1, ts: "2026-09-08T10:00:00Z" },
    { role: "assistant", idx: 2, ts: "2026-09-08T10:01:00Z", parts },
    ...extra,
  ];
  const workState = (el: HTMLElement) => {
    const head = el.querySelector<HTMLButtonElement>(".mt-work-head");
    return head ? (head.getAttribute("aria-expanded") === "true" ? "open" : "closed") : "unfolded";
  };

  it("keeps the disclosure through a poll whose last part is a tool", () => {
    const done = [...bigWork, ANSWER];
    expect(workState(render(turnsWith(done), OWNER, { working: true, autoCollapseWork: true }))).toBe("unfolded");
    // Completion folds it…
    expect(workState(rerender(turnsWith(done), OWNER, { working: false, autoCollapseWork: true }))).toBe("closed");
    // …and the turn turning out not to be over (one more tool, so workSplit has no final text to
    // split on) must not throw the whole trace back on screen.
    const toolLast = [...done, { kind: "tool", tool: "Write", info: "memo.md" } as Part];
    expect(workState(rerender(turnsWith(toolLast), OWNER, { working: true, autoCollapseWork: true }))).toBe("closed");
    // …nor when the reader is the one who closed it.
    const textAgain = [...toolLast, { kind: "text", text: "続けます" } as Part];
    expect(workState(rerender(turnsWith(textAgain), OWNER, { working: true, autoCollapseWork: true }))).toBe("closed");
  });

  it("does not fold a still-running reply because a follow-up was typed into it", () => {
    const streaming = [...bigWork, { kind: "text", text: "途中の説明" } as Part];
    const el = render(turnsWith(streaming), OWNER, { working: true, autoCollapseWork: true });
    expect(workState(el)).toBe("unfolded");
    // The optimistic echo of a follow-up sent mid-turn: a user group AFTER the running reply.
    // It is not the boundary — the agent has not started it — so the reply stays the live one.
    const echo: Turn[] = [{ role: "user", text: "ついでにこれも", idx: 1e9 + 1, pending: true }];
    expect(workState(rerender(turnsWith(streaming, echo), OWNER, { working: true, autoCollapseWork: true }))).toBe(
      "unfolded",
    );
  });

  it("still folds the previous reply the moment a follow-up is sent after it finished", () => {
    const done = [...bigWork, ANSWER];
    // It completed while the reader watched, so it folded then — and the fold is one-way.
    expect(workState(render(turnsWith(done), OWNER, { working: false, autoCollapseWork: true }))).toBe("closed");
    const echo: Turn[] = [{ role: "user", text: "次のお願い", idx: 1e9 + 1, pending: true }];
    expect(workState(rerender(turnsWith(done, echo), OWNER, { working: true, autoCollapseWork: true }))).toBe("closed");
  });

  it("carries no remembered boundary across a session switch", () => {
    const done = [...bigWork, ANSWER];
    expect(workState(render(turnsWith(done), OWNER, { working: false, autoCollapseWork: true }))).toBe("closed");
    // Another session's turn at the same idx, still running: it must start unfolded, not inherit
    // the previous conversation's boundary.
    const other = rerender(turnsWith(done), { ...OWNER, session: "s2" }, { working: true, autoCollapseWork: true });
    expect(workState(other)).toBe("unfolded");
  });
});

// A shared file is a deliverable, and the fold would eat it: workSplit's boundary sits after the
// LAST tool, so a picture card with any tool after it lands inside a closed disclosure whose
// summary counts only tools and interim texts — nothing on screen says an image is in there.
// Measured on a real session (5 generated images over two generate_image calls): only the last
// call's card was visible. Generating twice is not even needed — the agent opening the picture it
// just made is one more tool, and buries it the same way.
describe("a shared file is never folded into the work trace", () => {
  const IMAGES = ["out/curry-1.png", "out/curry-2.png"];
  const TWO_CALLS: Turn[] = [
    { role: "user", text: "カレーの画像を作って", idx: 1 },
    {
      role: "assistant",
      idx: 2,
      parts: [
        { kind: "tool", tool: "mcp__af_40ed9852__generate_image", info: "curry" },
        { kind: "userfile", files: IMAGES },
        { kind: "text", text: "もう1枚作ります" },
        { kind: "tool", tool: "mcp__af_40ed9852__generate_image", info: "curry" },
        { kind: "userfile", files: ["out/curry-3.png"] },
        { kind: "text", text: "3枚のカレー画像が生成できました。" },
      ],
    },
  ];
  // Folded content stays in the DOM (aria-hidden + inert), so presence proves nothing here —
  // only placement does.
  const inWork = (el: HTMLElement) => el.querySelectorAll(".mt-work .mt-files").length;

  it("hoists every card out of the closed disclosure, exactly once", () => {
    const el = render(TWO_CALLS, OWNER, { working: false, autoCollapseWork: true });
    expect(el.querySelector<HTMLButtonElement>(".mt-work-head")!.getAttribute("aria-expanded")).toBe("false");
    expect(el.querySelectorAll(".mt-files").length).toBe(2); // both calls' cards, no duplicate inside the fold
    expect(inWork(el)).toBe(0);
    // In order, and above the final answer.
    const cards = [...el.querySelectorAll(".mt-files")];
    expect(cards[0].querySelectorAll(".mt-file-item").length).toBe(IMAGES.length);
    const work = el.querySelector(".mt-work")!;
    expect(work.compareDocumentPosition(cards[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("leaves the tool traces folded", () => {
    const el = render(TWO_CALLS, OWNER, { working: false, autoCollapseWork: true });
    expect(el.querySelectorAll(".mt-work .mt-tool, .mt-work .mt-toolrun").length).toBeGreaterThan(0);
  });

  it("renders the cards once inline while the turn is still unfolded", () => {
    const el = render(TWO_CALLS, OWNER, { working: true, autoCollapseWork: true });
    expect(el.querySelector(".mt-work-head")).toBeNull(); // no disclosure yet
    expect(el.querySelectorAll(".mt-files").length).toBe(2);
  });
});

// An expanded work trace or thinking block runs to several screens, so with the only control in
// the head there is no way to fold it without scrolling all the way back up from where you
// finished reading. The same toggle is repeated at the bottom of the body.
describe("an expanded work trace or thinking block closes from the bottom too", () => {
  const WORK_TURN: Turn[] = [
    { role: "user", text: "調べて", idx: 1 },
    {
      role: "assistant",
      idx: 2,
      parts: [
        { kind: "tool", tool: "Read" },
        { kind: "text", text: "調べ終わりました。" },
      ],
    },
  ];
  const THINK_TURN: Turn[] = [
    { role: "user", text: "考えて", idx: 1 },
    {
      role: "assistant",
      idx: 2,
      parts: [
        { kind: "thinking", text: "まず前提を確かめる。" },
        { kind: "text", text: "こうです。" },
      ],
    },
  ];

  it("work trace: the bottom close folds it, ending in the same state as the head toggle", () => {
    const el = render(WORK_TURN, OWNER, { working: false, autoCollapseWork: true });
    const head = el.querySelector<HTMLButtonElement>(".mt-work-head")!;
    act(() => head.click());
    expect(head.getAttribute("aria-expanded")).toBe("true");
    const foot = el.querySelector<HTMLButtonElement>(".mt-work-body .mirror-disclosure-foot")!;
    expect(foot.textContent).toContain(tr("mirror.collapse_section"));
    act(() => foot.click());
    expect(head.getAttribute("aria-expanded")).toBe("false");
  });

  it("thinking: the bottom close folds it", () => {
    const el = render(THINK_TURN, OWNER);
    const head = el.querySelector<HTMLButtonElement>(".mirror-thinking-head")!;
    act(() => head.click());
    expect(head.getAttribute("aria-expanded")).toBe("true");
    act(() => el.querySelector<HTMLButtonElement>(".mirror-thinking-body .mirror-disclosure-foot")!.click());
    expect(head.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("how an incoming peer message looks (docs/log/58 §58.14)", () => {
  const peerTurn = (text: string): Turn[] => [{ role: "user", text, idx: 1, source: "peer" }];

  it("renders two chips, the sender and the kind", () => {
    const el = render(
      peerTurn("[agent-fleet:peer from=build-api intent=request reply=only-if-blocked] 直して"),
      RECIPIENT,
    );
    expect(el.querySelector(".mt-peer")?.textContent).toContain("build-api");
    const kind = el.querySelector(".mt-peer-kind");
    // The wording is locale-dependent, so check the translation resolved (no raw key leaked).
    expect(kind?.textContent?.trim()).toBeTruthy();
    expect(kind?.textContent).not.toContain("mirror.peer_intent");
  });

  it("still badges the sender for an older envelope with no kind (only the chip disappears)", () => {
    const el = render(peerTurn("[agent-fleet:peer from=build-api] 直して"), RECIPIENT);
    expect(el.querySelector(".mt-peer")?.textContent).toContain("build-api");
    expect(el.querySelector(".mt-peer-kind")).toBeNull();
  });

  // An envelope is enough to badge even with the origin tag gone. The tag lives in a separate
  // store and disappears for a turn fetched before the record was written (the mirror never
  // refetches a turn it holds) and for old records pushed out past the cap. Giving up here would
  // silently remove the only visualisation an incoming message has.
  it("badges from the envelope even when source is missing", () => {
    const el = render(
      [{ role: "user", text: "[agent-fleet:peer from=build-api intent=notice reply=none] 出た", idx: 1 }],
      RECIPIENT,
    );
    expect(el.querySelector(".mt-peer")?.textContent).toContain("build-api");
    expect(el.querySelector(".mt-peer-kind")?.textContent?.trim()).toBeTruthy();
  });

  it("adds no badge to the reader's own input, which has neither envelope nor origin tag", () => {
    const el = render([{ role: "user", text: "自分で打った指示", idx: 1 }], RECIPIENT);
    expect(el.querySelector(".mt-peer")).toBeNull();
  });

  // Arrivals over claude's own cross-session channel (docs/log/58 §58.16) carry no envelope —
  // they never went through AF, so there was nothing to attach one. The Agent recovers the name
  // from the transcript's origin and puts it in peerFrom, which is where this reads it. Without
  // that wiring the badge says only "another session", and an instruction neither the user nor
  // the operator sent cannot be traced to its source.
  it("names the sender from peerFrom for a native arrival with no envelope", () => {
    const el = render([{ role: "user", text: "資料の不足を申告する", idx: 1, source: "peer", peerFrom: "s6bbilu" }], RECIPIENT);
    expect(el.querySelector(".mt-peer")?.textContent).toContain("s6bbilu");
    // No envelope, so no kind chip: nothing is guessed.
    expect(el.querySelector(".mt-peer-kind")).toBeNull();
  });

  it("prefers the envelope's name when there is one (the server always attaches it)", () => {
    const el = render(
      [{ role: "user", text: "[agent-fleet:peer from=build-api intent=notice reply=none] 出た", idx: 1, source: "peer", peerFrom: "s6bbilu" }],
      RECIPIENT,
    );
    expect(el.querySelector(".mt-peer")?.textContent).toContain("build-api");
  });
});

// claude writes the tool_use for AskUserQuestion / ExitPlanMode at the moment it ASKS, so
// "present in the transcript" does not mean "decided". The decided badge is shown only once the
// tool_result arrives; hardcoding it is what made a plan awaiting approval claim it was decided.
describe("an undecided question or plan never claims it was decided", () => {
  it("does not badge a question card answered when there is no answer", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        parts: [
          {
            kind: "question",
            questions: [{ header: "方式", question: "どれにしますか？", options: [{ label: "案A" }, { label: "案B" }] }],
          },
        ],
      },
    ];
    const el = render(turns, RECIPIENT);
    expect(el.querySelector(".mt-question")).not.toBeNull(); // the question itself is visible
    expect(el.querySelector(".mq-done")).toBeNull();
  });

  it("does not badge a plan card decided when there is no tool_result", () => {
    const turns: Turn[] = [{ role: "assistant", idx: 1, parts: [{ kind: "plan", plan: "# 移行計画\n\n棚卸しする" }] }];
    const el = render(turns, RECIPIENT);
    expect(el.querySelector(".mt-plan")).not.toBeNull();
    expect(el.querySelector(".mt-plan-badge")).toBeNull();
    expect(el.querySelector(".mt-plan.decided")).toBeNull();
  });

  it("badges it rejected ahead of the tool_result when the optimistic reject mark is set", () => {
    const turns: Turn[] = [{ role: "assistant", idx: 1, parts: [{ kind: "plan", plan: "# 移行計画" }] }];
    const el = render(turns, { ...OWNER, isRejectedPlan: () => true });
    expect(el.querySelector(".mt-plan-badge")?.textContent).toBe(tr("mirror.rejected"));
  });
});
