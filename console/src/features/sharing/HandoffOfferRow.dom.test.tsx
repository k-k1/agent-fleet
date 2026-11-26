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
// api("api/spaces") answers a list of spaces (an Agent that supports them); anything else is the
// handoff store's own read.
const spacesOk = async (...a: unknown[]): Promise<unknown> => (String(a[0]).includes("api/spaces") ? { spaces: [] } : { offers: [] });
const apiGet = vi.fn(spacesOk);
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, apiJSON: (...a: unknown[]) => apiJSON(...a), api: (...a: unknown[]) => apiGet(...a) };
});
vi.mock("../repos/useRepoRail.ts", () => ({ useRepoRailContext: () => ({ access: { kinds: { get: (k: string) => ({ kind: k }), has: () => true }, noRoute: false, allOff: false, refresh: () => {} }, launchKinds: ["claude"], connsSettling: false }) }));
vi.mock("../repos/StartModal.tsx", () => ({ StartModal: () => null }));
vi.mock("../mirror/HandoffProposal.tsx", () => ({ markHandoffLaunched: vi.fn() }));
vi.mock("../repos/LaunchModal.tsx", () => ({
  LaunchModal: (p: { repo: string; branch?: string; initialPrompt?: string; newSpace?: boolean }) => (
    <div data-testid="launch-dialog" data-new-space={String(!!p.newSpace)}>
      {p.repo}|{p.branch}|{p.initialPrompt}
    </div>
  ),
}));

