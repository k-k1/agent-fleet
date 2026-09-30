// The chat's two-stage stop (ADR 0105), driven through the whole MirrorView against a stubbed
// Agent. The branches live in MirrorView's wiring — which guard a stop passes, when the stop
// row is drawn, what a notice restores — so mounting the parts alone would prove the parts and
// leave the wiring untested. The Agent's answers are the frozen wire fixture the Go side tests
// against too (workspace/agent/testdata/stop-queue-wire.json).
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { MirrorView } from "./MirrorView.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import type { Session } from "../../types/session.ts";
import { echoStore, sweptDiscards } from "./parts/sendEcho.ts";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Json = any;
// Resolved from the working directory: the Console tests always run from console/ (AGENTS.md),
// and under the dom project import.meta.url does not survive fileURLToPath at module scope.
const fixture: Json = JSON.parse(
  readFileSync(resolve(process.cwd(), "../workspace/agent/testdata/stop-queue-wire.json"), "utf8"),
);
const T = fixture.turn;
const M = fixture.messages;

const SESSION = "fixture-session";

// What the stubbed Agent answers. `messages` is the poll body; `turn` answers each POST /turn
// by op (a function may hold the answer back to model a request still in flight).
let messages: Json;
let turn: (body: Json) => Promise<{ status: number; body: Json }>;
let turnBodies: Json[];
let respondBodies: Json[];
let polls = 0;

const answer = (c: { status: number; response: Json }) => Promise.resolve({ status: c.status, body: c.response });

function stubFetch() {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const json = (status: number, body: Json) =>
      new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    if (url.includes(`/sessions/${SESSION}/messages`)) {
      polls++;
      return json(200, { cursor: 1, alive: true, ...messages });
    }
    if (url.includes(`/sessions/${SESSION}/turn`)) {
      const body = JSON.parse(String(init?.body ?? "{}"));
      turnBodies.push(body);
      const r = await turn(body);
      return json(r.status, r.body);
    }
    if (url.includes(`/sessions/${SESSION}/respond`)) {
      respondBodies.push(JSON.parse(String(init?.body ?? "{}")));
      return json(200, {});
    }
    return json(200, {});
  });
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const meta = (driver: "managed" | "tui"): Session =>
  ({ name: SESSION, kind: driver === "managed" ? "codex" : "claude", driver, alive: true }) as unknown as Session;

async function mount(driver: "managed" | "tui" = "managed") {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <MirrorView paneId="p1" session={SESSION} sessionMeta={meta(driver)} active mirror onToggleMirror={() => {}} />
      </ToastProvider>,
    );
  });
  await settle();
}

