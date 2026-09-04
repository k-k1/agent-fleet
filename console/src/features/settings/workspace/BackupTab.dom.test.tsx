// Settings export / import tab (docs/log/79). Three things the UI has to hold, pinned in jsdom:
//   1. While the workspace is stopped, the agent-instructions category cannot be picked (it
//      lives in the Agent and is unreadable; letting it be picked would fake a successful run)
//   2. Only the categories actually present in the loaded file appear in the checklist
//   3. Import creates profiles before hosts and never recreates what already exists (the other
//      order 400s on the missing referent, and recreating would overwrite a live environment)
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const rawJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: () => Promise.resolve({}),
  raw: () => Promise.resolve(new Response("")),
  rawJSON: (...args: unknown[]) => rawJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
  errDetail: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  getTenant: () => "t1",
}));
let running = true;
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: running ? "running" : "stopped" }),
  wsStartBusy: () => false,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

import { BackupTab } from "./BackupTab.tsx";
import { BUNDLE_KIND, BUNDLE_VERSION } from "../../../lib/settingsBundle.ts";

const bundle = {
  kind: BUNDLE_KIND,
  version: BUNDLE_VERSION,
  exportedAt: "2026-08-26T00:00:00.000Z",
  sections: {
    prefs: { termSize: 15 },
    ssm: {
      profiles: [
        { label: "prod", startUrl: "https://c.awsapps.com/start", ssoRegion: "ap-northeast-1", accountId: "", roleName: "", region: "" },
        { label: "kept", startUrl: "https://c.awsapps.com/start", ssoRegion: "ap-northeast-1", accountId: "", roleName: "", region: "" },
      ],
      hosts: [{ alias: "mng@web-01", profile: "prod", instanceId: "i-1", documentName: "", region: "" }],
    },
  },
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<BackupTab />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

async function pickFile(text: string) {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]')!;
  const file = new File([text], "af-settings.json", { type: "application/json" });
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  await act(async () => {
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const picks = () => Array.from(document.querySelectorAll<HTMLInputElement>(".backup-preview input[type=checkbox]"));

beforeEach(() => {
  running = true;
  api.mockReset();
  rawJSON.mockReset();
  // Existing state: only the profile "kept" is registered, and there are no hosts.
  api.mockImplementation((path: string) => {
    if (path === "api/ssm/profiles") return Promise.resolve([{ id: "p-kept", label: "kept" }]);
    if (path === "api/ssm/hosts") return Promise.resolve([]);
    if (path === "api/user-notes") return Promise.resolve({ text: "hi", enabled: true, targets: [] });
    return Promise.resolve({});
  });
  rawJSON.mockImplementation((path: string) =>
    Promise.resolve(new Response(JSON.stringify({ id: "new-" + path }), { status: 201 })),
  );
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("BackupTab", () => {
  it("does not let the instructions category be picked while the workspace is stopped", async () => {
    running = false;
    await mount();
    const boxes = Array.from(document.querySelectorAll<HTMLInputElement>(".ds-group .backup-picks input[type=checkbox]"));
    expect(boxes).toHaveLength(3);
    expect(boxes[2].disabled).toBe(true); // the agent instructions
    expect(boxes[2].checked).toBe(false);
  });

  it("offers only the categories present in the file as import options", async () => {
    await mount();
    await pickFile(JSON.stringify(bundle));
    // prefs and ssm are present but instructions is not, so only 2 rows.
    expect(picks()).toHaveLength(2);
    // The export timestamp is formatted for display, never shown as the raw ISO string.
    const head = document.querySelector(".backup-preview")?.textContent ?? "";
    expect(head).toContain("2026");
    expect(head).not.toContain("2026-08-26T00:00:00.000Z");
  });

  it("creates profiles before hosts and does not recreate what already exists", async () => {
    await mount();
    await pickFile(JSON.stringify(bundle));
    const apply = Array.from(document.querySelectorAll<HTMLButtonElement>(".backup-preview button")).find(
      (b) => b.className.includes("primary"),
    )!;
    await act(async () => {
      apply.click();
    });
    await act(async () => {
      await Promise.resolve();
    });
    const calls = rawJSON.mock.calls.map((c) => [c[0], c[1], c[2]] as [string, string, any]);
    // "kept" already exists, so it is not created: one profile (prod), then the host.
    expect(calls.map((c) => c[0])).toEqual(["api/ssm/profiles", "api/ssm/hosts"]);
    expect(calls[0][2].label).toBe("prod");
    // The host references the newly issued id (the bundle only carries the display label).
    expect(calls[1][2].profileId).toBe("new-api/ssm/profiles");
    expect(document.querySelector(".backup-result")?.textContent).toBeTruthy();
  });

  it("does not proceed to import unless the file is a settings export", async () => {
    await mount();
    await pickFile(JSON.stringify({ kind: "something-else" }));
    expect(document.querySelector(".backup-preview")).toBeNull();
  });
});
