// Showing "when it stops / why it will not" on the roster (docs/log/75 P4).
//
// This surface exists because an operator had nothing to look at when auto-stop was not
// firing. Three things are pinned:
//   1. "will not stop" must read differently from "no schedule yet".
//   2. Whatever is holding it open (session name, pin, a watcher) has to be named.
//   3. A Workspace that is not running gets no stop schedule at all.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { MembersPanel } from "./tenantMembers.tsx";

const SIZING = { mem_meaning: "cap", cpu_effective: true };
const inHours = (h: number) => new Date(Date.now() + h * 3600_000).toISOString();

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mountRoster(members: unknown[]) {
  api.mockImplementation((p: string) =>
    p === "api/admin/workspace-sizing" ? Promise.resolve(SIZING) : Promise.resolve({ members }),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<MembersPanel slug="acme" isSuper={false} onOpenMember={() => {}} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const rowText = (i: number) => (document.querySelectorAll(".member-row")[i]?.textContent || "").replace(/\s+/g, " ");

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("auto-stop outlook on the member roster", () => {
  it("shows a scheduled stop as time remaining", async () => {
    await mountRoster([
      {
        user_key: "a",
        role: "member",
        state: "running",
        idle: { enabled: true, stopAt: inHours(1.5), observedAt: new Date().toISOString() },
      },
    ]);
    expect(rowText(0)).toMatch(/1h(2[0-9]|30)m/); // "stops in 1h30m"
  });

  it("names the reason and the holder on a row that will not stop, instead of a time", async () => {
    await mountRoster([
      {
        user_key: "a",
        role: "member",
        state: "running",
        idle: {
          enabled: true,
          stopAt: inHours(1),
          holders: [{ kind: "working", session: "s5" }, { kind: "watching" }],
          observedAt: new Date().toISOString(),
        },
      },
    ]);
    const t = rowText(0);
    expect(t).toContain("s5"); // who is holding it open
    expect(t).not.toMatch(/1h/); // no schedule: showing one reads as "about to stop"
    expect(document.querySelector(".mr-idle.hold")).toBeTruthy(); // warning colour: cost keeps accruing
  });

  it("says disabled for a tenant with auto-stop off, distinct from having no schedule", async () => {
    await mountRoster([
      { user_key: "a", role: "member", state: "running", idle: { enabled: false, observedAt: new Date().toISOString() } },
    ]);
    expect(document.querySelector(".mr-idle")?.textContent).toBeTruthy();
    expect(document.querySelector(".mr-idle.hold")).toBeFalsy();
  });

  it("shows nothing for a stopped Workspace, which has no stop schedule to show", async () => {
    await mountRoster([
      {
        user_key: "a",
        role: "member",
        state: "stopped",
        idle: { enabled: true, stopAt: inHours(1), observedAt: new Date().toISOString() },
      },
      { user_key: "b", role: "member", state: "running" }, // a row with no observation yet
    ]);
    expect(document.querySelectorAll(".mr-idle").length).toBe(0);
  });
});
