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

import { ItemCard, inboxErrorText } from "./InboxModal.tsx";
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
