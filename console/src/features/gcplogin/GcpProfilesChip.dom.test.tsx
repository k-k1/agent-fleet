// ADR 0107 phase 3: the WS bar's Google Cloud badge counts the logged-in Settings profiles,
// lists each one's login in a popover that says there is no default profile, turns amber
// while an agent waits for a login, and hands a login to the profile modal — which starts
// nothing until the member's own press there, and never adopts an attempt from a list.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
const calls: { path: string; method: string }[] = [];
let profiles: Json[] = [];
let requests: Json[] = [];

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string, opts?: RequestInit) => {
    calls.push({ path, method: opts?.method || "GET" });
    if (path === "api/gcp-login/profiles") return { profiles };
    if (path === "api/gcp-login") return { requests };
    return {};
  }),
  apiJSON: vi.fn(async (path: string, method: string) => {
    calls.push({ path, method });
    if (path.includes("/start")) return { attempt: "att1" };
    return { ok: true };
  }),
}));

const { GcpProfilesChip } = await import("./GcpProfilesChip.tsx");
const { GcpLoginHost } = await import("./GcpLoginHost.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { useGcpLoginStore } = await import("./store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useSettingsUI } = await import("../settings/store.ts");
const { setLocale } = await import("../../lib/i18n/index.ts");

const prod: Json = { name: "prod", state: "signed_in", label: "Production", project: "prod-project", account: "dev@example.com" };
const stg: Json = { name: "stg", state: "signed_in", label: "Staging", project: "stg-project", account: "dev@example.com" };
const dev: Json = { name: "dev", state: "none", label: "", project: "dev-project" };

let root: Root | null = null;
let host: HTMLDivElement;

async function tick(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}
async function mount(withHost = false): Promise<void> {
  await act(async () =>
    root!.render(
      <ToastProvider>
        <GcpProfilesChip />
        {withHost && <GcpLoginHost />}
      </ToastProvider>,
    ),
  );
  await tick(0);
}
const chip = () => host.querySelector(".ws-gcp-btn") as HTMLButtonElement | null;
const asks = () => calls.filter((c) => c.path === "api/gcp-login/profiles").length;
async function openPop(): Promise<void> {
  await act(async () => chip()!.click());
  await tick(0);
}
const rows = () => Array.from(host.querySelectorAll(".ws-gcp-row"));
const loginOf = (i: number) => rows()[i].querySelector<HTMLButtonElement>(".ws-gcp-login")!;

beforeEach(() => {
  setLocale("en");
  vi.useFakeTimers();
  calls.length = 0;
  profiles = [prod, stg, dev];
  requests = [];
  useGcpLoginStore.setState({ profiles: null, requests: [], hidden: {}, modal: null, profileModal: null });
  useWorkspaceStore.setState({ state: "running" });
  useSettingsUI.setState({ settingsOpen: false });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  document.body.innerHTML = "";
  root = null;
  vi.useRealTimers();
});

