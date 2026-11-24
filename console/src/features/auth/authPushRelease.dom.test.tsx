// Every path that can end with a different account signed in on this browser releases the push
// subscription first (#4750): otherwise the previous member's pushes keep arriving for the next one.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const order: string[] = [];
let gate: Promise<void> = Promise.resolve();
vi.mock("../push/webPush.ts", () => ({
  releasePushForSignOut: () => {
    order.push("release");
    return gate;
  },
}));
vi.mock("../editor/dirtyRegistry.ts", () => ({ confirmDirtyNavigation: () => Promise.resolve(true) }));
vi.mock("../../core/auth/authExpired.ts", () => ({
  subscribeAuthExpired: (f: () => void) => {
    f();
    return () => {};
  },
  relogin: () => order.push("relogin"),
}));

vi.mock("../../core/auth/providerRequired.ts", () => ({
  subscribeProviderRequired: (f: (r: unknown) => void) => {
    f({ tenant: "acme", provider: "okta" });
    return () => {};
  },
  clearProviderRequired: () => {},
  reloginForTenant: () => order.push("relogin-tenant"),
}));
// location.assign cannot be spied on in jsdom; clearLocalState runs immediately before it.
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  rel: (p: string) => p,
  clearLocalState: () => order.push("navigate"),
}));

import { t } from "../../lib/i18n/index.ts";
import { AuthExpiredModal } from "./AuthExpiredModal.tsx";
import { ProviderRequiredModal } from "./ProviderRequiredModal.tsx";
import { NotProvisioned } from "./NotProvisioned.tsx";

let root: Root | null = null;
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.innerHTML = "";
  order.length = 0;
});

describe("re-login after the session expired", () => {
  it("releases the push subscription before heading to the login page", async () => {
    root = createRoot(document.body.appendChild(document.createElement("div")));
    act(() => root!.render(<AuthExpiredModal />));
    const btn = [...document.querySelectorAll("button")].find((b) => b.className.includes("primary"))!;
    await act(async () => {
      btn.click();
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(order).toEqual(["release", "relogin"]);
  });
  it("does not head to the login page until the release has finished", async () => {
    let open!: () => void;
    gate = new Promise<void>((r) => (open = r));
    root = createRoot(document.body.appendChild(document.createElement("div")));
    act(() => root!.render(<AuthExpiredModal />));
    const btn = [...document.querySelectorAll("button")].find((b) => b.className.includes("primary"))!;
    await act(async () => {
      btn.click();
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(order).toEqual(["release"]);
    open();
    await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    expect(order).toEqual(["release", "relogin"]);
    gate = Promise.resolve();
  });
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const clickText = async (label: string) => {
  const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes(label))!;
  await act(async () => void btn.click());
  await settle();
};

describe.each([
  ["a tenant that needs another sign-in method", <ProviderRequiredModal />, "auth.provider_required_signin", "relogin-tenant"],
  ["an account that is on no roster", <NotProvisioned />, "notprov.switch_account", "navigate"],
])("leaving for %s", (_name, ui, labelKey, after) => {
  it("releases the push subscription before heading away", async () => {
    root = createRoot(document.body.appendChild(document.createElement("div")));
    act(() => root!.render(ui));
    await clickText(t(labelKey as never));
    expect(order).toEqual(["release", after]);
  });
  it("does not head away until the release has finished", async () => {
    let open!: () => void;
    gate = new Promise<void>((r) => (open = r));
    root = createRoot(document.body.appendChild(document.createElement("div")));
    act(() => root!.render(ui));
    await clickText(t(labelKey as never));
    expect(order).toEqual(["release"]);
    open();
    await settle();
    expect(order).toEqual(["release", after]);
    gate = Promise.resolve();
  });
});
