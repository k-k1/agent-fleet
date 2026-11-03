// The sender's side of a handoff whose recipient holds it for a launch (#2058): the card's
// status line and the refused withdraw say whether the recipient is still starting it or their
// launch's result is being confirmed, instead of a generic "pending" and "could not withdraw".
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { answerConfirm, confirmDialog } from "../../test/confirmDialog.ts";
import type { HandoffOffer } from "../sharing/handoffStore.ts";

const apiJSON = vi.fn();
let owned: HandoffOffer[] = [];
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return {
    ...real,
    apiJSON: (...a: unknown[]) => apiJSON(...a),
    api: vi.fn(async (path: string) => ({ offers: path === "api/session-handoff-offers" ? owned : [] })),
  };
});

const { HandoffProposal } = await import("./HandoffProposal.tsx");
const { useHandoffStore } = await import("../sharing/handoffStore.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

const base: HandoffOffer = {
  id: "offer-1", sessionId: "cat-1", sessionName: "src", recipientUserKey: "bob", title: "Carry on", status: "pending",
};
const proposal = { id: "p-1", prompt: "Continue.", title: "Carry on", created_at: 1 };

let root: Root | null = null;
let host: HTMLDivElement;

async function render(offer: HandoffOffer) {
  owned = [offer];
  useHandoffStore.setState({ owned });
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <HandoffProposal session="src" proposal={proposal} onChange={() => undefined} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
}

const launchButton = () =>
  [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(t("mirror.handoff_launch"))) as HTMLButtonElement;

beforeEach(() => {
  apiJSON.mockReset();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  owned = [];
  useHandoffStore.setState({ owned: [], received: [] });
  vi.restoreAllMocks();
});

describe("the sender's handoff card while the recipient holds the offer", () => {
  it("says plainly pending when nobody holds it", async () => {
    await render(base);
    expect(host.textContent).toContain(t("handoff.state_pending", { who: "bob" }));
  });

  it("says the recipient is starting it", async () => {
    await render({ ...base, takeState: "starting" });
    expect(host.textContent).toContain(t("handoff.state_starting", { who: "bob" }));
  });

  it("says the recipient's launch result is being confirmed, with no internal detail", async () => {
    for (const takeState of ["confirming", "unknown"] as const) {
      await render({ ...base, takeState });
      expect(host.textContent).toContain(t("handoff.state_confirming", { who: "bob" }));
    }
    expect(host.textContent).not.toMatch(/token|idempotency|handoff-offer-/i);
  });

  it("words a withdraw refused under a confirmed launch as being confirmed", async () => {
    apiJSON.mockResolvedValue({
      error: { code: "handoff_being_taken", offerStatus: "pending", takeState: "unknown", status: 409 },
    });
    await render({ ...base, takeState: "unknown" });
    await act(async () => launchButton().click());
    await answerConfirm(true);
    expect(apiJSON).toHaveBeenCalledWith("api/session-handoff-offers/offer-1", "DELETE");
    expect(document.body.textContent).toContain(t("handoff.withdraw_confirming", { who: "bob" }));
    expect(document.body.textContent).not.toContain(t("handoff.withdraw_lost"));
  });

  it("words a withdraw refused while the recipient is starting it, and a decided one as before", async () => {
    apiJSON.mockResolvedValueOnce({ error: { code: "handoff_being_taken", takeState: "starting", status: 409 } });
    await render({ ...base, takeState: "starting" });
    await act(async () => launchButton().click());
    await answerConfirm(true);
    expect(document.body.textContent).toContain(t("handoff.withdraw_starting", { who: "bob" }));

    apiJSON.mockResolvedValueOnce({ error: { code: "handoff_already_decided", offerStatus: "accepted", status: 409 } });
    await render(base);
    await act(async () => launchButton().click());
    await answerConfirm(true);
    expect(document.body.textContent).toContain(t("handoff.withdraw_lost"));
  });
});

// The picker named the recipient by e-mail, so the card must too; the internal key is the
// fallback for a recipient with no address (#2075).
describe("the sender's handoff card names the recipient as the picker did", () => {
  const withEmail = { ...base, recipientUserKey: "bob-example-com", recipientEmail: "bob@example.com" };

  it("shows the e-mail on the status line, not the user key", async () => {
    await render(withEmail);
    expect(host.textContent).toContain(t("handoff.state_pending", { who: "bob@example.com" }));
    expect(host.textContent).not.toContain("bob-example-com");
  });

  it("asks the withdraw question with the e-mail", async () => {
    await render(withEmail);
    await act(async () => launchButton().click());
    const dlg = confirmDialog()!;
    expect(dlg.querySelector(".ui-confirm-title")?.textContent).toBe(t("handoff.withdraw_confirm_title"));
    expect(dlg.textContent).toContain(t("handoff.withdraw_confirm_body", { who: "bob@example.com" }));
    expect(dlg.querySelector(".ui-btn-danger, .btn-danger")?.textContent).toBe(t("handoff.withdraw_confirm_action"));
    await answerConfirm(false);
    expect(confirmDialog()).toBeNull();
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("words a refused withdraw with the e-mail", async () => {
    apiJSON.mockResolvedValue({ error: { code: "handoff_being_taken", takeState: "starting", status: 409 } });
    await render({ ...withEmail, takeState: "starting" });
    await act(async () => launchButton().click());
    await answerConfirm(true);
    expect(document.body.textContent).toContain(t("handoff.withdraw_starting", { who: "bob@example.com" }));
  });
});
