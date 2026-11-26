// The inbox card as the recipient sees it (ADR 0111 decision 2, docs/log/130 R-1).
//
// What is held: the buttons follow the state the CP projected (claim and decline only while
// the caller can take it), a refused claim names who took it instead of a bare error, and the
// sender's refusal message names the recipients who do not accept.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => api(path),
  apiJSON: (path: string, method: string, body: unknown) => apiJSON(path, method, body),
  errText: (e: unknown) => String((e as { code?: string })?.code ?? e),
  getTenant: () => "",
  rel: (p: string) => p,
}));

// The space dialog is loaded on first use and is tested on its own (SpaceLaunchModal.dom.test.tsx);
// here it only has to open with the request as its prompt.
let spaceModalProps: { prompt: string; inboxItemId: string; onClose: () => void } | null = null;
vi.mock("../repos/SpaceLaunchModal.tsx", () => ({
  SpaceLaunchModal: (p: { prompt: string; inboxItemId: string; onClose: () => void }) => {
    spaceModalProps = p;
    return <div data-testid="space-modal">{p.prompt}</div>;
  },
}));

import { ItemCard, SpaceLaunchHost, inboxErrorText } from "./InboxModal.tsx";
import { useLaunchSeed, useLaunchTarget, useReposStore } from "../repos/store.ts";
import type { InboxItem } from "./store.ts";
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const base: InboxItem = {
  id: "item-1",
  kind: "request",
  state: "open",
  role: "recipient",
  delivery: "unread",
  sender: { userKey: "a-example-com", email: "a@example.com", removed: false },
  recipients: [],
  assignee: null,
  assignedToMe: false,
  body: "please look at the flaky test\nsecond line",
  canClaim: true,
  longOpen: false,
  unseen: true,
  seq: 3,
  createdAt: "2026-10-12T00:00:00Z",
  updatedAt: "2026-10-12T00:00:00Z",
  finalAt: "",
  session: null,
  pr: null,
};

async function render(item: InboxItem) {
  host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host!);
    root.render(
      <ToastProvider>
        <ul>
          <ItemCard item={item} />
        </ul>
        <SpaceLaunchHost />
      </ToastProvider>,
    );
  });
}

const buttons = () => [...document.querySelectorAll("button")];
const byText = (t: string) => buttons().find((b) => b.textContent?.includes(t));

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  spaceModalProps = null;
  useLaunchSeed.getState().clear();
  useLaunchTarget.getState().clear();
  useReposStore.setState({ repos: [] });
});

