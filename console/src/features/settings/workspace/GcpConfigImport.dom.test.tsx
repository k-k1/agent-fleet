// The gcloud configuration import dialog (issue #3017, part 2), in jsdom. What it has to hold:
//   1. No path, token file or unknown key from a file reaches a request, the page or the console.
//   2. The pasted text is not kept once read.
//   3. A failed read of the current profiles sends nothing.
//   4. Every configuration gets a row, with a reason when it cannot be imported.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const rawJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  rawJSON: (...a: unknown[]) => rawJSON(...a),
}));

import { GcpConfigImport } from "./GcpConfigImport.tsx";

const SENTINELS = ["CREDFILESENTINEL", "TOKENFILESENTINEL", "UNKNOWNKEYSENTINEL", "UNKNOWNVALSENTINEL", "PROXYSENTINEL"];
const GOOD = `[core]
account = me@example.com
project = my-project-123
UNKNOWNKEYSENTINEL = UNKNOWNVALSENTINEL
[compute]
region = asia-northeast1
`;
const FILES: [string, string][] = [
  ["config_good", GOOD],
  ["config_cred", "[core]\nproject = my-project-123\n[auth]\ncredential_file_override = /home/u/CREDFILESENTINEL.json\n"],
  ["config_token", "[core]\nproject = my-project-123\n[auth]\naccess_token_file = /home/u/TOKENFILESENTINEL\n"],
  ["config_proxy", "[core]\nproject = my-project-123\n[proxy]\naddress = PROXYSENTINEL\n"],
  ["config_sa", "[core]\nproject = my-project-123\naccount = sa@my-project-123.iam.gserviceaccount.com\n"],
  ["active_config", "good\n"],
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let onImported = vi.fn();
const logs: string[] = [];

async function flush() {
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
}

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  onImported = vi.fn();
  await act(async () => {
    root!.render(<GcpConfigImport onClose={() => {}} onImported={onImported} />);
  });
}

function setValue(el: HTMLInputElement | HTMLTextAreaElement, v: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, v);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

async function chooseFiles(files: [string, string][]) {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]')!;
  const list = files.map(([n, t]) => new File([t], n));
  Object.defineProperty(input, "files", { value: list, configurable: true });
  await act(async () => {
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
}

async function paste(text: string, name?: string) {
  if (name !== undefined) await act(async () => setValue(document.querySelector<HTMLInputElement>("label.ssm-fld input")!, name));
  await act(async () => setValue(document.querySelector("textarea")!, text));
  const btn = [...document.querySelectorAll("button")].find((b) => b.textContent === "読み込む" || b.textContent === "Read")!;
  await act(async () => btn.click());
  await flush();
}

const rowEl = (name: string) =>
  [...document.querySelectorAll<HTMLElement>("li.aws-import-row")].find((li) => li.querySelector(".ssm-alias")?.textContent === name)!;
const importBtn = () => [...document.querySelectorAll<HTMLButtonElement>("button.primary")].at(-1)!;

beforeEach(() => {
  api.mockReset();
  rawJSON.mockReset();
  api.mockResolvedValue([]);
  let n = 0;
  rawJSON.mockImplementation(async () => new Response(JSON.stringify({ id: "id" + ++n }), { status: 201 }));
  logs.length = 0;
  for (const m of ["log", "info", "warn", "error", "debug"] as const) {
    vi.spyOn(console, m).mockImplementation((...a: unknown[]) => void logs.push(a.map(String).join(" ")));
  }
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("GcpConfigImport", () => {
  it("lists every file with its status and lets no sentinel reach a request, the page or the console", async () => {
    await mount();
    await chooseFiles(FILES);
    expect(rowEl("good").className).toContain("create");
    for (const n of ["cred", "token", "proxy", "sa"]) expect(rowEl(n).className, n).toContain("blocked");
    expect(rowEl("cred").textContent).toContain("auth/credential_file_override");
    expect(rowEl("token").textContent).toContain("auth/access_token_file");
    // active_config is not a configuration: its row goes by position, not by file name.
    expect(document.querySelectorAll("li.aws-import-row")).toHaveLength(6);
    await act(async () => importBtn().click());
    await flush();
    expect(rawJSON).toHaveBeenCalledTimes(1);
    expect(rawJSON.mock.calls[0][2]).toEqual({
      loginMethod: "google",
      label: "good",
      project: "my-project-123",
      quotaProject: "",
      account: "me@example.com",
      region: "asia-northeast1",
      zone: "",
      impersonateServiceAccount: "",
    });
    const seen = JSON.stringify(rawJSON.mock.calls) + document.body.innerHTML + logs.join("\n") + JSON.stringify(api.mock.calls);
    for (const s of SENTINELS) expect(seen, s).not.toContain(s);
    expect(onImported).toHaveBeenCalled();
  });

  it("drops pasted text once read and names the configuration from the name field", async () => {
    await mount();
    await paste(GOOD, "prod");
    expect(document.querySelector("textarea")).toBeNull();
    expect(rowEl("prod").className).toContain("create");
    expect(document.body.innerHTML).not.toContain("UNKNOWNVALSENTINEL");
  });

  it("sends nothing when the current profiles cannot be read", async () => {
    api.mockResolvedValue({ error: { code: "internal" } });
    await mount();
    await paste(GOOD, "prod");
    expect(document.querySelector('[role="alert"]')?.textContent).toBeTruthy();
    expect(rawJSON).not.toHaveBeenCalled();
  });

  it("re-reads the profiles before sending, so a row created meanwhile is not created twice", async () => {
    await mount();
    await paste(GOOD, "prod");
    api.mockResolvedValue([{ id: "1", label: "prod", name: "prod", loginMethod: "google", project: "my-project-123", quotaProject: "", account: "me@example.com", region: "asia-northeast1", zone: "", impersonateServiceAccount: "" }]);
    await act(async () => importBtn().click());
    await flush();
    expect(rawJSON).not.toHaveBeenCalled();
  });

  it("marks a row that already exists and keeps a taken label out", async () => {
    api.mockResolvedValue([{ id: "1", label: "good", name: "good", loginMethod: "google", project: "other-project-9", quotaProject: "", account: "", region: "", zone: "", impersonateServiceAccount: "" }]);
    await mount();
    await chooseFiles([["config_good", GOOD]]);
    expect(rowEl("good").className).toContain("blocked");
    expect(importBtn().disabled).toBe(true);
  });
});
