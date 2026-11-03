// Handoffs another member sent appear in the top-bar inbox (#2075), beside the ADR 0111 items
// but read from their own store and route: the section lists the pending received offers, an
// entry opens the same row the shared view shows, and a decided offer leaves with the next
// read. The badge counts them as a backlog, untouched by do-not-disturb.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { HandoffOffer } from "../sharing/handoffStore.ts";
import type { InboxMailbox } from "./store.ts";

let received: HandoffOffer[] = [];
let mailbox: InboxMailbox | null = null;
const api = vi.fn(async (path: string) => {
  if (path === "api/session-handoff-offers/received") return { offers: received };
  if (path === "api/session-handoff-offers") return { offers: [] };
  if (path === "api/inbox") return { items: [], mailbox };
  return {};
});
const apiJSON = vi.fn(async (..._a: unknown[]) => ({}) as Record<string, unknown>);
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => api(path),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
  errText: (e: unknown) => String((e as { code?: string })?.code ?? e),
  getTenant: () => "",
  rel: (p: string) => p,
}));

const { InboxModal } = await import("./InboxModal.tsx");
const { InboxButton } = await import("./InboxButton.tsx");
const { useInboxStore } = await import("./store.ts");
const { useTenantStore } = await import("../../core/store/tenant.ts");
const { useHandoffStore } = await import("../sharing/handoffStore.ts");
const { useLaunchSeed, useLaunchTarget, useReposStore } = await import("../repos/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

const OFFER: HandoffOffer = {
  id: "offer-1",
  sessionId: "cat-1",
  sessionName: "session-1",
  recipientUserKey: "bob-example-com",
  ownerUserKey: "alice-example-com",
  ownerEmail: "alice@example.com",
  title: "Carry on with the parser",
  status: "pending",
  branch: "feature/x",
  headSha: "abcdef1234567890",
  repoRemote: "https://example.com/team/private.git",
  createdAt: "2026-10-12T00:00:00Z",
  prompt: "Continue the work.",
};

let root: Root | null = null;
let host: HTMLDivElement;

const flush = async () => {
  await act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });
};

async function renderModal(onClose = () => undefined) {
  await act(async () => {
    root!.render(
      <ToastProvider>
<ConfirmProvider>
        <InboxModal onClose={onClose} />
      </ConfirmProvider>
</ToastProvider>,
    );
  });
  await flush();
}

const section = () => document.body.querySelector(".inbox-handoffs");
const button = (text: string) =>
  [...document.body.querySelectorAll("button")].find((b) => b.textContent?.includes(text)) as HTMLButtonElement | undefined;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  received = [];
  mailbox = null;
  useReposStore.setState({ repos: [{ name: "private", path: "/r/private" }] } as never);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  api.mockClear();
  apiJSON.mockClear();
  useHandoffStore.setState({ owned: [], received: [], receivedAt: 0 });
  useInboxStore.getState().reset();
  useLaunchSeed.getState().clear();
  useLaunchTarget.getState().clear();
  vi.unstubAllGlobals();
});

describe("the inbox's handoff section", () => {
  it("is absent with nothing pending, and the empty text still shows", async () => {
    await renderModal();
    expect(section()).toBeNull();
    expect(document.body.textContent).toContain(t("inbox.empty_received"));
  });

  it("lists a pending handoff by title and the sender's e-mail, read from the handoff route", async () => {
    received = [OFFER];
    await renderModal();
    expect(api).toHaveBeenCalledWith("api/session-handoff-offers/received");
    const s = section();
    expect(s).toBeTruthy();
    expect(s!.textContent).toContain(t("inbox.handoffs"));
    expect(s!.textContent).toContain("Carry on with the parser");
    expect(s!.textContent).toContain(t("inbox.from", { who: "alice@example.com" }));
    expect(s!.textContent).not.toContain("alice-example-com");
    expect(document.body.textContent).not.toContain(t("inbox.empty_received"));
  });

  it("does not read the route again while the store's list is fresh", async () => {
    useHandoffStore.setState({ received: [OFFER], receivedAt: Date.now() });
    await renderModal();
    expect(api).not.toHaveBeenCalledWith("api/session-handoff-offers/received");
    expect(section()!.textContent).toContain("Carry on with the parser");
  });

  it("opens the existing offer row, and accepting closes the inbox for the launch dialog", async () => {
    received = [OFFER];
    const onClose = vi.fn();
    await renderModal(onClose);
    await act(async () => section()!.querySelector<HTMLButtonElement>(".inbox-card-head")!.click());
    const row = section()!.querySelector(".handoff-inbox-row");
    expect(row).toBeTruthy();
    expect(row!.textContent).toContain("Continue the work.");
    await act(async () => button(t("handoff.accept"))!.click());
    expect(useLaunchSeed.getState().handoffOfferId).toBe("offer-1");
    expect(onClose).toHaveBeenCalled();
  });

  it("drops a declined handoff once the route stops listing it", async () => {
    received = [OFFER];
    await renderModal();
    await act(async () => section()!.querySelector<HTMLButtonElement>(".inbox-card-head")!.click());
    received = [];
    await act(async () => button(t("handoff.decline"))!.click());
    await flush();
    expect(apiJSON).toHaveBeenCalledWith("api/session-handoff-offers/offer-1/decline", "POST", {});
    expect(section()).toBeNull();
  });

  it("drops a handoff withdrawn, expired or taken elsewhere on the next read", async () => {
    received = [OFFER];
    await renderModal();
    expect(section()).toBeTruthy();
    received = [];
    await act(async () => useHandoffStore.getState().refresh());
    expect(section()).toBeNull();
  });
});

