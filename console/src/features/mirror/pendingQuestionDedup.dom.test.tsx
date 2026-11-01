// #3190: a question whose transcript part reached the client in an earlier poll than its pending
// payload was drawn twice. The whole view is mounted so the wiring — poll 1 delivers the open
// part, poll 2 the pending payload, poll 3 the answer — is what is tested.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { MirrorView } from "./MirrorView.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { setLocale } from "../../lib/i18n/index.ts";
import type { Session } from "../../types/session.ts";

const SESSION = "dup-question-session";
const QS = [{ header: "Plan", question: "Which branch?", options: [{ label: "main" }, { label: "dev" }] }];
const part = (extra: object = {}) => ({ role: "assistant", idx: 1, parts: [{ kind: "question", questions: QS, qid: "toolu_1", ...extra }] });

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let body: Record<string, unknown> = {};
let served = 0; // messages responses handed out; lets a test wait for a poll that SAW the new body

async function settle(rounds = 5) {
  for (let i = 0; i < rounds; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
}

async function until(cond: () => boolean, what: string) {
  for (let i = 0; i < 400; i++) {
    if (cond()) return;
    await act(async () => {
      await new Promise((r) => setTimeout(r, 25));
    });
  }
  throw new Error("timed out waiting for " + what);
}

const cards = () => document.querySelectorAll(".mt-question").length;

beforeEach(() => {
  localStorage.clear();
  setLocale("en");
  useWorkspaceStore.setState({ state: "running" });
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const isMessages = String(input).includes(`/sessions/${SESSION}/messages`);
    if (isMessages) served++;
    const out = isMessages ? body : {};
    return new Response(JSON.stringify(out), { status: 200, headers: { "Content-Type": "application/json" } });
  });
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.unstubAllGlobals();
});

describe("a pending question is drawn once", () => {
  it("hides the open transcript copy while pending and shows it decided after the answer", async () => {
    body = { cursor: 1, alive: true, status: "idle", messages: [part()] };
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const meta = { name: SESSION, kind: "claude", alive: true } as unknown as Session;
    await act(async () => {
      root!.render(
        <ToastProvider>
          <MirrorView paneId="p1" session={SESSION} sessionMeta={meta} active mirror onToggleMirror={() => {}} />
        </ToastProvider>,
      );
    });
    // Poll 1: only the transcript card exists.
    await until(() => cards() === 1, "the transcript card");
    expect(document.querySelector(".mt-question.answered")).toBeNull();
    // Poll 2: the payload arrives, nothing new in the window. One card — the pending one.
    body = { cursor: 1, alive: true, status: "question", messages: [], pendingQuestions: QS };
    const seen = served;
    await until(() => served >= seen + 2, "two polls after the payload");
    await settle();
    expect(document.body.textContent).toContain("Which branch?");
    expect(cards()).toBe(1);
    // Poll 3: answered. The held part comes back decided.
    body = { cursor: 2, alive: true, status: "idle", messages: [part({ answer: "main" })] };
    await until(() => !!document.querySelector(".mt-question.answered"), "the answered card");
    expect(cards()).toBe(1);
  }, 30000);
});