describe("ItemCard", () => {
  it("opens an unread request, marks it read, and offers claim and decline", async () => {
    apiJSON.mockResolvedValue({});
    api.mockResolvedValue({ items: [], mailbox: null });
    await render(base);
    expect(document.body.textContent).toContain("please look at the flaky test");
    await act(async () => {
      document.querySelector<HTMLButtonElement>(".inbox-card-head")!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/inbox/items/item-1/read", "POST", {});
    expect(byText("引き受ける") ?? byText("Take it")).toBeTruthy();
    expect(byText("辞退する") ?? byText("Decline")).toBeTruthy();
  });

  it("binds each expanded row's repo label to its own select", async () => {
    const mine = { ...base, state: "claimed" as const, canClaim: false, delivery: "read" as const, assignedToMe: true, assignee: { userKey: "me", email: "me@example.com", removed: false } };
    host = document.createElement("div");
    document.body.appendChild(host);
    await act(async () => {
      root = createRoot(host!);
      root.render(
        <ToastProvider>
          <ul>
            <ItemCard item={{ ...mine, id: "i1" }} />
            <ItemCard item={{ ...mine, id: "i2" }} />
          </ul>
        </ToastProvider>,
      );
    });
    for (const h of document.querySelectorAll<HTMLButtonElement>(".inbox-card-head")) {
      await act(async () => h.click());
    }
    const rows = [...document.querySelectorAll(".inbox-launch")];
    expect(rows.length).toBe(2);
    for (const r of rows) {
      const label = r.querySelector<HTMLLabelElement>("label")!;
      expect(label.control).toBe(r.querySelector("select"));
    }
  });

  it("shows no claim button on a request somebody else took", async () => {
    await render({ ...base, state: "claimed", canClaim: false, delivery: "read", assignee: { userKey: "c", email: "c@example.com", removed: false } });
    await act(async () => {
      document.querySelector<HTMLButtonElement>(".inbox-card-head")!.click();
    });
    expect(byText("引き受ける") ?? byText("Take it")).toBeUndefined();
    expect(document.body.textContent).toContain("c@example.com");
  });

  it("sends the claim and survives a 409 without a red error left on the card", async () => {
    apiJSON.mockImplementation(async (path: string) =>
      path.endsWith("/claim") ? { error: { code: "inbox_state_conflict", state: "claimed" } } : {},
    );
    api.mockResolvedValue({ items: [], mailbox: null });
    await render({ ...base, delivery: "read" });
    await act(async () => {
      document.querySelector<HTMLButtonElement>(".inbox-card-head")!.click();
    });
    await act(async () => {
      (byText("引き受ける") ?? byText("Take it"))!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/inbox/items/item-1/claim", "POST", {});
    // The list is re-read after the refusal, so the card shows the state the CP holds.
    expect(api).toHaveBeenCalledWith("api/inbox");
  });

  it("offers the sender a withdraw and shows each recipient's delivery", async () => {
    await render({
      ...base,
      role: "sender",
      canClaim: false,
      delivery: "read",
      recipients: [
        { userKey: "b", email: "b@example.com", removed: false, delivery: "declined", eligible: false },
        { userKey: "", email: "", removed: true, delivery: "unread", eligible: false },
      ],
    });
    await act(async () => {
      document.querySelector<HTMLButtonElement>(".inbox-card-head")!.click();
    });
    expect(byText("取り下げる") ?? byText("Withdraw")).toBeTruthy();
    expect(byText("引き受ける") ?? byText("Take it")).toBeUndefined();
    expect(document.body.textContent).toMatch(/辞退|Declined/);
    expect(document.body.textContent).toMatch(/外れたメンバー|removed member/);
  });
});

describe("inboxErrorText", () => {
  const tr = (k: string, v?: Record<string, string>) => k + (v ? JSON.stringify(v) : "");
  it("names the recipients who do not accept (docs/log/130 §130.8 item 1)", () => {
    const s = inboxErrorText(tr as never, { code: "inbox_recipient_not_accepting", message: "", recipients: ["b", "c"] } as never);
    expect(s).toBe('inbox.refused{"who":"b, c"}');
  });
  it("falls back to the catalogue for other codes", () => {
    expect(inboxErrorText(tr as never, { code: "inbox_invalid_recipient", message: "" })).toBe("inbox_invalid_recipient");
  });
});

describe("the assignee's launch", () => {
  const mine = { ...base, state: "claimed" as const, canClaim: false, delivery: "read" as const, assignedToMe: true, assignee: { userKey: "me", email: "me@example.com", removed: false } };
  const select = () => document.querySelector<HTMLSelectElement>(".inbox-launch select")!;
  const launchBtn = () => document.querySelector<HTMLButtonElement>(".inbox-launch .ui-btn-primary")!;
  async function open(repos: { name: string; path: string }[]) {
    useReposStore.setState({ repos: repos as never });
    await render(mine);
    await act(async () => {
      document.querySelector<HTMLButtonElement>(".inbox-card-head")!.click();
    });
  }
  async function pick(el: HTMLSelectElement, label: string) {
    const opt = [...el.options].find((o) => o.text.includes(label))!;
    await act(async () => {
      el.value = opt.value;
      el.dispatchEvent(new Event("change", { bubbles: true }));
    });
  }

  it("a member without working copies is offered a space, by default, and can start", async () => {
    await open([]);
    expect(select().selectedOptions[0].text).toMatch(/スペース|space/i);
    expect(launchBtn().disabled).toBe(false);
    expect(document.querySelector(".inbox-warn")).toBeNull();
    await act(async () => launchBtn().click());
    expect(document.querySelector("[data-testid=space-modal]")?.textContent).toBe(base.body);
    // The request travels as props, so the space launch goes through the request route and the
    // global seed (which another launch may read) is never written.
    expect(spaceModalProps?.inboxItemId).toBe("item-1");
    expect(useLaunchSeed.getState().inboxItemId).toBe("");
    expect(useLaunchSeed.getState().prompt).toBe("");
    expect(useLaunchTarget.getState().target).toBeNull();
  });

  it("keeps the working copy as the default and offers the space beside it", async () => {
    await open([{ name: "app", path: "/home/dev/repos/app" }]);
    expect(select().selectedOptions[0].text).toBe("app");
    await act(async () => launchBtn().click());
    expect(useLaunchTarget.getState().target?.name).toBe("app");
    expect(useLaunchSeed.getState().inboxItemId).toBe("item-1");
    expect(document.querySelector("[data-testid=space-modal]")).toBeNull();
  });

  it("starts in a space when that is picked over a working copy", async () => {
    await open([{ name: "app", path: "/home/dev/repos/app" }]);
    await pick(select(), "app");
    await pick(select(), spaceOptionText());
    await act(async () => launchBtn().click());
    expect(document.querySelector("[data-testid=space-modal]")).not.toBeNull();
    expect(useLaunchTarget.getState().target).toBeNull();
  });

  it("keeps the space dialog (and what is typed in it) when the request's card goes away", async () => {
    await open([]);
    await act(async () => launchBtn().click());
    expect(document.querySelector("[data-testid=space-modal]")).not.toBeNull();
    // The sender withdrew it: the card is gone from the list, the dialog is not.
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ul />
          <SpaceLaunchHost />
        </ToastProvider>,
      );
    });
    expect(document.querySelector(".inbox-launch")).toBeNull();
    expect(document.querySelector("[data-testid=space-modal]")?.textContent).toBe(base.body);
  });

  it("closes the space dialog without touching the seed", async () => {
    await open([]);
    await act(async () => launchBtn().click());
    await act(async () => spaceModalProps!.onClose());
    expect(document.querySelector("[data-testid=space-modal]")).toBeNull();
    expect(useLaunchSeed.getState().inboxItemId).toBe("");
  });
});

function spaceOptionText(): string {
  return [...document.querySelectorAll<HTMLOptionElement>(".inbox-launch option")].find((o) => /スペース|space/i.test(o.text))!.text;
}