describe("Google Cloud profiles chip", () => {
  it("counts logged-in profiles against all of them, with the usage chips' classes", async () => {
    await mount();
    expect(chip()!.textContent).toContain("2/3");
    expect(chip()!.title).toBe("Google Cloud profiles: 2 of 3 logged in");
    expect(chip()!.className).toContain("ok");
    expect(chip()!.classList.contains("kind-tag")).toBe(true);
    expect(chip()!.classList.contains("ws-usage-btn")).toBe(true);
    expect(chip()!.className).not.toMatch(/warn|muted/);
  });

  it("names the profile when exactly one is logged in, and is muted when none is", async () => {
    profiles = [prod, dev];
    await mount();
    expect(chip()!.textContent).toContain("Production");
    profiles = [dev];
    await act(async () => void useGcpLoginStore.getState().refreshProfiles());
    expect(chip()!.textContent).toContain("0/1");
    expect(chip()!.className).toContain("muted");
  });

  it("renders nothing without Settings profiles or while the workspace is not running", async () => {
    profiles = [];
    await mount();
    expect(asks()).toBe(1);
    expect(chip()).toBeNull();
    profiles = [prod];
    await act(async () => useWorkspaceStore.setState({ state: "stopped" }));
    await act(async () => void useGcpLoginStore.getState().refreshProfiles());
    expect(chip()).toBeNull();
    await act(async () => useWorkspaceStore.setState({ state: "running" }));
    await tick(0);
    expect(chip()).not.toBeNull();
  });

  it("keeps the last list when the Agent cannot answer", async () => {
    await mount();
    profiles = null as unknown as Json[];
    await act(async () => void useGcpLoginStore.getState().refreshProfiles());
    expect(chip()!.textContent).toContain("2/3");
  });

  it("lists every profile logged-in first, says there is no default, and asks again on open", async () => {
    await mount();
    const before = asks();
    await openPop();
    expect(asks()).toBe(before + 1);
    expect(rows().map((r) => r.querySelector(".ws-aws-name")!.textContent)).toEqual(["Production", "Staging", "dev"]);
    expect(rows()[0].textContent).toContain("Logged in");
    expect(rows()[0].textContent).toContain("prod-project");
    expect(rows()[0].textContent).toContain("dev@example.com");
    expect(rows()[2].textContent).toContain("Not logged in");
    expect(host.querySelector(".ws-gcp-pop")!.textContent).toContain("af-gcloud-exec --profile");
    // A logged-in row offers a forced sign-in, a logged-out one a plain one, as in Settings.
    expect(loginOf(0).textContent).toBe("Log in again");
    expect(loginOf(2).textContent).toBe("Log in");
  });

  it("does not poll on its own", async () => {
    await mount();
    const before = asks();
    await tick(30 * 60_000);
    expect(asks()).toBe(before);
  });

  it("turns amber while an agent waits for a profile's login, and asks again when that settles", async () => {
    await mount();
    const before = asks();
    requests = [{ id: "r1", profile: "dev", label: "", project: "dev-project", waiters: [] }];
    await act(async () => void useGcpLoginStore.getState().refresh());
    await tick(0);
    expect(asks()).toBe(before + 1);
    expect(chip()!.className).toContain("warn");
    expect(chip()!.title).toBe("Google Cloud profiles: an agent is waiting for a login");
    await openPop();
    expect(rows()[2].querySelector(".ws-aws-foot .warn")!.textContent).toBe("An agent is waiting for this login");
    expect(rows()[0].querySelector(".ws-aws-foot .warn")).toBeNull();

    // The same list read again costs nothing; the request going away asks once more.
    const mid = asks();
    await act(async () => void useGcpLoginStore.getState().refresh());
    await tick(0);
    expect(asks()).toBe(mid);
    requests = [];
    profiles = [prod, stg, { ...dev, state: "signed_in", account: "dev@example.com" }];
    await act(async () => void useGcpLoginStore.getState().refresh());
    await tick(0);
    expect(asks()).toBe(mid + 1);
    expect(chip()!.className).not.toContain("warn");
    expect(chip()!.textContent).toContain("3/3");
  });

  it("opens the profile login modal from a row, starting nothing until the press there", async () => {
    await mount(true);
    await openPop();
    await act(async () => loginOf(2).click());
    await tick(0);
    expect(host.querySelector(".ws-gcp-pop")).toBeNull();
    expect(useGcpLoginStore.getState().profileModal).toEqual({
      profile: { name: "dev", label: "", project: "dev-project", account: "" },
      force: false,
    });
    expect(document.body.textContent).toContain("Google Cloud login (dev)");
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    const press = Array.from(document.querySelectorAll("button")).find((b) => b.textContent === "Log in") as HTMLButtonElement;
    await act(async () => press.click());
    await tick(0);
    expect(calls.filter((c) => c.method === "POST")).toEqual([{ path: "api/gcp-login/profiles/dev/start", method: "POST" }]);
  });

  it("forces a fresh sign-in from a logged-in row, and re-reads the states when the modal closes", async () => {
    await mount(true);
    await openPop();
    await act(async () => loginOf(0).click());
    await tick(0);
    expect(useGcpLoginStore.getState().profileModal?.force).toBe(true);
    const again = Array.from(document.querySelectorAll("button")).find((b) => b.textContent === "Log in again") as HTMLButtonElement;
    await act(async () => again.click());
    await tick(0);
    expect(calls).toContainEqual({ path: "api/gcp-login/profiles/prod/start?force=1", method: "POST" });
    const before = asks();
    const close = Array.from(document.querySelectorAll("button")).find((b) => b.textContent === "Close") as HTMLButtonElement;
    await act(async () => close.click());
    await tick(0);
    expect(useGcpLoginStore.getState().profileModal).toBeNull();
    expect(asks()).toBe(before + 1);
  });

  it("opens Settings on the Google Cloud tab", async () => {
    await mount();
    await openPop();
    const link = Array.from(host.querySelectorAll("button.wu-manage")).find((b) =>
      b.textContent?.includes("Google Cloud settings"),
    ) as HTMLButtonElement;
    await act(async () => link.click());
    expect(useSettingsUI.getState().settingsOpen).toBe(true);
    expect(useSettingsUI.getState().settingsSection).toBe("gcp");
  });

  it("drops the open popover's dismiss layer when the chip goes away", async () => {
    await mount();
    await openPop();
    await act(async () => useWorkspaceStore.setState({ state: "stopped" }));
    expect(chip()).toBeNull();
    const press = new MouseEvent("mousedown", { bubbles: true, cancelable: true, button: 0 });
    document.body.dispatchEvent(press);
    expect(press.defaultPrevented).toBe(false);
  });
});
