// The ~/.aws/config import dialog (issue #3010), in jsdom. What it has to hold:
//   1. No secret in the pasted text reaches a request, the rendered preview or the console.
//   2. The pasted text is not kept once read.
//   3. A failed read of the current profiles sends nothing.
//   4. A rename in the preview keeps a role attached to its source.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const rawJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  rawJSON: (...a: unknown[]) => rawJSON(...a),
}));

import { AwsConfigImport } from "./AwsConfigImport.tsx";

const URL = "https://corp.awsapps.com/start";
const SECRETS = ["AKIASENTINEL", "SECRETSENTINEL", "TOKENSENTINEL", "PROCSENTINEL"];
const CONFIG = `[sso-session corp]
sso_start_url = ${URL}
sso_region = us-east-1

[default]
sso_session = corp
sso_account_id = 111111111111
sso_role_name = Admin

[profile deploy]
role_arn = arn:aws:iam::222222222222:role/Deploy
source_profile = default

[profile cont]
sso_session = corp
sso_account_id = 111111111111
sso_role_name = Read
  aws_secret_access_key = SECRETSENTINEL

[profile keys]
aws_access_key_id = AKIASENTINEL
aws_session_token = TOKENSENTINEL

[profile proc]
credential_process = /bin/creds --arg PROCSENTINEL
`;

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
    root!.render(<AwsConfigImport onClose={() => {}} onImported={onImported} />);
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

// By the shown name, compared exactly (attribute selectors may fold case in jsdom).
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

describe("AwsConfigImport", () => {
  it("never lets a secret reach a request, the page or the console, and drops the pasted text", async () => {
    await mount();
    await paste(CONFIG);
    expect(document.querySelector("textarea")).toBeNull();
    expect(document.body.textContent).toContain("秘密");
    // [default] must be renamed before it can be created; rename it, which keeps deploy attached.
    const label = rowEl("default").querySelector<HTMLInputElement>("input.cinput")!;
    await act(async () => setValue(label, "main"));
    expect(rowEl("deploy").textContent).toContain("main");
    expect(rowEl("cont").textContent).toContain("sso_role_name");
    await act(async () => importBtn().click());
    await flush();
    const bodies = rawJSON.mock.calls.map((c) => JSON.stringify(c[2]));
    expect(bodies).toHaveLength(2);
    expect(rawJSON.mock.calls[0][2]).toMatchObject({ label: "main", startUrl: URL, accountId: "111111111111" });
    expect(rawJSON.mock.calls[1][2]).toMatchObject({ label: "deploy", kind: "assume_role", sourceProfileId: "id1" });
    const seen = bodies.join("\n") + document.body.innerHTML + logs.join("\n") + JSON.stringify(api.mock.calls);
    for (const s of SECRETS) expect(seen).not.toContain(s);
    expect(onImported).toHaveBeenCalled();
  });

  it("sends nothing when the current profiles cannot be read", async () => {
    api.mockImplementation((p: string) => Promise.resolve(p.endsWith("profiles") ? { error: { code: "internal" } } : []));
    await mount();
    await paste("[profile a]\nsso_start_url = " + URL + "\nsso_region = us-east-1\nsso_account_id = 1\nsso_role_name = R\n");
    expect(document.querySelector('[role="alert"]')?.textContent).toBeTruthy();
    expect(rawJSON).not.toHaveBeenCalled();
  });

  it("re-reads the profiles before sending, so a row created meanwhile is not created twice", async () => {
    await mount();
    await paste("[profile a]\nsso_start_url = " + URL + "\nsso_region = us-east-1\nsso_account_id = 1\nsso_role_name = R\n");
    api.mockImplementation((p: string) =>
      Promise.resolve(p.endsWith("profiles") ? [{ id: "x", kind: "sso", label: "a", startUrl: URL, ssoRegion: "us-east-1", accountId: "1", roleName: "R" }] : []),
    );
    await act(async () => importBtn().click());
    await flush();
    expect(rawJSON).not.toHaveBeenCalled();
  });

  it("shows no part of a profile name or key name that holds a key", async () => {
    await mount();
    await paste(`[profile "aws_secret_access_key=SECRETSENTINEL"]\nsso_start_url = ${URL}\nsso_region = us-east-1\nsso_account_id = 1\nsso_role_name = R\n`);
    expect(document.body.innerHTML).not.toContain("SECRETSENTINEL");
    expect(document.querySelector('[data-row="line-1"]')).not.toBeNull();
    root!.unmount();
    document.body.innerHTML = "";
    await mount();
    await paste("[profile a]\nSECRETSENTINEL = 1\nsecretsentinel = 2\n");
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("3");
    expect(document.body.innerHTML.toLowerCase()).not.toContain("secretsentinel");
  });

  it("lets a clashing row be unticked so the other one goes in", async () => {
    await mount();
    const sso = (n: string, acct: string) => `[profile ${n}]\nsso_start_url = ${URL}\nsso_region = us-east-1\nsso_account_id = ${acct}\nsso_role_name = R\n`;
    await paste(sso("Prod", "1") + sso("prod", "2"));
    expect(rowEl("Prod").className).toContain("blocked");
    const box = rowEl("prod").querySelector<HTMLInputElement>("input[type=checkbox]")!;
    expect(box.checked).toBe(true);
    await act(async () => box.click());
    expect(rowEl("Prod").className).toContain("create");
    expect(rowEl("prod").querySelector<HTMLInputElement>("input[type=checkbox]")!.checked).toBe(false);
  });

  it("keeps Stop usable while importing, and reports the logins the run implies", async () => {
    await mount();
    const sso = (n: string) => `[profile ${n}]\nsso_session = corp\nsso_account_id = 1${n.length}\nsso_role_name = R${n}\n`;
    await paste(`[sso-session corp]\nsso_start_url = ${URL}\nsso_region = us-east-1\n` + sso("a") + sso("bb"));
    let release: () => void = () => {};
    rawJSON.mockImplementationOnce(
      () => new Promise((res) => (release = () => res(new Response(JSON.stringify({ id: "id1" }), { status: 201 })))),
    );
    await act(async () => importBtn().click());
    await flush();
    const stop = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "中止" || b.textContent === "Stop")!;
    expect(stop.closest("[inert]")).toBeNull();
    await act(async () => stop.click());
    await act(async () => release());
    await flush();
    expect(rawJSON).toHaveBeenCalledTimes(1);
    const text = document.body.textContent || "";
    expect(text).toMatch(/1 回|: 1 \(/);
  });

  it("names the line of a file the CLI cannot read", async () => {
    await mount();
    await paste("[profile a]\nregion = 1\nregion = 2\n");
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("3");
    expect(api).not.toHaveBeenCalled();
  });
});
