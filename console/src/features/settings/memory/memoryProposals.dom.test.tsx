// The background review's proposals (ADR 0117 decision 6): what the member sees, and that every
// decision goes to the Agent's member-only routes with the id in the query (the audit ledger reads
// the URL only). A proposal that cannot be approved offers no approve button; one the Agent
// withheld shows no name, text or diff.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { ProposedPanel } from "./memoryProposals.tsx";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const ID1 = "0123456789abcdef0123";
const ID2 = "1123456789abcdef0123";
const ID3 = "2123456789abcdef0123";
const RUN1 = "aaaaaaaaaaaaaaaaaaaa";
const RUN2 = "bbbbbbbbbbbbbbbbbbbb";

const base = {
  session: "sess-a",
  scope: "project",
  projectId: "app-1",
  project: "app",
  reason: "the user corrected this twice",
  evidence: [3],
  created: "2026-10-19T09:00:00Z",
  diff: "diff --git a/af/x b/af/x\n+added line",
};
const proposals = [
  { ...base, id: ID1, runId: RUN1, op: "create", name: "build-cmd", state: "pending" },
  { ...base, id: ID2, runId: RUN1, op: "update", name: "style", state: "stale", staleReason: "changed" },
  // A withheld proposal carries no text from the Agent; the fixture puts some in on purpose, to pin
  // that the screen does not print what the flag says to hold back.
  { ...base, id: ID3, runId: RUN2, op: "update", name: "SENTINEL-NAME", state: "stale", staleReason: "secret", withheld: true, reason: "SENTINEL-REASON", diff: "SENTINEL-DIFF" },
];

const flush = () =>
  act(async () => {
    for (let i = 0; i < 4; i++) await Promise.resolve();
  });

let changed = 0;
async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ProposedPanel reload={0} onChanged={() => void changed++} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
}

const click = async (b: Element | null | undefined) => {
  expect(b).toBeTruthy();
  await act(async () => (b as HTMLElement).click());
  await flush();
};
const confirmButton = () => document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:last-child");
const headButtons = () => Array.from(host!.querySelectorAll<HTMLButtonElement>(".mem-diff-head button"));
const rows = () => Array.from(host!.querySelectorAll<HTMLButtonElement>(".mem-list .mem-snap"));

