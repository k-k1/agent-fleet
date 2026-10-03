// #1031: peer messages the Agent holds until the member answers the session's pending question
// are shown above the composer, per sender, and each can be dropped. The whole view is mounted
// so the wiring from the messages payload to the notice and back to the drop endpoint is what
// is tested.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { MirrorView } from "./MirrorView.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { setLocale } from "../../lib/i18n/index.ts";
import type { Session } from "../../types/session.ts";

const SESSION = "peer-pending-session";
const TURNS = [{ role: "assistant", idx: 1, parts: [{ kind: "text", text: "Which branch?" }] }];

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let pending: unknown[] = [];
let deletes: string[] = [];

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

beforeEach(() => {
  localStorage.clear();
  setLocale("en");
  useWorkspaceStore.setState({ state: "running" });
  deletes = [];
  pending = [
    { id: "af_1", from: "reviewer", intent: "request", blockedOn: "question", queuedAt: "2026-10-03T01:00:00Z", excerpt: "Rebase onto develop" },
    { id: "af_2", from: "reviewer", intent: "notice", blockedOn: "question", queuedAt: "2026-10-03T01:01:00Z", excerpt: "CI is green" },
    { id: "af_3", from: "builder", intent: "notice", blockedOn: "question", queuedAt: "2026-10-03T01:02:00Z", excerpt: "Image pushed" },
  ];
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (init?.method === "DELETE" && url.includes(`/sessions/${SESSION}/pending-peer/`)) {
      const id = decodeURIComponent(url.split("/pending-peer/")[1]);
      deletes.push(id);
      pending = pending.filter((p) => (p as { id: string }).id !== id);
      return new Response(JSON.stringify({ dropped: id }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    const body = url.includes(`/sessions/${SESSION}/messages`)
      ? { cursor: 1, alive: true, status: "question", messages: TURNS, pendingPeers: pending }
      : {};
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.unstubAllGlobals();
});

async function mount() {
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
}

describe("peer messages waiting for the member's answer", () => {
  it("says how many messages from each sender follow the answer", async () => {
    await mount();
    await until(() => !!document.querySelector(".mirror-peer-pending"), "the waiting-messages notice");
    const heads = [...document.querySelectorAll(".mirror-peer-pending .mpp-msg")].map((e) => e.textContent);
    expect(heads).toEqual([
      "2 messages from reviewer will be delivered after you answer.",
      "1 message from builder will be delivered after you answer.",
    ]);
    const texts = [...document.querySelectorAll(".mirror-peer-pending .mpp-text")].map((e) => e.textContent);
    expect(texts).toEqual(["Rebase onto develop", "CI is green", "Image pushed"]);
  });

  it("drops one message through the Agent and stops showing it", async () => {
    await mount();
    await until(() => document.querySelectorAll(".mirror-peer-pending .mpp-drop").length === 3, "three drop buttons");
    await act(async () => document.querySelectorAll<HTMLButtonElement>(".mirror-peer-pending .mpp-drop")[1].click());
    await settle();
    expect(deletes).toEqual(["af_2"]);
    const texts = [...document.querySelectorAll(".mirror-peer-pending .mpp-text")].map((e) => e.textContent);
    expect(texts).toEqual(["Rebase onto develop", "Image pushed"]);
    expect(document.querySelector(".mirror-peer-pending .mpp-msg")!.textContent).toBe(
      "1 message from reviewer will be delivered after you answer.",
    );
  });

  it("shows nothing when no message waits", async () => {
    pending = [];
    await mount();
    await until(() => document.body.textContent!.includes("Which branch?"), "the transcript");
    expect(document.querySelector(".mirror-peer-pending")).toBeNull();
  });
});
