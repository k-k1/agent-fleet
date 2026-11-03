// The recipient's side of a handoff before the launch (ADR 0057, docs/log/77 §77.18). These
// replace run-sheet steps on #1960:
//   H-8  — the working copy is guessed from the remote's basename only, with the first working
//          copy as the fallback (recorded as it behaves: no repository identity check);
//   H-9  — with no working copy the accept button is disabled and the reason is shown;
//   H-17 — accepting opens the launch dialog wherever StartHost is mounted, which in a popout
//          tab is only true because the popout shell mounts it too (app/startHost.test.ts).
// Accepting here takes nothing: the offer is taken only when the launch is pressed (#2020).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { answerConfirm, confirmDialog } from "../../test/confirmDialog.ts";
import type { HandoffOffer } from "./handoffStore.ts";

const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, apiJSON: (...a: unknown[]) => apiJSON(...a), api: vi.fn(async () => ({ offers: [] })) };
});
vi.mock("../repos/useRepoRail.ts", () => ({ useRepoRailContext: () => ({ launchKinds: ["claude"], connsSettling: false }) }));
vi.mock("../repos/StartModal.tsx", () => ({ StartModal: () => null }));
vi.mock("../mirror/HandoffProposal.tsx", () => ({ markHandoffLaunched: vi.fn() }));
vi.mock("../repos/LaunchModal.tsx", () => ({
  LaunchModal: (p: { repo: string; branch?: string; initialPrompt?: string }) => (
    <div data-testid="launch-dialog">
      {p.repo}|{p.branch}|{p.initialPrompt}
    </div>
  ),
}));

const { HandoffOfferRow, repoNameFromRemote } = await import("./HandoffOfferRow.tsx");
const { StartHost } = await import("../repos/StartHost.tsx");
const { useLaunchSeed, useLaunchTarget, useReposStore } = await import("../repos/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

const offer: HandoffOffer = {
  id: "offer-1", sessionId: "cat-1", sessionName: "session-1", recipientUserKey: "bob", ownerUserKey: "alice",
  title: "Carry on", status: "pending", branch: "feature/x", headSha: "abcdef1234567890",
  repoRemote: "https://example.com/team/private.git", prompt: "Continue the work.",
};

let root: Root | null = null;
let host: HTMLDivElement;

async function render(repos: { name: string; path?: string }[], withStartHost = false, row: HandoffOffer = offer) {
  useReposStore.setState({ repos } as never);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ul>
            <HandoffOfferRow offer={row} onDone={() => undefined} />
          </ul>
          {withStartHost && <StartHost />}
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
}

const select = () => host.querySelector("select") as HTMLSelectElement;
const giveBackButton = () =>
  [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(t("handoff.give_back"))) as HTMLButtonElement | undefined;
const acceptButton = () =>
  [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(t("handoff.accept"))) as HTMLButtonElement;

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
  useLaunchSeed.getState().clear();
  useLaunchTarget.getState().clear();
  useReposStore.setState({ repos: [] } as never);
});