beforeEach(() => {
  changed = 0;
  apiJSON.mockImplementation((path: string) => {
    if (path === "api/agents/memory-review/proposals/list") return Promise.resolve({ proposals });
    return Promise.resolve({});
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  api.mockReset();
  apiJSON.mockReset();
  document.body.innerHTML = "";
});

describe("ProposedPanel", () => {
  it("lists the queue through the member-only route and shows the diff and the reason", async () => {
    await mount();
    expect(apiJSON.mock.calls[0]).toEqual(["api/agents/memory-review/proposals/list", "POST", {}]);
    expect(rows()).toHaveLength(3);
    expect(rows()[0].textContent).toContain("build-cmd");
    expect(host!.textContent).toContain("the user corrected this twice");
    expect(host!.querySelector(".mem-diff")?.textContent).toContain("+added line");
  });

  it("approves with the id in the query and the body, then reads the queue again", async () => {
    await mount();
    const approve = headButtons()[0];
    await click(approve);
    const call = apiJSON.mock.calls.find(([p]) => String(p).includes("/approve"));
    expect(call).toEqual([`api/agents/memory-review/proposals/approve?id=${ID1}`, "POST", { id: ID1 }]);
    expect(changed).toBe(1);
    expect(apiJSON.mock.calls.filter(([p]) => p === "api/agents/memory-review/proposals/list").length).toBeGreaterThan(1);
  });

  it("offers no approve on a stale proposal, only reject, and says why", async () => {
    await mount();
    await click(rows()[1]);
    expect(headButtons()).toHaveLength(1); // reject
    expect(host!.querySelector(".mem-diff-head .mem-warn")).toBeTruthy();
    await click(headButtons()[0]);
    expect(apiJSON.mock.calls.some(([p, , b]) => p === `api/agents/memory-review/proposals/reject?id=${ID2}` && b.id === ID2)).toBe(true);
    expect(apiJSON.mock.calls.some(([p]) => String(p).includes("/approve"))).toBe(false);
  });

  it("shows nothing of a withheld proposal but still lets it be rejected", async () => {
    await mount();
    await click(rows()[2]);
    expect(host!.textContent).not.toContain("SENTINEL");
    expect(host!.querySelector(".mem-diff pre, .mem-diff .diff")).toBeNull();
    expect(host!.querySelector(".mem-prop-why")).toBeNull();
    expect(headButtons()).toHaveLength(1);
  });

  it("keeps the proposal listed and says so when the Agent refuses the approval", async () => {
    apiJSON.mockImplementation((path: string) => {
      if (path === "api/agents/memory-review/proposals/list") return Promise.resolve({ proposals });
      if (String(path).includes("/approve")) return Promise.resolve({ error: { code: "proposal_stale", message: "the memory is not as it was" } });
      return Promise.resolve({});
    });
    await mount();
    await click(headButtons()[0]);
    expect(document.body.textContent).toContain("the memory is not as it was");
    expect(rows()).toHaveLength(3);
    expect(changed).toBe(0);
  });

  it("rejects all after a confirmation, and one run's proposals on their own", async () => {
    await mount();
    const bulk = Array.from(host!.querySelectorAll<HTMLButtonElement>(".mem-prop-bulk button"));
    expect(bulk).toHaveLength(2); // this run's, and all
    await click(bulk[0]);
    expect(apiJSON.mock.calls.some(([p]) => String(p).includes("reject-all"))).toBe(false); // not before confirming
    await click(confirmButton());
    expect(apiJSON.mock.calls.find(([p]) => String(p).includes("reject-all"))).toEqual([
      `api/agents/memory-review/proposals/reject-all?run=${RUN1}`,
      "POST",
      { run: RUN1 },
    ]);
    apiJSON.mockClear();
    await click(Array.from(host!.querySelectorAll<HTMLButtonElement>(".mem-prop-bulk button"))[1]);
    await click(confirmButton());
    expect(apiJSON.mock.calls.find(([p]) => String(p).includes("reject-all"))).toEqual([
      "api/agents/memory-review/proposals/reject-all",
      "POST",
      { run: "" },
    ]);
  });

  it("does not reject anything when the confirmation is cancelled", async () => {
    await mount();
    await click(host!.querySelectorAll<HTMLButtonElement>(".mem-prop-bulk button")[1]);
    await click(document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:first-child"));
    expect(apiJSON.mock.calls.some(([p]) => String(p).includes("reject-all"))).toBe(false);
  });

  it("says a failed read is a failure and an empty queue is empty", async () => {
    apiJSON.mockResolvedValue({ error: { code: "forbidden", message: "nope" } });
    await mount();
    expect(host!.querySelector(".mem-warn")).toBeTruthy();
    act(() => root?.unmount());
    host?.remove();
    apiJSON.mockResolvedValue({ proposals: [] });
    await mount();
    expect(host!.querySelector(".mem-warn")).toBeNull();
    expect(rows()).toHaveLength(0);
    expect(host!.querySelector(".mem-prop-bulk")).toBeNull();
  });

  it("reports a request that may not have arrived and reads the queue again instead of throwing", async () => {
    apiJSON.mockImplementation((path: string) => {
      if (path === "api/agents/memory-review/proposals/list") return Promise.resolve({ proposals });
      return Promise.reject(new TypeError("network down"));
    });
    const unhandled = vi.fn();
    window.addEventListener("unhandledrejection", unhandled);
    await mount();
    const before = apiJSON.mock.calls.length;
    await click(headButtons()[0]);
    await flush();
    expect(apiJSON.mock.calls.length).toBeGreaterThan(before + 1);
    window.removeEventListener("unhandledrejection", unhandled);
    expect(unhandled).not.toHaveBeenCalled();
  });
});