// Lets the poll's fetch resolve and React commit what it set.
async function settle(rounds = 5) {
  for (let i = 0; i < rounds; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
}

// repoll waits, in real time, until the mirror has fetched /messages again and applied it. The
// poll runs every 1.2-3 s, so a test that needs a later poll to have happened must wait for
// one: settling React alone would pass without the poll it claims to test ever running.
async function repoll() {
  const n = polls;
  for (let i = 0; i < 100 && polls === n; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
  }
  if (polls === n) throw new Error("no poll came");
  await settle(3);
}

async function until(cond: () => boolean, what: string) {
  for (let i = 0; i < 50; i++) {
    if (cond()) return;
    await settle(1);
  }
  throw new Error("timed out waiting for " + what);
}

const QUESTION = { id: "q1", question: "Which?", options: [{ label: "A" }, { label: "B" }] };

const $ = <E extends Element = HTMLElement>(sel: string) => document.querySelector<E>(sel);
const $$ = (sel: string) => Array.from(document.querySelectorAll<HTMLElement>(sel));
const click = async (el: Element | null) => {
  expect(el, "element to click").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await settle(2);
};
const composer = () => $<HTMLTextAreaElement>("textarea");
// Empties the composer the way typing does: React owns the value property, so the write has to
// go through the native setter for onChange to fire. The staged attachment chips go too.
async function clearComposer() {
  await act(async () => {
    const el = composer()!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, "");
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  for (const x of $$(".ma-del")) await click(x);
  await settle(2);
}
const toasts = () => $$(".ui-toast-msg").map((e) => e.textContent || "");

beforeEach(() => {
  localStorage.clear();
  turnBodies = [];
  respondBodies = [];
  // Both are module-level per session name, and every test uses the same name.
  echoStore.clear();
  sweptDiscards.clear();
  messages = { status: "working", messages: [] };
  turn = () => answer(T.interrupt_first);
  useWorkspaceStore.setState({ state: "running" });
  stubFetch();
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.unstubAllGlobals();
});

describe("stop button", () => {
  it("stays pressable while a stop is in flight, and a second press sends a second stop", async () => {
    let release!: () => void;
    turn = () =>
      new Promise((res) => {
        release = () => res({ status: T.interrupt_first.status, body: T.interrupt_first.response });
      });
    await mount();
    const stop = $<HTMLButtonElement>(".mirror-stop");
    await click(stop);
    expect(turnBodies).toEqual([T.interrupt_first.request]);
    // The first answer has not come back: this is when the second stop is needed.
    expect($<HTMLButtonElement>(".mirror-stop")!.disabled).toBe(false);
    turn = () => answer(T.interrupt_second);
    await click($(".mirror-stop"));
    expect(turnBodies).toEqual([T.interrupt_first.request, T.interrupt_second.request]);
    release();
    await settle();
  });

  it("says a second press also stops what the queue starts (Managed)", async () => {
    await mount();
    expect($(".mirror-stop")!.title).toMatch(/もう一度|again/);
  });

  it("tells the member a first stop let the queue go on", async () => {
    messages = { status: "working", messages: [], ...M.working };
    await mount();
    await click($(".mirror-stop"));
    await until(() => toasts().some((t) => /キューの入力が続けて|queued input starts next/.test(t)), "first-stop toast");
  });
});

describe("stop and discard the queue", () => {
  it("is in the menu while running even when the chat sees no queue, and sends discard_queue", async () => {
    turn = () => answer(T.interrupt_discard_queue_nothing_queued);
    await mount();
    const more = $(".mirror-stop-more");
    expect(more).toBeTruthy();
    expect(more!.classList.contains("hot")).toBe(false);
    await click(more);
    await click($(".mirror-stop-menu .ui-menu-item"));
    expect(turnBodies).toEqual([T.interrupt_discard_queue_nothing_queued.request]);
  });

  it("is emphasised, with the count, while the chat sees a queue", async () => {
    messages = { status: "working", messages: [], ...M.working };
    await mount();
    const more = $(".mirror-stop-more")!;
    expect(more.classList.contains("hot")).toBe(true);
    expect(more.textContent).toContain("3");
  });

  it("is reachable while the session is idle with something still queued", async () => {
    messages = { status: "idle", messages: [], queuedItems: M.held_by_the_runtime.queuedItems };
    await mount();
    expect($(".mirror-stop-more")).toBeTruthy();
  });

  it("is reachable while a question card is shown, with nothing queued", async () => {
    // A pending question reads status "question", not "working": nothing but the question
    // itself keeps the brake on screen here.
    messages = { status: "question", messages: [], pendingQuestions: [QUESTION] };
    await mount();
    expect($(".mq-cancel")).toBeTruthy(); // the card is up
    expect($(".mirror-stop")).toBeTruthy();
    expect($(".mirror-stop-more")).toBeTruthy();
    expect($(".mirror-stop-more")!.classList.contains("hot")).toBe(false);
  });

  it("shows the queue behind a question with its actions, and emphasises the brake", async () => {
    // queuedItems now comes whenever the runtime is up and holds a queue — a codex question
    // with input queued behind it included.
    messages = { status: "question", messages: [], pendingQuestions: [QUESTION], queuedItems: M.working.queuedItems };
    await mount();
    expect($(".mq-cancel")).toBeTruthy();
    expect($$(".mirror-turn.user")).toHaveLength(3);
    expect($$(".mt-queue-remove")).toHaveLength(2);
    const more = $(".mirror-stop-more")!;
    expect(more.classList.contains("hot")).toBe(true);
    expect(more.textContent).toContain("3");
  });

  it("a Managed question's Cancel declines it through /respond, never as a stop", async () => {
    messages = { status: "question", messages: [], pendingQuestions: [QUESTION], queuedItems: M.working.queuedItems };
    await mount();
    await click($(".mq-cancel"));
    expect(respondBodies).toEqual([{ id: "q1", decision: "cancel" }]);
    expect(turnBodies).toEqual([]);
  });

  it("is not offered on Terminal (CLI), whose queue lives in the CLI", async () => {
    messages = { status: "working", messages: [], queuedPrompts: ["then deploy it"] };
    await mount("tui");
    expect($(".mirror-stop")).toBeTruthy();
    expect($(".mirror-stop-more")).toBeNull();
    await click($(".mirror-stop"));
    expect(turnBodies).toEqual([{ op: "interrupt" }]);
  });

  it("keeps Terminal (CLI)'s stop out from under a question, whose Cancel owns the Esc", async () => {
    messages = {
      status: "working",
      messages: [],
      pendingQuestions: [{ question: "Which?", options: [{ label: "A" }, { label: "B" }] }],
    };
    await mount("tui");
    expect($(".mirror-stop")).toBeNull();
  });
});

describe("queued bubbles", () => {
  it("offer back-to-input and remove on still-queued entries only", async () => {
    messages = { status: "working", messages: [], ...M.working };
    await mount();
    const bubbles = $$(".mirror-turn.user");
    expect(bubbles).toHaveLength(3); // one per entry, never folded together
    const acts = (sel: string) => bubbles.map((b) => b.querySelectorAll(sel).length);
    // cm_a is committed; af_c (a peer) and cm_b (discord, the member) are still queued. Only
    // the member's own input may go back into the input box; any queued entry may be removed.
    expect(acts(".mt-queue-remove")).toEqual([0, 1, 1]);
    expect(acts(".mt-queue-restore")).toEqual([0, 0, 1]);
    // A queued peer message wears its origin badge before it runs.
    expect(bubbles[1].classList.contains("from-peer")).toBe(true);
  });

  it("puts the text back into the input box only once remove succeeded", async () => {
    messages = { status: "working", messages: [], ...M.working };
    turn = () => answer(T.remove);
    await mount();
    const bubble = $$(".mirror-turn.user")[2];
    await click(bubble.querySelector(".mt-queue-act"));
    expect(turnBodies).toEqual([T.remove.request]);
    expect(composer()!.value).toBe(T.remove.response.removed.text);
  });

  it("leaves the input box alone and says so when the entry already started", async () => {
    messages = { status: "working", messages: [], ...M.working };
    turn = () => answer(T.remove_already_started);
    await mount();
    await click($$(".mirror-turn.user")[2].querySelector(".mt-queue-act"));
    expect(composer()!.value).toBe("");
    await until(() => toasts().some((t) => /すでに始まって|already started/.test(t)), "already_started toast");
  });

  it("has no actions without queuedItems (an older Agent, or Terminal)", async () => {
    messages = { status: "working", messages: [], queuedPrompts: M.working.queuedPrompts };
    await mount();
    expect($$(".mirror-turn.user").length).toBeGreaterThan(0);
    expect($$(".mt-queue-act")).toHaveLength(0);
  });
});

describe("discard notice", () => {
  it("counts what was discarded, lists other origins, and restores the member's input one at a time", async () => {
    messages = { status: "idle", messages: [], ...M.idle_after_a_second_stop };
    turn = (b) => answer(b.op === "dismiss_discard" ? T.dismiss_discard : T.interrupt_first);
    await mount();
    const notice = $(".mirror-discard")!;
    expect(notice.textContent).toMatch(/6 件を捨てました|6 queued inputs were discarded/);
    // Member input is member + discord/slack: cm_d and af_i. The rest are listed by origin.
    expect($$(".mirror-discard .md-item")).toHaveLength(1 + 4);
    await click($(".md-restore"));
    expect(composer()!.value).toBe("look at this");
    expect($$(".ma-chip").length).toBe(1); // its attachment came back too
    // The next one waits for an empty input box rather than overwriting it.
    expect($<HTMLButtonElement>(".md-restore")!.disabled).toBe(true);
  });

  it("dismisses only once the last of the member's inputs is back, not after the first", async () => {
    messages = { status: "idle", messages: [], ...M.idle_after_a_second_stop };
    turn = () => answer(T.dismiss_discard);
    await mount();
    await click($(".md-restore"));
    expect(composer()!.value).toBe("look at this");
    // One of two is back. Dismissing now would lose the other on the server if the tab closed.
    expect(turnBodies).toEqual([]);
    await clearComposer();
    await click($(".md-restore"));
    expect(composer()!.value).toBe("from slack");
    expect(turnBodies).toEqual([{ op: "dismiss_discard", id: "dsc_0002" }]);
    expect($(".mirror-discard")).toBeNull();
  });

  it("stays shown for the rest after the poll no longer carries the dismissed discard", async () => {
    messages = { status: "idle", messages: [], ...M.idle_after_a_second_stop };
    turn = () => answer(T.dismiss_discard);
    await mount();
    await click($(".md-restore"));
    messages = { status: "idle", messages: [] };
    await repoll();
    expect($(".mirror-discard")).toBeTruthy();
  });

  it("closing dismisses it once and hides it even if a stale poll still carries it", async () => {
    messages = { status: "working", messages: [], ...M.working };
    turn = () => answer(T.dismiss_discard);
    await mount();
    expect($(".mirror-discard")).toBeTruthy();
    await click($(".md-close"));
    expect(turnBodies).toEqual([T.dismiss_discard.request]);
    await repoll(); // still carries the discard
    expect($(".mirror-discard")).toBeNull();
  });

  it("words a first stop's discard as stopped before sending", async () => {
    messages = { status: "idle", messages: [], discardedInputs: [T.interrupt_first_stops_unsent_start.response.discard] };
    await mount();
    expect($(".mirror-discard .md-msg")!.textContent).toMatch(/送る前に止めました。1 件を戻せます|Stopped before it was sent\. 1 input/);
  });

  it("sweeps the Pending echo of an input a discard threw away, once", async () => {
    messages = { status: "working", messages: [] };
    turn = () => Promise.resolve({ status: 200, body: { sent: SESSION, op: "steer" } });
    await mount();
    const send = async (text: string) => {
      await act(async () => {
        const el = composer()!;
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
      await click($(".mirror-send"));
    };
    await send("just sent");
    expect($$(".mirror-turn.user .mt-pending")).toHaveLength(1);
    messages = { status: "idle", messages: [], discardedInputs: [T.interrupt_first_stops_unsent_start.response.discard] };
    await repoll();
    expect($$(".mirror-turn.user")).toHaveLength(0);
    // The member puts it back and sends it again: the still-listed discard must not eat it. The
    // session now reads working, so the next poll is a new payload and is applied in full.
    await send("just sent");
    messages = { ...messages, status: "working" };
    await repoll();
    expect($$(".mirror-turn.user .mt-pending")).toHaveLength(1);
  });

  it("is not shown on Terminal (CLI)", async () => {
    messages = { status: "idle", messages: [], ...M.idle_after_a_second_stop };
    await mount("tui");
    expect($(".mirror-discard")).toBeNull();
  });
});