describe("the received handoff row", () => {
  it("guesses the working copy from the remote's basename (H-8)", async () => {
    expect(repoNameFromRemote("https://example.com/team/private.git")).toBe("private");
    expect(repoNameFromRemote("git@example.com:team/private.git/")).toBe("private");
    await render([{ name: "other", path: "/r/other" }, { name: "private", path: "/r/private" }]);
    expect(select().value).toBe("private");
  });

  it("falls back to the first working copy when no folder matches the basename (H-8)", async () => {
    // Recorded as it behaves: a clone of the same repository under another folder name is
    // not recognised, and nothing checks repository identity or HEAD.
    await render([{ name: "alpha", path: "/r/alpha" }, { name: "private-clone", path: "/r/private-clone" }]);
    expect(select().value).toBe("alpha");
  });

  it("disables accepting and says why when there is no working copy (H-9)", async () => {
    await render([]);
    expect(acceptButton().disabled).toBe(true);
    expect(host.textContent).toContain(t("handoff.accept_no_repo_hint"));
  });

  it("opens the pre-filled launch dialog on accept and takes nothing yet (H-17)", async () => {
    await render([{ name: "private", path: "/r/private" }], true);
    expect(document.querySelector("[data-testid=launch-dialog]")).toBeNull();
    await act(async () => acceptButton().click());
    const dialog = document.querySelector("[data-testid=launch-dialog]");
    expect(dialog?.textContent).toBe("private|feature/x|Continue the work.");
    expect(useLaunchSeed.getState().handoffOfferId).toBe("offer-1");
    // Opening the dialog must not hold the offer: a cancelled dialog leaves it pending.
    expect(apiJSON).not.toHaveBeenCalled();
  });

  // #2058: a launch whose result stays unknown can be given back, by the recipient only, after a
  // confirmation that names the risk; a launch still in progress cannot.
  describe("giving back a launch whose result is unknown", () => {
    const unknown: HandoffOffer = { ...offer, takeState: "unknown", takeRev: "rev-a" };
    const repos = [{ name: "private", path: "/r/private" }];

    it("is offered only for an unknown take", async () => {
      await render(repos);
      expect(giveBackButton()).toBeUndefined();
      await render(repos, false, { ...offer, takeState: "confirming" });
      expect(giveBackButton()).toBeUndefined();
      expect(host.textContent).toContain(t("handoff.confirming_hint"));
      await render(repos, false, unknown);
      expect(giveBackButton()).toBeDefined();
      expect(host.textContent).toContain(t("handoff.unknown_hint"));
    });

    it("asks first, naming the risk, and sends nothing when cancelled", async () => {
      await render(repos, false, unknown);
      await act(async () => giveBackButton()!.click());
      const dlg = confirmDialog()!;
      expect(dlg.querySelector(".ui-confirm-title")?.textContent).toBe(t("handoff.give_back_confirm_title", { who: "alice" }));
      // It must say both: the session may exist, and a launch still in progress is not cancelled.
      expect(dlg.textContent).toMatch(/twice/);
      expect(dlg.textContent).toMatch(/does not cancel/);
      expect(dlg.textContent).toMatch(/still being processed/);
      expect(dlg.querySelector(".btn-danger, .ui-btn-danger")?.textContent).toBe(t("handoff.give_back_confirm_action"));
      await answerConfirm(false);
      expect(confirmDialog()).toBeNull();
      expect(apiJSON).not.toHaveBeenCalled();
    });

    it("gives it back once confirmed and says so", async () => {
      apiJSON.mockResolvedValue({ status: "pending" });
      await render(repos, false, unknown);
      await act(async () => giveBackButton()!.click());
      expect(apiJSON).not.toHaveBeenCalled();
      await answerConfirm(true);
      expect(apiJSON).toHaveBeenCalledWith("api/session-handoff-offers/offer-1/give-back", "POST", { confirmed: true, takeRev: "rev-a" });
      expect(document.body.textContent).toContain(t("handoff.give_back_done", { who: "alice" }));
    });

    it("says the launch may still be running when the CP refuses", async () => {
      apiJSON.mockResolvedValue({ error: { code: "handoff_not_unknown", takeState: "confirming", status: 409 } });
      await render(repos, false, unknown);
      await act(async () => giveBackButton()!.click());
      await answerConfirm(true);
      expect(document.body.textContent).toContain(t("handoff.give_back_too_soon"));
    });

    it("asks to check again when the row's launch was replaced meanwhile", async () => {
      apiJSON.mockResolvedValue({ error: { code: "handoff_give_back_stale", takeState: "unknown", status: 409 } });
      await render(repos, false, unknown);
      await act(async () => giveBackButton()!.click());
      await answerConfirm(true);
      expect(document.body.textContent).toContain(t("handoff.give_back_stale"));
    });

    it("shows an unshared or archived offer held by the launch as a give-back-only row", async () => {
      const recovery: HandoffOffer = {
        ...unknown, recoveryOnly: true, prompt: "", branch: "", headSha: "", repoRemote: "",
      };
      await render(repos, false, recovery);
      expect(host.textContent).toContain(t("handoff.recovery_hint"));
      expect(giveBackButton()).toBeDefined();
      expect(acceptButton()).toBeUndefined();
      expect(host.querySelector("select")).toBeNull();
      expect(host.querySelector("pre")).toBeNull();
      expect(host.textContent).not.toContain(t("handoff.decline"));
      // Still within the window: no give-back yet, only the hint.
      await render(repos, false, { ...recovery, takeState: "confirming" });
      expect(giveBackButton()).toBeUndefined();
      expect(host.textContent).toContain(t("handoff.confirming_hint"));
    });
  });
});

// The sender is named by e-mail, as the recipient's shared-sessions list names them (#2075).
describe("HandoffOfferRow names the sender", () => {
  const repos = [{ name: "private", path: "/r/private" }];
  const fromAlice = { ...offer, ownerUserKey: "alice-example-com", ownerEmail: "alice@example.com" };

  it("by e-mail when the CP sends one", async () => {
    await render(repos, false, fromAlice);
    expect(host.textContent).toContain(t("handoff.from", { who: "alice@example.com" }));
    expect(host.textContent).not.toContain("alice-example-com");
  });

  it("by e-mail in the give-back question and answer", async () => {
    apiJSON.mockResolvedValue({ status: "pending" });
    await render(repos, false, { ...fromAlice, takeState: "unknown", takeRev: "rev-a" });
    await act(async () => giveBackButton()!.click());
    expect(confirmDialog()!.querySelector(".ui-confirm-title")?.textContent).toBe(t("handoff.give_back_confirm_title", { who: "alice@example.com" }));
    await answerConfirm(true);
    expect(document.body.textContent).toContain(t("handoff.give_back_done", { who: "alice@example.com" }));
  });

  it("by the key when there is no e-mail", async () => {
    await render(repos, false, offer);
    expect(host.textContent).toContain(t("handoff.from", { who: "alice" }));
  });
});