const { HandoffOfferRow, repoNameFromRemote } = await import("./HandoffOfferRow.tsx");
const { StartHost } = await import("../repos/StartHost.tsx");
const { useLaunchSeed, useLaunchTarget, useReposStore, useSpaceHandoff } = await import("../repos/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { SHARE_REQUEST_TIMEOUT_MS } = await import("./boundedRequest.ts");

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
  apiGet.mockReset();
  apiGet.mockImplementation(spacesOk);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  useLaunchSeed.getState().clear();
  useSpaceHandoff.getState().clear();
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

  // #5233: a handoff from a session that ran in the sender's space has no repository.
  describe("an offer from a space", () => {
    const fromSpace: HandoffOffer = { ...offer, branch: "", repoRemote: "", headSha: "", sourceSpace: true };

    it("says the sender's files stay behind and offers no working copy to pick", async () => {
      await render([{ name: "private", path: "/r/private" }], false, fromSpace);
      expect(host.textContent).toContain(t("handoff.space_offer_hint", { who: "alice" }));
      expect(select()).toBeNull();
      expect(host.textContent).not.toContain(t("handoff.accept_no_repo_hint"));
      expect(acceptButton().disabled).toBe(false);
    });

    it("can be accepted with no working copy at all", async () => {
      await render([], false, fromSpace);
      expect(host.textContent).not.toContain(t("handoff.accept_no_repo_hint"));
      expect(acceptButton().disabled).toBe(false);
    });

    it("opens the new-space dialog on accept, seeded with the text, and takes nothing yet", async () => {
      await render([{ name: "private", path: "/r/private" }], true, fromSpace);
      await act(async () => acceptButton().click());
      const dialog = document.querySelector("[data-testid=launch-dialog]") as HTMLElement | null;
      expect(dialog?.dataset.newSpace).toBe("true");
      expect(useSpaceHandoff.getState()).toMatchObject({ offerId: "offer-1", prompt: "Continue the work.", title: "Carry on" });
      // The launch seed is not touched: nothing that reads it can take this offer.
      expect(useLaunchSeed.getState().handoffOfferId).toBe("");
      expect(useLaunchTarget.getState().target).toBeNull();
      expect(apiJSON).not.toHaveBeenCalled();
    });

    it("asks the Agent for its spaces before it starts anything", async () => {
      await render([], true, fromSpace);
      await act(async () => acceptButton().click());
      expect(apiGet.mock.calls.some((c) => String(c[0]).includes("api/spaces"))).toBe(true);
    });

    it("starts nothing and says so when the Agent is too old to start in a space", async () => {
      apiGet.mockImplementation(async () => ({ error: { status: 404, code: "http_404", message: "not found" } }));
      await render([], true, fromSpace);
      await act(async () => acceptButton().click());
      expect(document.querySelector("[data-testid=launch-dialog]")).toBeNull();
      expect(useSpaceHandoff.getState().offerId).toBe("");
      expect(document.body.textContent).toContain(t("handoff.space_unsupported"));
    });

    // Support not proven is not support: an Agent without spaces would take the field as home.
    it.each([
      ["a gateway error", async () => ({ error: { status: 502, code: "http_502", message: "bad gateway" } })],
      ["a dropped connection", async () => Promise.reject(new TypeError("Failed to fetch"))],
      ["an answer that is no list of spaces", async () => ({ offers: [] })],
      ["an empty answer", async () => ({})],
    ])("starts nothing on %s, and says the check failed rather than that the Agent is old", async (_n, answer) => {
      apiGet.mockImplementation(answer as never);
      await render([], true, fromSpace);
      await act(async () => acceptButton().click());
      expect(document.querySelector("[data-testid=launch-dialog]")).toBeNull();
      expect(useSpaceHandoff.getState().offerId).toBe("");
      expect(document.body.textContent).toContain(t("handoff.space_check_failed"));
      expect(document.body.textContent).not.toContain(t("handoff.space_unsupported"));
      expect(acceptButton().disabled).toBe(false); // retry is possible
    });

    // An Agent that took the connection and never answers must not freeze the row: the probe has a
    // deadline, and retry and decline work again afterwards.
    it("frees the row when the Agent never answers, and starts nothing", async () => {
      vi.useFakeTimers();
      try {
        apiGet.mockImplementation((async (...a: unknown[]) => {
          if (!String(a[0]).includes("api/spaces")) return { offers: [] };
          const signal = (a[1] as { signal?: AbortSignal } | undefined)?.signal;
          return new Promise((_, reject) => signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))));
        }) as never);
        await render([], true, fromSpace);
        await act(async () => acceptButton().click());
        expect(acceptButton().disabled).toBe(true); // checking
        await act(async () => {
          await vi.advanceTimersByTimeAsync(SHARE_REQUEST_TIMEOUT_MS + 1000);
        });
        expect(acceptButton().disabled).toBe(false);
        expect(useSpaceHandoff.getState().offerId).toBe("");
        expect(document.body.textContent).toContain(t("handoff.space_check_failed"));
      } finally {
        vi.useRealTimers();
      }
    });

    it("cancels the probe when the row goes away", async () => {
      let signal: AbortSignal | undefined;
      apiGet.mockImplementation((async (...a: unknown[]) => {
        if (!String(a[0]).includes("api/spaces")) return { offers: [] };
        signal = (a[1] as { signal?: AbortSignal } | undefined)?.signal;
        return new Promise(() => undefined);
      }) as never);
      await render([], true, fromSpace);
      await act(async () => acceptButton().click());
      expect(signal?.aborted).toBe(false);
      await act(async () => root!.render(<ToastProvider><StartHost /></ToastProvider>));
      expect(signal?.aborted).toBe(true);
    });

    describe("an answer that arrives late", () => {
      function slowSpaces() {
        let release: () => void = () => undefined;
        apiGet.mockImplementation((async (...a: unknown[]) => {
          if (!String(a[0]).includes("api/spaces")) return { offers: [] };
          await new Promise<void>((r) => (release = r));
          return { spaces: [] };
        }) as never);
        return () => release();
      }
      const settle = async (release: () => void) => {
        await act(async () => {
          release();
          await new Promise((r) => setTimeout(r, 0));
        });
      };

      it("writes nothing once the inbox that held the row is closed", async () => {
        await render([], true, fromSpace);
        const release = slowSpaces();
        await act(async () => acceptButton().click());
        await act(async () => root!.render(<ToastProvider><StartHost /></ToastProvider>)); // the row is gone
        await settle(release);
        expect(useSpaceHandoff.getState().offerId).toBe("");
        expect(document.querySelector("[data-testid=launch-dialog]")).toBeNull();
      });

      it("never overwrites the launch of another offer opened meanwhile", async () => {
        await render([{ name: "private", path: "/r/private" }], true, fromSpace);
        const release = slowSpaces();
        await act(async () => acceptButton().click());
        // Another offer from the same list is accepted first: its repository dialog is open.
        await act(async () => {
          useLaunchSeed.getState().set("B's text", "B", "", "", "offer-b");
          useLaunchTarget.getState().open({ name: "private", path: "/r/private", branch: "main" } as never);
        });
        await settle(release);
        expect(useLaunchSeed.getState()).toMatchObject({ handoffOfferId: "offer-b", prompt: "B's text" });
        expect(useSpaceHandoff.getState().offerId).toBe("");
        expect((document.querySelector("[data-testid=launch-dialog]") as HTMLElement).dataset.newSpace).toBe("false");
      });

      it("writes nothing when the tenant changed meanwhile", async () => {
        const { useTenantStore } = await import("../../core/store/tenant.ts");
        useTenantStore.setState({ tenant: "a" });
        await render([], true, fromSpace);
        const release = slowSpaces();
        await act(async () => acceptButton().click());
        await act(async () => useTenantStore.setState({ tenant: "b" }));
        await settle(release);
        expect(useSpaceHandoff.getState().offerId).toBe("");
        useTenantStore.setState({ tenant: "" });
      });

      it("writes nothing when another account signed in to the same tenant meanwhile", async () => {
        const { useTenantStore } = await import("../../core/store/tenant.ts");
        useTenantStore.setState({ tenant: "a", whoami: { email: "a@example.com", resolved_user: "a" } as never });
        await render([], true, fromSpace);
        const release = slowSpaces();
        await act(async () => acceptButton().click());
        await act(async () => useTenantStore.setState({ whoami: { email: "b@example.com", resolved_user: "b" } as never }));
        await settle(release);
        expect(useSpaceHandoff.getState().offerId).toBe("");
        useTenantStore.setState({ tenant: "", whoami: null });
      });

      it("writes nothing when the Start hub was opened meanwhile", async () => {
        await render([], true, fromSpace);
        const release = slowSpaces();
        await act(async () => acceptButton().click());
        await act(async () => useSessionsStore.getState().openStart());
        await settle(release);
        expect(useSpaceHandoff.getState().offerId).toBe("");
      });
    });
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
