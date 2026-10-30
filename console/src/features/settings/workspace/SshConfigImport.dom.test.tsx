// The ~/.ssh/config import dialog (issue #3017, part 1), in jsdom. What it has to hold:
//   1. No part of a ProxyCommand, an IdentityFile path or another secret reaches a request, the
//      rendered preview or the console.
//   2. The pasted text is not kept once read.
//   3. A failed read of the current lists sends nothing.
//   4. The preview says the connection method changes, and a created host carries no SSH settings.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const rawJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  rawJSON: (...a: unknown[]) => rawJSON(...a),
}));

import { SshConfigImport } from "./SshConfigImport.tsx";

const I1 = "i-0123456789abcdef0";
const SENTINELS = ["TOKENSENTINEL", "KEYPATHSENTINEL", "ENVSENTINEL", "PROCSENTINEL"];
const CONFIG = `Host web
  HostName ${I1}
  User ec2-user
  IdentityFile /home/me/KEYPATHSENTINEL
  ProxyCommand aws ssm start-session --target %h --document-name AWS-StartSSHSession --parameters portNumber=%p --profile prod --region ap-northeast-1

Host leak
  HostName ${I1}
  ProxyCommand aws ssm start-session --target %h --document-name AWS-StartSSHSession --token TOKENSENTINEL

Host tunnel
  HostName ${I1}
  ProxyCommand nc %h PROCSENTINEL
  SetEnv AWS_SECRET_ACCESS_KEY=ENVSENTINEL

Host *
  ServerAliveInterval 30
`;
const PROFILES = [{ id: "p1", label: "prod", kind: "sso", startUrl: "https://x", ssoRegion: "us-east-1", accountId: "1", roleName: "R" }];

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
    root!.render(<SshConfigImport onClose={() => {}} onImported={onImported} />);
  });
}

function setValue(el: HTMLInputElement | HTMLTextAreaElement, v: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, v);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

async function paste(text: string) {
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
  api.mockImplementation((p: string) => Promise.resolve(p.endsWith("profiles") ? PROFILES : []));
  rawJSON.mockImplementation(async () => new Response(JSON.stringify({ id: "h1" }), { status: 201 }));
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

describe("SshConfigImport", () => {
  it("never lets a command, key path or secret reach a request, the page or the console", async () => {
    await mount();
    await paste(CONFIG);
    expect(document.querySelector("textarea")).toBeNull();
    expect(rowEl("web").className).toContain("create");
    expect(rowEl("leak").className).toContain("blocked");
    expect(rowEl("tunnel").className).toContain("blocked");
    await act(async () => importBtn().click());
    await flush();
    expect(rawJSON).toHaveBeenCalledTimes(1);
    expect(rawJSON.mock.calls[0][0]).toBe("api/ssm/hosts");
    // Exactly the four host fields (plus the profile id): nothing of the SSH entry.
    expect(rawJSON.mock.calls[0][2]).toEqual({ alias: "web", profileId: "p1", instanceId: I1, documentName: "", region: "ap-northeast-1" });
    const seen = JSON.stringify(rawJSON.mock.calls) + document.body.innerHTML + logs.join("\n") + JSON.stringify(api.mock.calls);
    for (const s of SENTINELS) expect(seen).not.toContain(s);
    expect(seen).not.toContain("portNumber");
    expect(JSON.stringify(rawJSON.mock.calls)).not.toContain("AWS-StartSSHSession");
    expect(onImported).toHaveBeenCalled();
  });

  it("says the connection method changes", async () => {
    await mount();
    await paste(CONFIG);
    expect(document.body.textContent).toMatch(/connection method changes|接続方式が変わります/);
  });

  it("sends nothing when the current lists cannot be read", async () => {
    api.mockImplementation((p: string) => Promise.resolve(p.endsWith("hosts") ? { error: { code: "internal" } } : PROFILES));
    await mount();
    await paste(CONFIG);
    expect(document.querySelector('[role="alert"]')?.textContent).toBeTruthy();
    expect(rawJSON).not.toHaveBeenCalled();
  });

  it("lists a host that already exists as such and creates nothing", async () => {
    api.mockImplementation((p: string) => Promise.resolve(p.endsWith("profiles") ? PROFILES : [{ id: "h", alias: "web", instanceId: I1 }]));
    await mount();
    await paste(CONFIG);
    expect(rowEl("web").className).toContain("exists");
    expect(importBtn().disabled).toBe(true);
  });

  it("lets the member pick the profile when the command names none", async () => {
    await mount();
    await paste(`Host a\n  HostName ${I1}\n  ProxyCommand aws ssm start-session --target %h --document-name AWS-StartSSHSession\n`);
    expect(rowEl("a").className).toContain("blocked");
    const sel = rowEl("a").querySelector<HTMLSelectElement>("select")!;
    await act(async () => {
      sel.value = "p1";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(rowEl("a").className).toContain("create");
    await act(async () => importBtn().click());
    await flush();
    expect(rawJSON.mock.calls[0][2]).toMatchObject({ alias: "a", profileId: "p1" });
  });

  it("names the line of a file OpenSSH cannot read", async () => {
    await mount();
    await paste("Host a\n  User x\nHost\n");
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("3");
    expect(api).not.toHaveBeenCalled();
  });
});
