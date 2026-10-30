// #1477: the WS bar's AWS badge counts the signed-in Settings profiles, lists each one's login
// in a popover that says there is no default profile, hands a login to the profile modal and
// the settings to Settings > AWS profiles/SSM, and asks the Agent only on demand.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
const calls: string[] = [];
let profiles: Json[] = [];

let logoutReply: Json = { revoked: true };
let confirmAnswer = true;
// What the Agent lists once a logout has gone through.
let afterLogout: Json[] | null = null;
const confirmAsks: { title: string; body: unknown }[] = [];
const toasts: { msg: string; kind?: string }[] = [];
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: vi.fn(async (path: string, opts?: RequestInit) => {
    calls.push(opts?.method ? `${opts.method} ${path}` : path);
    if (path === "api/aws-login/profiles") return { profiles };
    if (path.endsWith("/logout")) {
      if (afterLogout) profiles = afterLogout;
      return logoutReply;
    }
    return {};
  }),
  apiJSON: vi.fn(async () => ({})),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({
  useToast: () => (msg: string, o?: { kind?: string }) => toasts.push({ msg, kind: o?.kind }),
}));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => (o: { title: string; body: unknown }) => {
    confirmAsks.push(o);
    return Promise.resolve(confirmAnswer);
  },
}));

