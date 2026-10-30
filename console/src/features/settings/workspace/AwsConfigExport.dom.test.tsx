// The ~/.aws/config export dialog (issue #3017, part 3), in jsdom: it only reads, it shows what
// was left out and why, a hostile stored value never reaches the text, and the download is named
// as a fragment.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const rawJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  rawJSON: (...a: unknown[]) => rawJSON(...a),
}));

import { AwsConfigExport } from "./AwsConfigExport.tsx";

const URL_OK = "https://corp.awsapps.com/start";
const row = (id: string, label: string, extra: Record<string, unknown> = {}) => ({
  id,
  label,
  kind: "sso",
  startUrl: URL_OK,
  ssoRegion: "us-east-1",
  accountId: "111111111111",
  roleName: "Admin",
  region: "",
  ...extra,
});
const PROFILES = [
  row("a", "prod"),
  row("b", "evil", { startUrl: URL_OK + "\naws_secret_access_key = SECRETSENTINEL" }),
  row("c", "default"),
  { id: "d", label: "deploy", kind: "assume_role", sourceProfileId: "c", roleArn: "arn:aws:iam::222222222222:role/Deploy", region: "" },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const logs: string[] = [];

async function flush() {
  for (let i = 0; i < 5; i++) await act(async () => void (await Promise.resolve()));
}
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<AwsConfigExport onClose={() => {}} />));
  await flush();
}

beforeEach(() => {
  api.mockReset();
  rawJSON.mockReset();
  api.mockResolvedValue(PROFILES);
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

describe("AwsConfigExport", () => {
  it("lists what is left out with reasons, keeps hostile values out of the text, and sends nothing", async () => {
    await mount();
    const text = document.querySelector<HTMLElement>("pre")!.textContent!;
    expect(text).toContain("[profile prod]");
    expect(text).not.toContain("SECRETSENTINEL");
    expect(text).not.toContain("[profile evil]");
    expect(text).not.toContain("[profile deploy]");
    const left = [...document.querySelectorAll("ul li")].map((li) => li.textContent);
    expect(left).toHaveLength(3);
    expect(left.join("\n")).toMatch(/evil/);
    expect(left.join("\n")).toMatch(/default/);
    expect(left.join("\n")).toMatch(/deploy/);
    expect(document.body.textContent).toMatch(/not a replacement|置き換え用のファイルではありません/);
    expect(rawJSON).not.toHaveBeenCalled();
    expect(api.mock.calls).toEqual([["api/ssm/profiles"]]);
    expect(logs.join("\n")).not.toContain("SECRETSENTINEL");
  });

  it("downloads the text under the fragment name", async () => {
    const urls: Blob[] = [];
    (URL as any).createObjectURL = (b: Blob) => (urls.push(b), "blob:x");
    (URL as any).revokeObjectURL = () => {};
    const names: string[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      names.push(this.download);
    });
    await mount();
    await act(async () => document.querySelector<HTMLButtonElement>("button.primary")!.click());
    expect(names).toEqual(["agent-fleet-aws-profiles.aws-config-fragment.txt"]);
    expect(await urls[0].text()).toContain("[profile prod]");
  });

  it("says so when the profiles cannot be read, and offers no download", async () => {
    api.mockResolvedValue({ error: { code: "internal" } });
    await mount();
    expect(document.querySelector('[role="alert"]')?.textContent).toBeTruthy();
    expect(document.querySelector("button.primary")).toBeNull();
  });
});
