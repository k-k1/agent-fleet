// The agent instructions tab (docs/log/60). What it has to protect is that the screen cannot
// be misread, and only those three points are pinned down in jsdom:
//   1. An unsupported kind stays as a row and its reason is readable (dropping it silently
//      reads as a gap in coverage).
//   2. "Written" and "in effect" are shown separately (a save can succeed with no effect).
//   3. Over the limit, saving is blocked (the Agent also rejects it with 400, but the screen
//      stops it first).
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) =>
    sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { InstructionsTab } from "./InstructionsTab.tsx";

const payload = {
  text: "always speak Japanese\n",
  bytes: 21,
  max_bytes: 64,
  enabled: true,
  path: "/home/dev/.config/agent-fleet/user-notes.md",
  fleet_bytes: 29521,
  targets: [
    {
      kind: "claude",
      supported: true,
      on: true,
      applied: true,
      delivery: "file",
      path: "/var/lib/af/claude/CLAUDE.md",
    },
    {
      kind: "opencode",
      supported: true,
      on: true,
      applied: false,
      delivery: "config",
      path: "/home/dev/.config/agent-fleet/instructions/opencode.md",
      error: "config_unreadable",
    },
    {
      kind: "cursor",
      supported: false,
      on: false,
      applied: false,
      reason: "no_user_scope",
    },
  ],
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<InstructionsTab />);
  });
  // Flush one round of useRetryLoad's resolution.
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  api.mockResolvedValue(payload);
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const rows = () =>
  Array.from(
    document.querySelectorAll<HTMLTableRowElement>(".instr-targets tr"),
  );

describe("InstructionsTab", () => {
  it("keeps unsupported kinds and shows them with a reason", async () => {
    await mount();
    expect(rows()).toHaveLength(3);
    const unsupported = document.querySelector(".instr-unsupported");
    expect(unsupported).not.toBeNull();
    // The reason appears as prose; the raw code carries no meaning to the reader.
    expect(
      unsupported?.querySelector(".instr-badge")?.textContent,
    ).toBeTruthy();
    expect(unsupported?.textContent).not.toContain("no_user_scope");
    // No toggle on an unsupported row: something that looks pressable reads as "this works".
    expect(unsupported?.querySelector(".choice-seg")).toBeNull();
  });

  it("distinguishes written from in effect per row", async () => {
    await mount();
    expect(document.querySelectorAll(".instr-ok")).toHaveLength(1); // claude
    const fail = document.querySelector(".instr-fail");
    expect(fail).not.toBeNull();
    expect(fail?.textContent).toBeTruthy();
    expect(fail?.textContent).not.toContain("config_unreadable"); // never show the raw code
  });

  it("blocks saving once the limit is exceeded", async () => {
    await mount();
    const ta = document.querySelector<HTMLTextAreaElement>("#instr-body")!;
    const setter = Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype,
      "value",
    )!.set!;
    await act(async () => {
      setter.call(ta, "x".repeat(payload.max_bytes + 1));
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const save = Array.from(
      document.querySelectorAll<HTMLButtonElement>(".instr-actions .ui-btn"),
    )[0];
    expect(save.disabled).toBe(true);
    expect(document.querySelector(".instr-over")).not.toBeNull();
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("PUTs the body on save", async () => {
    apiJSON.mockResolvedValue({ ...payload, text: "short\n" });
    await mount();
    const ta = document.querySelector<HTMLTextAreaElement>("#instr-body")!;
    const setter = Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype,
      "value",
    )!.set!;
    await act(async () => {
      setter.call(ta, "short\n");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const save = Array.from(
      document.querySelectorAll<HTMLButtonElement>(".instr-actions .ui-btn"),
    )[0];
    await act(async () => {
      save.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/user-notes", "PUT", {
      text: "short\n",
    });
  });
});
