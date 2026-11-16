// A stopped session's carried question must not follow the pane to another session (#4307).
// The whole view is mounted so the path from the poll state to the drawn card is what is
// tested: A's card is drawn, the pane switches to B while B's first response is held back, the
// card is gone at once, and B's own carried card is drawn once B answers.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { MirrorView } from "./MirrorView.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { setLocale } from "../../lib/i18n/index.ts";
import type { Session } from "../../types/session.ts";

const A = "carried-a";
const B = "carried-b";

const turn = (text: string) => [{ role: "user", idx: 0, ts: "2026-11-16T05:00:00Z", text }];
const carried = (question: string) => ({
  kind: "question",
  questions: [{ question, options: [{ label: "Yes" }, { label: "No" }] }],
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let releaseB: (() => void) | null = null;

async function settle(rounds = 5) {
  for (let i = 0; i < rounds; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
}

async function until(cond: () => boolean, what: string) {
  for (let i = 0; i < 50; i++) {
    if (cond()) return;
    await settle(1);
  }
  throw new Error("timed out waiting for " + what);
}

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });

beforeEach(() => {
  localStorage.clear();
  setLocale("en");
  useWorkspaceStore.setState({ state: "running" });
  let gate: Promise<void> = new Promise((r) => (releaseB = r));
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes(`/sessions/${A}/messages`)) {
      return json({ cursor: 1, alive: false, status: "idle", messages: turn("hello from A"), carried: carried("Question left in A?") });
    }
    if (url.includes(`/sessions/${B}/messages`)) {
      await gate; // B's first response is held until the test releases it
      gate = Promise.resolve();
      return json({ cursor: 1, alive: false, status: "idle", messages: turn("hello from B"), carried: carried("Question left in B?") });
    }
    return json({});
  });
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  releaseB = null;
  vi.unstubAllGlobals();
});

function view(session: string) {
  const meta = { name: session, kind: "claude", alive: false } as unknown as Session;
  return (
    <ToastProvider>
      <ConfirmProvider>
        <MirrorView paneId="p1" session={session} sessionMeta={meta} active mirror onToggleMirror={() => {}} />
      </ConfirmProvider>
    </ToastProvider>
  );
}

const cardText = () => document.querySelector('[data-testid="carried-card"]')?.textContent ?? null;

describe("carried card across a session switch", () => {
  it("drops A's card on the switch and draws B's own once B answers", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(view(A)));
    await until(() => (cardText() ?? "").includes("Question left in A?"), "A's carried card");

    await act(async () => root!.render(view(B)));
    await settle();
    expect(cardText()).toBeNull();
    expect(document.body.textContent).not.toContain("Question left in A?");

    await act(async () => releaseB!());
    await until(() => (cardText() ?? "").includes("Question left in B?"), "B's carried card");
    expect(document.body.textContent).not.toContain("Question left in A?");
  });
});
