// ADR 0126 decision 2: af-aws-exec runs that reported this session come in the messages payload
// as `cloudRuns` and are drawn in the conversation at the time they started. The whole view is
// mounted so the wiring from the payload to the timeline is what is tested.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { MirrorView } from "./MirrorView.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { setLocale } from "../../lib/i18n/index.ts";
import type { Session } from "../../types/session.ts";

const SESSION = "cloud-runs-session";
const TURNS = [
  { role: "user", idx: 0, ts: "2026-10-30T12:00:00Z", text: "list the buckets" },
  { role: "assistant", idx: 1, ts: "2026-10-30T12:00:05Z", parts: [{ kind: "text", text: "Listing them now." }] },
  { role: "user", idx: 2, ts: "2026-10-30T12:10:00Z", text: "thanks" },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let runs: unknown[] | undefined;

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
  runs = [
    {
      id: "1793366109333295724-c19c781c",
      provider: "aws",
      session: SESSION,
      startedAt: "2026-10-30T12:05:00Z",
      endedAt: "2026-10-30T12:05:03Z",
      profile: "prod-admin",
      account: "123456789012",
      role: "AdministratorAccess",
      region: "ap-northeast-1",
      program: "aws",
      service: "s3",
      operation: "ls",
      exitCode: 0,
      durationMs: 3000,
    },
  ];
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = String(input);
    const body = url.includes(`/sessions/${SESSION}/messages`)
      ? { cursor: 3, alive: true, status: "idle", messages: TURNS, ...(runs ? { cloudRuns: runs } : {}) }
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

describe("af-aws-exec runs in the mirror", () => {
  it("draws a run between the turns it happened between, with its identity", async () => {
    await mount();
    await until(() => !!document.querySelector(".cloud-run"), "the run row");
    const row = document.querySelector(".cloud-run")!.textContent!;
    expect(row).toContain("prod-admin");
    expect(row).toContain("123456789012 / AdministratorAccess");
    expect(row).toContain("aws s3 ls");
    const text = document.body.textContent!;
    expect(text.indexOf("123456789012")).toBeGreaterThan(text.indexOf("Listing them now."));
    expect(text.indexOf("123456789012")).toBeLessThan(text.indexOf("thanks"));
  });

  it("draws nothing when the session has no runs", async () => {
    runs = undefined;
    await mount();
    await until(() => document.body.textContent!.includes("thanks"), "the transcript");
    expect(document.querySelector(".cloud-run")).toBeNull();
  });
});
