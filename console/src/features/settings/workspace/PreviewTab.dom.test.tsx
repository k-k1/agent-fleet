// The standalone preview-subdomain tab. What matters is never offering a setting that does
// nothing on a deployment that issues no subdomain:
//   1. usePreviewAvailable is false when previewDomain is empty (so the rail hides it)
//   2. it is null until the answer arrives (so no entry appears and then vanishes)
//   3. reached directly anyway, the tab says "not available" instead of rendering blank
//   4. where subdomains are issued the settings appear, and the published ports are saved
//      on blur
import { describe, it, expect, afterEach, beforeAll, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
const tenantNow = { v: "default" };
vi.mock("../../../core/api/client.ts", async (orig) => ({
  errText: ((await orig()) as { errText: unknown }).errText, // real: the catalogue lookup is under test
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  getTenant: () => tenantNow.v,
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));
const toastSpy = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => toastSpy(m) }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));

const wsSettings = (extra: Record<string, unknown> = {}) => ({
  agentUpdate: false,
  allowAgentUpdate: false,
  previewDomain: "example.invalid",
  previewPorts: [3000, 8080],
  previewUrls: { "8080": "https://slug-8080.example.invalid", "3000": "https://slug-3000.example.invalid" },
  previewMaxPorts: 8,
  ...extra,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
// Every mounted tree, so afterEach can unmount all of them rather than only the last one.
const mounted: { root: Root; host: HTMLDivElement }[] = [];
// Bumped per test. A test that timed out keeps running on its pending await; freshModule
// throws for it once its import finally lands, so it cannot mount into a later test.
let generation = 0;

// The availability answer is cached at module scope (so settings do not refetch on every
// open), so each test re-imports the module; reusing it would make every later test read
// the first one's answer.
async function freshModule() {
  const mine = generation;
  vi.resetModules();
  const m = await import("./PreviewTab.tsx");
  if (mine !== generation) throw new Error("stale test: its import finished after the test ended");
  return m;
}

// Every test re-evaluates the module graph (see freshModule), which is slow on a loaded
// host; the budget is this file's own, not the global testTimeout.
vi.setConfig({ testTimeout: 30_000 });

// The first import of PreviewTab and its dependency graph is the slow one (cold transform
// of the whole tree); later freshModule() calls reuse the transformed modules. Do it here,
// with a generous timeout of its own, so the first test never pays for it.
beforeAll(async () => {
  await import("./PreviewTab.tsx");
}, 60_000);

async function mount(el: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  mounted.push({ root, host });
  await act(async () => {
    root!.render(el);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

function unmountCurrent() {
  const i = mounted.findIndex((m) => m.root === root);
  if (i >= 0) mounted.splice(i, 1);
  act(() => root?.unmount());
  host?.remove();
}

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

beforeEach(() => {
  generation++;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
  apiJSON.mockReset();
  toastSpy.mockReset();
});

afterEach(() => {
  generation++;
  for (const m of mounted.splice(0)) {
    act(() => m.root.unmount());
    m.host.remove();
  }
  root = null;
  host = null;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("usePreviewAvailable", () => {
  const probe = (usePreviewAvailable: () => boolean | null) => {
    return function Probe() {
      const v = usePreviewAvailable();
      return <span id="v">{v === null ? "pending" : String(v)}</span>;
    };
  };
  const read = () => document.querySelector("#v")!.textContent;

  it("is true when previewDomain is set", async () => {
    api.mockResolvedValue(wsSettings());
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("true");
  });

  it("is false when previewDomain is empty, so the rail hides the tab", async () => {
    api.mockResolvedValue(wsSettings({ previewDomain: "" }));
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("false");
  });

  it("is null until the answer arrives, so nothing appears and then vanishes", async () => {
    api.mockReturnValue(new Promise(() => {})); // never resolves
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("pending");
  });

  // Settings opening again is a fresh mount of the same module (the cache survives it).
  const remount = async (P: () => React.ReactElement) => {
    unmountCurrent();
    await mount(<P />);
  };

  it("does not cache an error answer: the next open asks again and shows the entry", async () => {
    api.mockResolvedValueOnce({ error: "control plane starting" });
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("false");
    api.mockResolvedValueOnce(wsSettings());
    await remount(P);
    expect(api).toHaveBeenCalledTimes(2);
    expect(read()).toBe("true");
  });

  it("still caches a successful answer without previewDomain as unavailable", async () => {
    api.mockResolvedValueOnce(wsSettings({ previewDomain: "" }));
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("false");
    api.mockResolvedValueOnce(wsSettings());
    await remount(P);
    expect(api).toHaveBeenCalledTimes(1);
    expect(read()).toBe("false");
  });
});

describe("PreviewTab", () => {
  it("says not available, rather than showing settings, where nothing is issued", async () => {
    api.mockResolvedValue(wsSettings({ previewDomain: "" }));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    expect(document.querySelector(".ds-group")).toBeNull();
    expect(document.querySelector(".pad")!.textContent).toContain("発行されません");
  });

  it("lists the current URLs in port order where subdomains are issued", async () => {
    api.mockResolvedValue(wsSettings());
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const urls = Array.from(document.querySelectorAll<HTMLAnchorElement>(".pv-current-url")).map((a) => a.textContent);
    expect(urls).toEqual(["slug-3000.example.invalid", "slug-8080.example.invalid"]);
  });

  it("saves the published ports on blur, not on every keystroke", async () => {
    api.mockResolvedValue(wsSettings());
    apiJSON.mockResolvedValue(wsSettings({ previewPorts: [3000, 5173] }));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const input = document.querySelector<HTMLInputElement>(".ds-select")!;
    expect(input.value).toBe("3000, 8080");
    await act(async () => {
      // Set the value on a React-controlled input by calling the value setter directly.
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "3000, 5173");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(apiJSON).not.toHaveBeenCalled(); // half-typed "3000, 5" must not be saved
    await act(async () => {
      // React onBlur rides on the native focusout; a non-bubbling blur never reaches it.
      input.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/ws-settings", "PUT", { previewPorts: [3000, 5173] });
  });
});


describe("writes queued across a scope change", () => {
  const queueSecondWrite = async () => {
    api.mockResolvedValue(wsSettings());
    const { PreviewTab } = await freshModule();
    const { t } = await import("../../../lib/i18n/index.ts");
    const { useTenantStore } = await import("../../../core/store/tenant.ts");
    const pending: Array<(v: unknown) => void> = [];
    apiJSON.mockImplementation(() => new Promise((r) => pending.push(r)));
    await mount(<PreviewTab />);
    const input = document.querySelector<HTMLInputElement>(".ds-select")!;
    await act(async () => input.dispatchEvent(new FocusEvent("focusout", { bubbles: true }))); // write 1 is out
    const label = [...document.querySelectorAll("*")].find((e) => e.children.length === 0 && e.textContent === t("env.preview_public_label"))!;
    const on = [...label.parentElement!.querySelectorAll("button")].find((b) => b.textContent === t("common.on"))!;
    await act(async () => on.click()); // write 2 waits behind it
    expect(apiJSON).toHaveBeenCalledTimes(1);
    return { pending, useTenantStore };
  };

  it("does not send a queued write to another tenant", async () => {
    const { pending } = await queueSecondWrite();
    tenantNow.v = "other"; // the tenant switched before the queue reached the second write
    await act(async () => pending[0](wsSettings()));
    await act(async () => {
      await Promise.resolve();
    });
    expect(apiJSON).toHaveBeenCalledTimes(1); // the public toggle was cancelled, not sent to "other"
    tenantNow.v = "default";
  });

  it("does not send a queued write after the identity changed within the tenant", async () => {
    const { pending, useTenantStore } = await queueSecondWrite();
    useTenantStore.setState({ identityRev: useTenantStore.getState().identityRev + 1 });
    await act(async () => pending[0](wsSettings()));
    await act(async () => {
      await Promise.resolve();
    });
    expect(apiJSON).toHaveBeenCalledTimes(1);
    useTenantStore.setState({ identityRev: 0 });
  });
});

describe("overlapping saves", () => {
  it("a field blur and a toggle click in flight together end on the last save", async () => {
    api.mockResolvedValue(wsSettings());
    const { PreviewTab } = await freshModule();
    const { usePreviewStore } = await import("../../preview/previewState.ts");
    const { t } = await import("../../../lib/i18n/index.ts");
    const pending: Array<(v: unknown) => void> = [];
    apiJSON.mockImplementation(() => new Promise((r) => pending.push(r)));
    await mount(<PreviewTab />);
    const input = document.querySelector<HTMLInputElement>(".ds-select")!;
    await act(async () => input.dispatchEvent(new FocusEvent("focusout", { bubbles: true }))); // ports save
    const label = [...document.querySelectorAll("*")].find((e) => e.children.length === 0 && e.textContent === t("env.preview_public_label"))!;
    const on = [...label.parentElement!.querySelectorAll("button")].find((b) => b.textContent === t("common.on"))!;
    await act(async () => on.click()); // public save, while the ports save is still out
    await act(async () => pending[0](wsSettings({ previewPublic: false }))); // ports answers first
    expect(usePreviewStore.getState().info?.public).toBe(false);
    await act(async () => {
      await Promise.resolve();
    });
    expect(apiJSON).toHaveBeenCalledTimes(2); // sent in order, one after the other
    await act(async () => pending[1](wsSettings({ previewPublic: true })));
    expect(usePreviewStore.getState().info?.public).toBe(true); // the last save wins
  });
});

describe("Expose this port… strip", () => {
  const addStrip = () => host!.querySelector(".pv-add-strip");
  const click = async (label: string) => {
    const b = [...host!.querySelectorAll("button")].find((x) => x.textContent === label)!;
    await act(async () => b.click());
  };
  const tabWith = async (extra: Record<string, unknown> = {}) => {
    api.mockResolvedValue(wsSettings(extra));
    const { PreviewTab } = await freshModule();
    const { useSettingsUI } = await import("../store.ts"); // after the reset: the tab's own copy
    return { useSettingsUI, PreviewTab };
  };

  it("asks first and saves nothing until Add; Cancel saves nothing", async () => {
    const { PreviewTab, useSettingsUI } = await tabWith();
    useSettingsUI.setState({ previewAddPort: 5173 });
    await mount(<PreviewTab />);
    expect(addStrip()?.textContent).toContain("5173");
    expect(apiJSON).not.toHaveBeenCalled();
    await click("キャンセル");
    expect(addStrip()).toBeNull();
    expect(apiJSON).not.toHaveBeenCalled();
    expect(useSettingsUI.getState().previewAddPort).toBeNull();
  });

  it("Add saves the list with the port appended", async () => {
    const { PreviewTab, useSettingsUI } = await tabWith();
    apiJSON.mockResolvedValue(wsSettings({ previewPorts: [3000, 8080, 5173] }));
    useSettingsUI.setState({ previewAddPort: 5173 });
    await mount(<PreviewTab />);
    await click("追加");
    expect(apiJSON).toHaveBeenCalledWith("api/env/ws-settings", "PUT", { previewPorts: [3000, 8080, 5173] });
    expect(addStrip()).toBeNull();
  });

  it("shows no strip for an already listed port, and the cap message at the cap", async () => {
    const { PreviewTab, useSettingsUI } = await tabWith();
    useSettingsUI.setState({ previewAddPort: 3000 });
    await mount(<PreviewTab />);
    expect(addStrip()).toBeNull();
    unmountCurrent();

    const full = await tabWith({ previewMaxPorts: 2 });
    full.useSettingsUI.setState({ previewAddPort: 5173 });
    await mount(<full.PreviewTab />);
    expect(addStrip()?.textContent).toContain("2");
    expect([...host!.querySelectorAll("button")].some((b) => b.textContent === "追加")).toBe(false);
  });
});

describe("development OIDC IdP", () => {
  const idp = (extra: Record<string, unknown> = {}) =>
    wsSettings({
      devIdpAvailable: true,
      devIdpIssuer: "https://af.example.invalid/dev-idp",
      devIdpClientId: "afdev_abc",
      ...extra,
    });
  const text = () => host!.textContent || "";
  it("is hidden where the deployment does not serve the IdP", async () => {
    api.mockResolvedValue(wsSettings({ devIdpAvailable: false }));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const { t } = await import("../../../lib/i18n/index.ts");
    expect(text()).not.toContain(t("env.devidp_title"));
    expect(text()).not.toContain("AF_DEV_IDP");
  });

  it("is hidden when the field is absent (older control plane)", async () => {
    api.mockResolvedValue(wsSettings());
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const { t } = await import("../../../lib/i18n/index.ts");
    expect(text()).not.toContain(t("env.devidp_title"));
  });

  it("shows issuer, discovery URL and client id with copy buttons, and never a secret", async () => {
    api.mockResolvedValue(idp());
    const { PreviewTab } = await freshModule();
    const write = vi.fn();
    Object.defineProperty(navigator, "clipboard", { value: { writeText: write }, configurable: true });
    await mount(<PreviewTab />);
    expect(text()).toContain("https://af.example.invalid/dev-idp/.well-known/openid-configuration");
    expect(text()).toContain("afdev_abc");
    expect(text()).toContain("AF_DEV_IDP_CLIENT_SECRET");
    const { t } = await import("../../../lib/i18n/index.ts");
    const copies = [...host!.querySelectorAll<HTMLButtonElement>(".dip-creds button")].filter((b) => b.textContent === t("env.devidp_copy"));
    expect(copies).toHaveLength(3);
    await act(async () => copies[2].click());
    expect(write).toHaveBeenCalledWith("afdev_abc");
  });

  it("shows the credentials and the warnings while off too: the env is injected either way", async () => {
    api.mockResolvedValue(idp({ devIdp: false }));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const { t } = await import("../../../lib/i18n/index.ts");
    expect(text()).toContain("afdev_abc");
    expect(text()).toContain(t("env.devidp_warn"));
  });

  it("rotates the secret on request, shows the new client id and says a restart is needed", async () => {
    api.mockResolvedValue(idp());
    apiJSON.mockResolvedValue({ devIdpClientId: "afdev_rot", restartRequired: true });
    const { PreviewTab } = await freshModule();
    const { t } = await import("../../../lib/i18n/index.ts");
    await mount(<PreviewTab />);
    const b = [...host!.querySelectorAll("button")].find((x) => x.textContent === t("env.devidp_rotate"))!;
    await act(async () => b.click());
    expect(apiJSON).toHaveBeenCalledWith("api/env/ws-settings/dev-idp/rotate", "POST", {});
    expect(text()).toContain("afdev_rot");
    expect(toastSpy).toHaveBeenCalledWith(t("env.devidp_rotated"));
    expect(t("env.devidp_rotated")).toMatch(/再起動/);
  });

  it("keeps the credentials after a reissue answer without IdP fields", async () => {
    api.mockResolvedValue(idp());
    apiJSON.mockResolvedValue(wsSettings({ previewReissued: true })); // as the real reissue answer: no devIdp*
    const { PreviewTab } = await freshModule();
    const { t } = await import("../../../lib/i18n/index.ts");
    await mount(<PreviewTab />);
    const b = [...host!.querySelectorAll("button")].find((x) => x.textContent === t("env.preview_reissue"))!;
    await act(async () => b.click());
    await act(async () => {
      await Promise.resolve();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/ws-settings/preview/reissue", "POST", {});
    expect(text()).toContain("afdev_abc");
  });

  const copyBtn = async () => {
    const { t } = await import("../../../lib/i18n/index.ts");
    return { t, btn: [...host!.querySelectorAll("button")].find((b) => b.textContent === t("env.devidp_copy"))! };
  };

  it("does not claim a copy when the Clipboard API is missing", async () => {
    api.mockResolvedValue(idp());
    const { PreviewTab } = await freshModule();
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    await mount(<PreviewTab />);
    const { t, btn } = await copyBtn();
    await act(async () => btn.click());
    expect(toastSpy).toHaveBeenCalledWith(t("common.copy_failed"));
    expect(toastSpy).not.toHaveBeenCalledWith(t("env.devidp_copied"));
  });

  it("reports a denied write without an unhandled rejection", async () => {
    api.mockResolvedValue(idp());
    const { PreviewTab } = await freshModule();
    Object.defineProperty(navigator, "clipboard", { value: { writeText: () => Promise.reject(new Error("denied")) }, configurable: true });
    const unhandled = vi.fn();
    process.on("unhandledRejection", unhandled);
    await mount(<PreviewTab />);
    const { t, btn } = await copyBtn();
    await act(async () => btn.click());
    await new Promise((r) => setTimeout(r, 0));
    process.off("unhandledRejection", unhandled);
    expect(toastSpy).toHaveBeenCalledWith(t("common.copy_failed"));
    expect(toastSpy).not.toHaveBeenCalledWith(t("env.devidp_copied"));
    expect(unhandled).not.toHaveBeenCalled();
  });

  const rotateBtn = async () => {
    const { t } = await import("../../../lib/i18n/index.ts");
    return { t, btn: [...host!.querySelectorAll("button")].find((x) => x.textContent === t("env.devidp_rotate"))! };
  };
  const settle = () =>
    act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });

  it("rotate queues behind a slow save: the late save answer cannot bring the old client id back", async () => {
    api.mockResolvedValue(idp());
    const { PreviewTab } = await freshModule();
    const pending: Array<(v: unknown) => void> = [];
    apiJSON.mockImplementation(() => new Promise((r) => pending.push(r)));
    await mount(<PreviewTab />);
    const input = document.querySelector<HTMLInputElement>(".ds-select")!;
    await act(async () => input.dispatchEvent(new FocusEvent("focusout", { bubbles: true })));
    expect(apiJSON).toHaveBeenCalledTimes(1);
    const { btn } = await rotateBtn();
    await act(async () => btn.click());
    await settle();
    // Not sent while the save is in flight.
    expect(apiJSON).toHaveBeenCalledTimes(1);
    await act(async () => pending[0](idp({ devIdpClientId: "afdev_old" })));
    await settle();
    expect(apiJSON).toHaveBeenCalledTimes(2);
    expect(apiJSON).toHaveBeenLastCalledWith("api/env/ws-settings/dev-idp/rotate", "POST", {});
    await act(async () => pending[1]({ devIdpClientId: "afdev_new", restartRequired: true }));
    await settle();
    expect(text()).toContain("afdev_new");
    expect(text()).not.toContain("afdev_old");
  });

  it("a lost rotate answer re-reads the settings and says the secret may have changed", async () => {
    let reads = 0; // the personas list is read from the same mock, so count ws-settings reads only
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/ws-settings" ? idp(++reads > 1 ? { devIdpClientId: "afdev_after" } : {}) : { personas: [], max: 20, domain: "example.test" }),
    );
    apiJSON.mockRejectedValue(new Error("network"));
    const { PreviewTab } = await freshModule();
    const unhandled = vi.fn();
    process.on("unhandledRejection", unhandled);
    await mount(<PreviewTab />);
    const { t, btn } = await rotateBtn();
    await act(async () => btn.click());
    await settle();
    await new Promise((r) => setTimeout(r, 0));
    process.off("unhandledRejection", unhandled);
    expect(apiJSON).toHaveBeenCalledTimes(1); // no automatic retry: that would rotate again
    expect(toastSpy).toHaveBeenCalledWith(t("env.devidp_rotate_lost_reloaded"));
    expect(text()).toContain("afdev_after");
    expect(unhandled).not.toHaveBeenCalled();
  });

  it("when the re-read fails too, the old client id is not shown as current", async () => {
    let reads = 0;
    api.mockImplementation((path: string) =>
      path !== "api/env/ws-settings"
        ? Promise.resolve({ personas: [], max: 20, domain: "example.test" })
        : ++reads > 1
          ? Promise.reject(new Error("network"))
          : Promise.resolve(idp()),
    );
    apiJSON.mockRejectedValue(new Error("network"));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const { t, btn } = await rotateBtn();
    await act(async () => btn.click());
    await settle();
    expect(toastSpy).toHaveBeenCalledWith(t("env.devidp_rotate_lost_unknown"));
    expect(text()).not.toContain("afdev_abc");
  });
});

describe("test personas editor", () => {
  const PATH = "api/env/ws-settings/dev-idp/personas";
  const persona = (handle: string) => ({ id: "id-" + handle, handle, name: handle.toUpperCase(), email: `${handle}@example.test`, sub: `persona:${handle}` });
  const serve = (personas: unknown[], extra: Record<string, unknown> = {}) =>
    api.mockImplementation((path: string) =>
      Promise.resolve(path === PATH ? { personas, max: 20, domain: "example.test" } : { ...wsSettings(), devIdpAvailable: true, devIdp: true, ...extra }),
    );
  const type = async (el: HTMLInputElement, v: string) =>
    act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, v);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  const inputByLabel = async (key: "env.persona_handle" | "env.persona_name") => {
    const { t } = await import("../../../lib/i18n/index.ts");
    return host!.querySelector<HTMLInputElement>(`input[aria-label="${t(key)}"]`)!;
  };
  const btn = async (key: "env.persona_add" | "env.persona_remove") => {
    const { t } = await import("../../../lib/i18n/index.ts");
    return [...host!.querySelectorAll("button")].find((b) => b.textContent === t(key))!;
  };

  it("lists the personas with their derived address and sub", async () => {
    serve([persona("admin")]);
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    expect(host!.textContent).toContain("admin@example.test");
    expect(host!.textContent).toContain("persona:admin");
  });

  it("is absent where the IdP is not available", async () => {
    serve([], { devIdpAvailable: false, devIdp: false });
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const { t } = await import("../../../lib/i18n/index.ts");
    expect(host!.textContent).not.toContain(t("env.persona_title"));
    expect(api).not.toHaveBeenCalledWith(PATH);
  });

  it("adds a persona, clears the form and shows the answer", async () => {
    serve([]);
    apiJSON.mockResolvedValue({ personas: [persona("viewer")], domain: "example.test" });
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    expect((await btn("env.persona_add")).disabled).toBe(true); // nothing typed yet
    await type(await inputByLabel("env.persona_handle"), "viewer");
    await type(await inputByLabel("env.persona_name"), "Viewer");
    await act(async () => (await btn("env.persona_add")).click());
    expect(apiJSON).toHaveBeenCalledWith(PATH, "POST", { handle: "viewer", name: "Viewer" });
    expect(host!.textContent).toContain("viewer@example.test");
    expect((await inputByLabel("env.persona_handle")).value).toBe("");
  });

  it("keeps the typed handle and says why when the server refuses it", async () => {
    serve([]);
    apiJSON.mockResolvedValue({ error: { code: "invalid_persona", message: "handle must be lower case" } });
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    await type(await inputByLabel("env.persona_handle"), "Admin");
    await act(async () => (await btn("env.persona_add")).click());
    expect(toastSpy).toHaveBeenCalledWith(expect.stringContaining("handle must be lower case"));
    expect((await inputByLabel("env.persona_handle")).value).toBe("Admin");
  });

  it("removes a persona", async () => {
    serve([persona("admin")]);
    apiJSON.mockResolvedValue({ personas: [], domain: "example.test" });
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    await act(async () => (await btn("env.persona_remove")).click());
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/id-admin`, "DELETE");
    expect(host!.textContent).not.toContain("admin@example.test");
  });

  it("blocks edits while the first load is out, so a late list cannot wipe an add", async () => {
    let release!: (v: unknown) => void;
    api.mockImplementation((path: string) =>
      path === PATH ? new Promise((r) => (release = r)) : Promise.resolve({ ...wsSettings(), devIdpAvailable: true, devIdp: true }),
    );
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    await type(await inputByLabel("env.persona_handle"), "viewer");
    expect((await btn("env.persona_add")).disabled).toBe(true); // GET still out
    await act(async () => (await btn("env.persona_add")).click());
    expect(apiJSON).not.toHaveBeenCalled();
    await act(async () => release({ personas: [persona("admin")], domain: "example.test" }));
    expect((await btn("env.persona_add")).disabled).toBe(false);
    expect(host!.textContent).toContain("admin@example.test");
  });

  it("serialises edits: a second click while one is out does nothing, and a late answer is not lost", async () => {
    serve([]);
    let release!: (v: unknown) => void;
    apiJSON.mockImplementation(() => new Promise((r) => (release = r)));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    await type(await inputByLabel("env.persona_handle"), "viewer");
    await act(async () => (await btn("env.persona_add")).click());
    await act(async () => (await btn("env.persona_add")).click());
    expect(apiJSON).toHaveBeenCalledTimes(1);
    await act(async () => release({ personas: [persona("viewer")], domain: "example.test" }));
    expect(host!.textContent).toContain("viewer@example.test");
  });

  it("frees the editor and reports it when a request fails outright", async () => {
    serve([]);
    apiJSON.mockRejectedValue(new Error("network"));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    await type(await inputByLabel("env.persona_handle"), "viewer");
    await act(async () => (await btn("env.persona_add")).click());
    expect(toastSpy).toHaveBeenCalled();
    expect((await btn("env.persona_add")).disabled).toBe(false);
  });

  it("loads and stays usable under StrictMode (setup, cleanup, setup)", async () => {
    serve([persona("admin")]);
    apiJSON.mockResolvedValue({ personas: [persona("admin"), persona("viewer")], domain: "example.test" });
    const { PreviewTab } = await freshModule();
    const { StrictMode } = await import("react");
    await mount(
      <StrictMode>
        <PreviewTab />
      </StrictMode>,
    );
    expect(host!.textContent).toContain("admin@example.test");
    await type(await inputByLabel("env.persona_handle"), "viewer");
    await act(async () => (await btn("env.persona_add")).click());
    expect(host!.textContent).toContain("viewer@example.test");
  });
});