describe("the inbox badge", () => {
  async function renderButton() {
    // The inbox stream is not under test: leave it pending so the store polls nothing extra.
    vi.stubGlobal("fetch", () => new Promise(() => undefined));
    await act(async () => {
      root!.render(
        <ToastProvider>
<ConfirmProvider>
          <InboxButton />
        </ConfirmProvider>
</ToastProvider>,
      );
    });
    await flush();
  }
  const badge = () => host.querySelector(".inbox-badge")?.textContent ?? "";

  it("adds pending handoffs to the unseen inbox items", async () => {
    mailbox = { lastSeq: 2, readSeq: 1, unseen: 1, dnd: false, acceptFrom: "tenant" };
    received = [OFFER, { ...OFFER, id: "offer-2" }];
    await renderButton();
    expect(badge()).toBe("3");
    expect(host.querySelector("button")!.getAttribute("aria-label")).toContain(t("inbox.badge_handoffs", { n: 2 }));
  });

  it("keeps counting handoffs under do-not-disturb, which mutes only inbox items", async () => {
    mailbox = { lastSeq: 2, readSeq: 1, unseen: 1, dnd: true, acceptFrom: "tenant" };
    received = [OFFER];
    await renderButton();
    expect(badge()).toBe("1");
  });

  it("shows no badge once the handoff is gone", async () => {
    received = [OFFER];
    await renderButton();
    expect(badge()).toBe("1");
    received = [];
    await act(async () => useHandoffStore.getState().refresh());
    expect(badge()).toBe("");
  });
});

// A member of two tenants switches: the previous tenant's handoffs (titles, prompts, addresses)
// and a launch seeded from one of them must not follow into the new tenant (#2085 review).
describe("a tenant switch", () => {
  async function renderButton() {
    vi.stubGlobal("fetch", () => new Promise(() => undefined));
    await act(async () => {
      root!.render(
        <ToastProvider>
<ConfirmProvider>
          <InboxButton />
        </ConfirmProvider>
</ToastProvider>,
      );
    });
    await flush();
  }
  const badge = () => host.querySelector(".inbox-badge")?.textContent ?? "";
  const tenantBefore = useTenantStore.getState().tenant;
  afterEach(() => useTenantStore.setState({ tenant: tenantBefore }));

  it("drops the previous tenant's handoffs and reads the route again, even with a fresh list", async () => {
    received = [OFFER];
    await renderButton();
    expect(badge()).toBe("1");
    useHandoffStore.setState({ received: [OFFER], receivedAt: Date.now() });
    api.mockClear();
    received = [];
    await act(async () => useTenantStore.setState({ tenant: "tenant-b-" + Math.random() }));
    await flush();
    expect(useHandoffStore.getState().received).toEqual([]);
    expect(badge()).toBe("");
    expect(api).toHaveBeenCalledWith("api/session-handoff-offers/received");
    await act(async () => host.querySelector<HTMLButtonElement>(".inbox-btn")!.click());
    await flush();
    expect(section()).toBeNull();
  });

  it("drops a launch seeded from a handoff", async () => {
    await renderButton();
    useLaunchSeed.getState().set("Continue the work.", "Carry on", "", "", "offer-1");
    useLaunchTarget.getState().open({ name: "private", path: "/r/private" } as never);
    await act(async () => useTenantStore.setState({ tenant: "tenant-c-" + Math.random() }));
    await flush();
    expect(useLaunchSeed.getState().handoffOfferId).toBe("");
    expect(useLaunchTarget.getState().target).toBeNull();
  });
});