const { AwsProfilesChip } = await import("./AwsProfilesChip.tsx");
const { useAwsLoginStore } = await import("./store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useSettingsUI } = await import("../settings/store.ts");
const { setLocale } = await import("../../lib/i18n/index.ts");

const NOW = Date.parse("2026-10-02T03:00:00Z");
const prod: Json = { name: "prod", state: "signed_in", label: "Production", accountId: "111111111111", roleName: "Admin" };
const stg: Json = { name: "stg", state: "renew", label: "Staging", accountId: "222222222222", roleName: "Dev" };
const dev: Json = { name: "dev", state: "none", label: "", accountId: "333333333333", roleName: "Dev" };

let root: Root | null = null;
let host: HTMLDivElement;

async function tick(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}
async function mount(): Promise<void> {
  await act(async () => root!.render(<AwsProfilesChip />));
  await tick(0);
}
const chip = () => host.querySelector(".ws-aws-btn") as HTMLButtonElement | null;
const asks = () => calls.filter((c) => c === "api/aws-login/profiles").length;
async function openPop(): Promise<void> {
  await act(async () => chip()!.click());
  await tick(0);
}
const rows = () => Array.from(host.querySelectorAll(".ws-aws-row"));

beforeEach(() => {
  setLocale("en");
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
  calls.length = 0;
  toasts.length = 0;
  logoutReply = { revoked: true };
  confirmAnswer = true;
  afterLogout = null;
  confirmAsks.length = 0;
  profiles = [prod, stg, dev];
  useAwsLoginStore.setState({ profiles: null, expiring: [], profileModal: null });
  useWorkspaceStore.setState({ state: "running" });
  useSettingsUI.setState({ settingsOpen: false });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  vi.useRealTimers();
});

describe("AWS profiles chip", () => {
  it("counts signed-in and renewable profiles against all of them", async () => {
    await mount();
    expect(chip()!.textContent).toContain("2/3");
    expect(chip()!.title).toBe("AWS profiles: 2 of 3 signed in");
    expect(chip()!.className).toContain("ok");
    // The usage chips' own classes give it their font, padding and height.
    expect(chip()!.classList.contains("kind-tag")).toBe(true);
    expect(chip()!.classList.contains("ws-usage-btn")).toBe(true);
    expect(chip()!.className).not.toMatch(/warn|muted/);
  });

  it("names the profile when exactly one is signed in", async () => {
    profiles = [prod, dev];
    await mount();
    expect(chip()!.textContent).toContain("Production");
  });

  it("is muted when nothing is signed in", async () => {
    profiles = [dev];
    await mount();
    expect(chip()!.textContent).toContain("0/1");
    expect(chip()!.className).toContain("muted");
    expect(chip()!.className).not.toMatch(/\bok\b/);
  });

  it("renders nothing without Settings profiles or while the workspace is not running", async () => {
    profiles = [];
    await mount();
    expect(chip()).toBeNull();
    profiles = [prod];
    await act(async () => useWorkspaceStore.setState({ state: "stopped" }));
    await act(async () => void useAwsLoginStore.getState().refreshExpiry());
    expect(chip()).toBeNull();
    await act(async () => useWorkspaceStore.setState({ state: "running" }));
    await tick(0);
    expect(chip()).not.toBeNull();
  });

  it("lists every profile signed-in first, says there is no default, and asks again on open", async () => {
    await mount();
    const before = asks();
    await openPop();
    expect(asks()).toBe(before + 1);
    const names = rows().map((r) => r.querySelector(".ws-aws-name")!.textContent);
    expect(names).toEqual(["Production", "Staging", "dev"]);
    expect(rows()[0].textContent).toContain("Signed in");
    expect(rows()[0].textContent).toContain("111111111111");
    expect(rows()[1].textContent).toContain("Renews on use");
    expect(rows()[2].textContent).toContain("Not signed in");
    expect(host.querySelector(".ws-aws-pop")!.textContent).toContain("af-aws-exec --profile");
    // A signed-in row with no known end has nothing to log in again for.
    expect(rows()[0].querySelector(".ws-aws-login")).toBeNull();
    expect(rows()[2].querySelector(".ws-aws-login")).not.toBeNull();
  });

  it("warns for an expiring login and opens its login modal from the row", async () => {
    profiles = [{ ...prod, expiresAt: "2026-10-02T03:10:00Z", expiring: true }, dev];
    await mount();
    expect(chip()!.className).toContain("warn");
    expect(chip()!.className).not.toMatch(/\bok\b/);
    expect(chip()!.title).toBe("AWS profiles: a login ends soon");
    await openPop();
    const row = rows()[0];
    expect(row.querySelector(".ws-aws-foot .warn")!.textContent).toMatch(/^Ends /);
    await act(async () => (row.querySelector(".ws-aws-login") as HTMLButtonElement).click());
    expect(useAwsLoginStore.getState().profileModal?.name).toBe("prod");
    expect(host.querySelector(".ws-aws-pop")).toBeNull();
  });

  it("opens Settings on the AWS profiles tab", async () => {
    await mount();
    await openPop();
    const link = Array.from(host.querySelectorAll("button.wu-manage")).find((b) =>
      b.textContent?.includes("AWS profile settings"),
    ) as HTMLButtonElement;
    await act(async () => link.click());
    expect(useSettingsUI.getState().settingsOpen).toBe(true);
    expect(useSettingsUI.getState().settingsSection).toBe("ssm");
  });

  it("does not poll, but asks once when a known end passes", async () => {
    profiles = [prod, stg];
    await mount();
    const before = asks();
    await tick(30 * 60_000);
    expect(asks()).toBe(before);

    profiles = [{ ...prod, expiresAt: "2026-10-02T04:00:00Z" }];
    await act(async () => void useAwsLoginStore.getState().refreshExpiry());
    const after = asks();
    profiles = [{ ...prod, state: "none" }];
    await tick(29 * 60_000);
    expect(asks()).toBe(after);
    await tick(60_000 + 3000);
    expect(asks()).toBe(after + 1);
    expect(chip()!.textContent).toContain("0/1");
  });

  it("keeps the refresh due at a known end through a render after that end", async () => {
    profiles = [{ ...prod, expiresAt: "2026-10-02T03:10:00Z" }];
    await mount();
    const before = asks();
    profiles = [{ ...prod, state: "none" }];
    await tick(10 * 60_000 + 1000);
    await act(async () => root!.render(<AwsProfilesChip />));
    expect(asks()).toBe(before);
    await tick(2000);
    expect(asks()).toBe(before + 1);
    expect(chip()!.textContent).toContain("0/1");
  });

  it("does not ask again for an end that had already passed when the Agent answered", async () => {
    profiles = [{ ...prod, state: "none", expiresAt: "2026-10-02T02:00:00Z" }];
    await mount();
    // The one ask is the workspace-running one; a timer for the past end would add another.
    expect(asks()).toBe(1);
    await tick(60 * 60_000);
    expect(asks()).toBe(1);
  });

  it("drops the open popover's dismiss layer when the chip goes away", async () => {
    await mount();
    await openPop();
    await act(async () => useWorkspaceStore.setState({ state: "stopped" }));
    expect(chip()).toBeNull();
    const press = new MouseEvent("mousedown", { bubbles: true, cancelable: true, button: 0 });
    document.body.dispatchEvent(press);
    expect(press.defaultPrevented).toBe(false);
    await act(async () => useWorkspaceStore.setState({ state: "running" }));
    await tick(0);
    expect(host.querySelector(".ws-aws-pop")).toBeNull();
  });
  it("offers Log out on signed-in and renewable rows, and logs out only that profile after asking", async () => {
    await mount();
    await openPop();
    const logoutOf = (i: number) => rows()[i].querySelector<HTMLButtonElement>(".ws-aws-logout");
    expect(logoutOf(0)).not.toBeNull(); // prod, signed in
    expect(logoutOf(1)).not.toBeNull(); // stg, renewable
    expect(logoutOf(2)).toBeNull(); // dev, not signed in
    const before = asks();
    afterLogout = [{ ...prod, state: "none" }, stg, dev];
    await act(async () => logoutOf(0)!.click());
    await tick(0);
    expect(calls).toContain("POST api/aws-login/profiles/prod/logout");
    expect(calls.filter((c) => c.endsWith("/logout"))).toHaveLength(1);
    expect(asks()).toBeGreaterThan(before);
    expect(toasts).toEqual([{ msg: "Logged out of Production.", kind: "success" }]);
    expect(chip()!.textContent).toContain("Staging");
  });

  it("offers Log out on a chain row too, and ends its source's login, not a login of its own", async () => {
    const deploy: Json = { name: "deploy", state: "signed_in", label: "Deploy", accountId: "444444444444", roleName: "Deploy", source: "prod" };
    profiles = [prod, deploy];
    await mount();
    await openPop();
    const row = rows().find((r) => r.textContent?.includes("Deploy"))!;
    await act(async () => row.querySelector<HTMLButtonElement>(".ws-aws-logout")!.click());
    await tick(0);
    expect(calls.filter((c) => c.endsWith("/logout"))).toEqual(["POST api/aws-login/profiles/prod/logout"]);
    expect(toasts).toEqual([{ msg: "Logged out of Production.", kind: "success" }]);
  });

  it("logs out nothing when the member declines", async () => {
    confirmAnswer = false;
    await mount();
    await openPop();
    await act(async () => rows()[0].querySelector<HTMLButtonElement>(".ws-aws-logout")!.click());
    await tick(0);
    expect(calls.some((c) => c.endsWith("/logout"))).toBe(false);
    expect(toasts).toEqual([]);
  });

  it("shows the normal toast, not the warning, when the AWS session had already ended", async () => {
    logoutReply = { revoked: false, alreadyEnded: true };
    await mount();
    await openPop();
    await act(async () => rows()[0].querySelector<HTMLButtonElement>(".ws-aws-logout")!.click());
    await tick(0);
    expect(toasts).toEqual([{ msg: "Logged out of Production.", kind: "success" }]);
  });

  it("says so when AWS could not be told, and why the Agent refused", async () => {
    logoutReply = { revoked: false, message: "Could not connect to the endpoint URL" };
    await mount();
    await openPop();
    await act(async () => rows()[0].querySelector<HTMLButtonElement>(".ws-aws-logout")!.click());
    await tick(0);
    expect(toasts[0].kind).toBe("warn");
    expect(toasts[0].msg).toContain("Could not connect to the endpoint URL");

    logoutReply = { error: { code: "not_exported", message: "x" } };
    await openPop();
    await act(async () => rows()[0].querySelector<HTMLButtonElement>(".ws-aws-logout")!.click());
    await tick(0);
    expect(toasts[1].msg).toContain("is not in the workspace's ~/.aws/config");
  });

  // #1109: a role-chaining profile has no login of its own.
  describe("role-chaining profiles", () => {
    const chain = (name: string, source: string, state: string): Json => ({ name, state, source, label: name, accountId: "444444444444", roleName: "Deploy" });

    it("counts a source and its chains as one login, however many chains follow it", async () => {
      profiles = [prod, stg, chain("c1", "stg", "renew"), chain("c2", "stg", "renew"), dev];
      await mount();
      expect(chip()!.textContent).toContain("2/3");
      expect(chip()!.title).toBe("AWS profiles: 2 of 3 signed in");
      profiles = [dev, chain("c1", "dev", "none")];
      await act(async () => void useAwsLoginStore.getState().refreshExpiry());
      expect(chip()!.textContent).toContain("0/1");
    });

    it("names the source, not a chain, when that is the one login", async () => {
      profiles = [prod, chain("deploy", "prod", "signed_in")];
      await mount();
      expect(chip()!.textContent).toContain("Production");
    });

    it("says a chain follows its source and signs in the source from the chain's row", async () => {
      profiles = [dev, chain("deploy", "dev", "none")];
      await mount();
      await openPop();
      const row = rows().find((r) => r.querySelector(".ws-aws-name")!.textContent === "deploy")!;
      expect(row.textContent).toContain("Follows dev's login");
      expect(row.textContent).toContain("Not signed in");
      expect(row.querySelector(".ws-aws-logout")).toBeNull();
      await act(async () => row.querySelector<HTMLButtonElement>(".ws-aws-login")!.click());
      expect(useAwsLoginStore.getState().profileModal?.name).toBe("dev");
    });

    it("shows a distinct state, no sign-in action and no count for a chain whose source is unavailable", async () => {
      profiles = [prod, chain("orphan", "gone", "source_unavailable")];
      await mount();
      expect(chip()!.title).toBe("AWS profiles: 1 of 1 signed in");
      await openPop();
      const row = rows().find((r) => r.querySelector(".ws-aws-name")!.textContent === "orphan")!;
      expect(row.textContent).toContain("Source unavailable");
      expect(row.textContent).not.toContain("Signed in");
      expect(row.querySelector(".ws-aws-login")).toBeNull();
    });

    it("speaks Japanese", async () => {
      setLocale("ja");
      profiles = [dev, chain("deploy", "dev", "none"), chain("orphan", "gone", "source_unavailable")];
      await mount();
      await openPop();
      expect(host.textContent).toContain("dev のログインに従います");
      expect(host.textContent).toContain("元プロファイルなし");
    });
  });
});
