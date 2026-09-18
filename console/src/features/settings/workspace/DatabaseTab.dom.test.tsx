// DatabaseTab — the workspace settings "Databases" tab.
// What matters:
//   1. the engine list renders from GET /env/databases
//   2. the Start button POSTs /{engine}/start and triggers a reload
//   3. while state is "installing" or "starting", a 5-second poll drives GET
//      — and the poll keeps going even if state stays installing across ticks
//   4. lastError is shown inline under the engine row
//   5. one row per registered database, each copying ITS OWN URL — the card must
//      never offer a URL for a database that is not in the payload (ADR 0086,
//      first live run: the engine-level URL named the Agent's own directory)
//   6. create by name POSTs /{engine}/create?db=NAME, and refuses a name the
//      Agent would reject or one that is already on the engine — the tab is the
//      only way to make a database that is not named after a working copy
//   7. Delete POSTs /{engine}/drop?db=NAME for the row it sits on
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  getTenant: () => "default",
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));
// The tab reads the workspace state itself (it is mounted straight by
// SettingsDialog), so the mock has to be steerable per test.
let wsState = "running";
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: wsState, start: () => {} }),
  wsStartBusy: () => false,
}));
vi.mock("../../sessions/store.ts", () => ({
  useSessionsStore: (sel: (s: unknown) => unknown) => sel({ sessions: [] }),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
vi.mock("../hostUpdate.ts", () => ({ useHostUpdate: () => null }));

import { DatabaseTab } from "./DatabaseTab.tsx";

const pgRunning = {
  engine: "postgres",
  major: "17",
  installed: true,
  state: "running",
  version: "17.11",
  rssBytes: 47_000_000,
  port: 54321,
  datadir: "/tmp/data",
  databases: [
    {
      name: "af_agent_fleet_9af42b",
      dir: "/home/dev/repos/agent-fleet",
      urlSocket:
        "postgres://postgres:pw@/af_agent_fleet_9af42b?host=/tmp/run&port=54321&sslmode=disable",
      urlTcp: "postgres://postgres:pw@127.0.0.1:54321/af_agent_fleet_9af42b?sslmode=disable",
    },
  ],
  lastUsedAt: "",
  lastError: "",
};

const mysqlAbsent = {
  engine: "mysql",
  major: "8.4",
  installed: false,
  state: "absent",
  version: "",
  rssBytes: 0,
  port: 0,
  datadir: "",
  databases: [],
  lastUsedAt: "",
  lastError: "",
};

const mysqlInstalling = {
  ...mysqlAbsent,
  state: "installing",
};

const pgError = {
  ...pgRunning,
  state: "error",
  lastError: "failed to bind port",
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

// React installs its own value setter on the input element, so assigning .value
// directly leaves React's copy stale and the change event carries the old text.
// Go through the prototype setter, which is what React's onChange reads back.
function setValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  setter.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

async function mount(running = true) {
  wsState = running ? "running" : "stopped";
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<DatabaseTab />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  vi.useFakeTimers();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.useRealTimers();
});

describe("DatabaseTab", () => {
  it("renders engine rows from the GET response", async () => {
    api.mockResolvedValue({ engines: [pgRunning, mysqlAbsent] });
    await mount();
    const rows = document.querySelectorAll(".db-engine-row");
    expect(rows.length).toBe(2);
    // PostgreSQL running: state badge should say "running"
    expect(rows[0].querySelector(".db-engine-state")?.textContent).toMatch(/running|稼働中/);
    // MySQL absent: state badge should say "not installed"
    expect(rows[1].querySelector(".db-engine-state")?.textContent).toMatch(/not installed|未インストール/);
  });

  it("shows one row per database, copying that database's own URL", async () => {
    const second = {
      name: "af_other_111111",
      dir: "/home/dev/repos/other",
      urlSocket: "postgres://postgres:pw@/af_other_111111?host=/tmp/run&port=54321&sslmode=disable",
      urlTcp: "postgres://postgres:pw@127.0.0.1:54321/af_other_111111?sslmode=disable",
    };
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    api.mockResolvedValue({
      engines: [{ ...pgRunning, databases: [...pgRunning.databases, second] }],
    });
    await mount();

    const rows = document.querySelectorAll(".db-database");
    expect(rows.length).toBe(2);
    expect(rows[0].querySelector(".db-database-name")?.textContent).toBe("af_agent_fleet_9af42b");
    expect(rows[1].querySelector(".db-database-dir")?.textContent).toBe("/home/dev/repos/other");
    // The password is masked in what is shown, and each row shows its own database.
    const shown = rows[1].querySelector(".db-url-text")?.textContent || "";
    expect(shown).toContain("af_other_111111");
    expect(shown).not.toContain("pw@");

    // Copy hands over the raw URL of THAT row.
    await act(async () => {
      rows[1].querySelector<HTMLButtonElement>(".db-url-copy")!.click();
    });
    expect(writeText).toHaveBeenCalledWith(second.urlSocket);
  });

  it("Reset targets the row's own database (never the agent's own directory)", async () => {
    api.mockResolvedValue({ engines: [pgRunning] });
    apiJSON.mockResolvedValue({ status: pgRunning });
    await mount();

    const row = document.querySelector(".db-database")!;
    const reset = Array.from(row.querySelectorAll<HTMLButtonElement>("button")).find((b) =>
      /Reset|リセット/.test(b.textContent || ""),
    );
    expect(reset).toBeTruthy();
    await act(async () => {
      reset!.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    const call = apiJSON.mock.calls.find((c) => String(c[0]).includes("/reset"));
    expect(call).toBeTruthy();
    // The name rides in the query string; without it the Agent would reset a
    // database derived from its own working directory.
    expect(String(call![0])).toContain("db=af_agent_fleet_9af42b");
  });

  it("says so when a running engine has no database yet", async () => {
    api.mockResolvedValue({ engines: [{ ...pgRunning, databases: [] }] });
    await mount();
    expect(document.querySelectorAll(".db-database").length).toBe(0);
    expect(document.querySelector(".db-engine-empty")?.textContent).toMatch(/af-db url/);
  });

  it("shows a 'stopped' message when the workspace is not running", async () => {
    await mount(false);
    expect(host!.textContent).toMatch(/Start the workspace|ワークスペースを起動/);
    expect(api).not.toHaveBeenCalled();
  });

  it("Create POSTs /{engine}/create with the typed name and clears the field", async () => {
    api.mockResolvedValue({ engines: [pgRunning] });
    apiJSON.mockResolvedValue({ status: pgRunning });
    await mount();

    const input = document.querySelector<HTMLInputElement>(".db-create-name")!;
    const create = document.querySelector<HTMLButtonElement>(".db-btn-create")!;
    // Nothing typed yet: there is no database to create.
    expect(create.disabled).toBe(true);

    await act(async () => {
      setValue(input, "scratch_db");
    });
    expect(create.disabled).toBe(false);
    await act(async () => {
      create.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    const call = apiJSON.mock.calls.find((c) => String(c[0]).includes("/create"));
    expect(call).toBeTruthy();
    expect(String(call![0])).toContain("api/env/databases/postgres/create?db=scratch_db");
    // A successful create empties the field, so a second press cannot repeat it.
    expect(document.querySelector<HTMLInputElement>(".db-create-name")!.value).toBe("");
  });

  it("refuses a name the Agent would reject, and one that already exists", async () => {
    api.mockResolvedValue({ engines: [pgRunning] });
    await mount();

    const input = document.querySelector<HTMLInputElement>(".db-create-name")!;
    const create = document.querySelector<HTMLButtonElement>(".db-btn-create")!;

    // Capitals and a leading digit both fail ^[a-z_][a-z0-9_]{0,62}$.
    await act(async () => {
      setValue(input, "9bad-Name");
    });
    expect(create.disabled).toBe(true);
    expect(document.querySelector(".db-create-bad")).not.toBeNull();

    // A valid name that is already on this engine: the create would be a no-op.
    await act(async () => {
      setValue(input, "af_agent_fleet_9af42b");
    });
    expect(create.disabled).toBe(true);
    expect(document.querySelector(".db-create-bad")).not.toBeNull();

    // Nothing was sent for either.
    expect(apiJSON.mock.calls.filter((c) => String(c[0]).includes("/create")).length).toBe(0);
  });

  it("Delete POSTs /{engine}/drop for the row it sits on", async () => {
    api.mockResolvedValue({ engines: [pgRunning] });
    apiJSON.mockResolvedValue({ status: pgRunning });
    await mount();

    const row = document.querySelector(".db-database")!;
    const drop = row.querySelector<HTMLButtonElement>(".db-btn-drop");
    expect(drop).toBeTruthy();
    await act(async () => {
      drop!.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    const call = apiJSON.mock.calls.find((c) => String(c[0]).includes("/drop"));
    expect(call).toBeTruthy();
    expect(String(call![0])).toContain("db=af_agent_fleet_9af42b");
  });

  it("Start button POSTs to /{engine}/start and triggers a reload", async () => {
    api.mockResolvedValue({ engines: [mysqlAbsent] });
    apiJSON.mockResolvedValue({ engines: [{ ...mysqlAbsent, state: "starting" }] });
    await mount();

    const btn = document.querySelector<HTMLButtonElement>(".db-btn");
    expect(btn).not.toBeNull();
    // MySQL absent: label should say "Install and start"
    expect(btn!.textContent).toMatch(/Install and start|導入して起動/);

    // api() is also used for the reload after the POST — reset before click
    api.mockResolvedValue({ engines: [{ ...mysqlAbsent, state: "starting" }] });
    await act(async () => {
      btn!.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(apiJSON).toHaveBeenCalledWith(
      expect.stringMatching(/api\/env\/databases\/mysql\/start/),
      "POST",
      {},
    );
    // After the POST, a reload (api GET) is triggered
    expect(api).toHaveBeenCalledWith("api/env/databases");
  });

  it("starts a 5-second poll while state is installing or starting", async () => {
    api.mockResolvedValue({ engines: [mysqlInstalling] });
    await mount();

    let getCount = api.mock.calls.filter((c) => c[0] === "api/env/databases").length;
    expect(getCount).toBe(1);

    // Advance 5 s — the interval fires and triggers a reload
    api.mockResolvedValue({ engines: [{ ...mysqlInstalling, state: "running" }] });
    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    await act(async () => {
      await Promise.resolve();
    });

    getCount = api.mock.calls.filter((c) => c[0] === "api/env/databases").length;
    expect(getCount).toBeGreaterThan(1);
  });

  it("poll keeps going while state stays installing — 3 ticks produce at least 4 GETs", async () => {
    // Installing state that never changes — the interval must keep re-arming.
    api.mockResolvedValue({ engines: [mysqlInstalling] });
    await mount();

    const countAfterMount = api.mock.calls.filter((c) => c[0] === "api/env/databases").length;
    expect(countAfterMount).toBe(1);

    // Advance 15 s (3 × 5 s intervals), still returning installing each time.
    await act(async () => {
      vi.advanceTimersByTime(15_000);
    });
    await act(async () => { await Promise.resolve(); });
    await act(async () => { await Promise.resolve(); });
    await act(async () => { await Promise.resolve(); });

    const getCount = api.mock.calls.filter((c) => c[0] === "api/env/databases").length;
    expect(getCount).toBeGreaterThanOrEqual(4); // 1 initial + ≥ 3 interval fires
  });

  it("poll stops after unmount — no further GETs after the component is removed", async () => {
    api.mockResolvedValue({ engines: [mysqlInstalling] });
    await mount();
    const countBefore = api.mock.calls.length;

    // Unmount before the timer fires
    await act(() => root?.unmount());
    root = null;

    await act(async () => {
      vi.advanceTimersByTime(10_000);
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(api.mock.calls.length).toBe(countBefore);
  });

  it("shows lastError inline under the engine row", async () => {
    api.mockResolvedValue({ engines: [pgError] });
    await mount();
    const errorEl = document.querySelector(".db-engine-error");
    expect(errorEl).not.toBeNull();
    expect(errorEl!.textContent).toContain("failed to bind port");
  });
});
