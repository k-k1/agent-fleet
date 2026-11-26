// A handoff proposal or a plan review launched from a session that runs in a space starts in that
// space — the Start hub's space form on that folder — and never opens the repository launch dialog,
// whose worktree and branch fields do not exist in a space. When the space is gone the launch says
// so and starts nothing.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { answerConfirm } from "../../test/confirmDialog.ts";
import type { Session } from "../../types/session.ts";

let spaceFolders = ["research-aaa"];
let tenant = "";
// What GET /api/spaces does: the list, a refusal, or a hang the test releases itself.
let spacesAnswer: () => Promise<unknown> = async () => ({ spaces: spaceFolders.map((folder) => ({ folder, name: "", dir: "/home/dev/spaces/" + folder })) });
let ownedOffers: unknown[] = [];
const apiGet = vi.fn(async (path: string): Promise<unknown> =>
  path === "api/spaces" ? spacesAnswer() : path === "api/session-handoff-offers" ? { offers: ownedOffers } : {},
);
const apiJSON = vi.fn(async (..._a: unknown[]): Promise<unknown> => ({}));
const sessionCarriedAnswer = vi.fn(async (..._a: unknown[]) => ({ ok: true }));
const sessionPlanFile = vi.fn(async (_s: string) => ({ ok: true, path: "/home/dev/.claude/plans/p.md", source: "claude" }));
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return {
    ...real,
    getTenant: () => tenant,
    getUser: () => "",
    isTransientErr: () => false,
    api: (path: string) => apiGet(path),
    apiJSON: (...a: unknown[]) => apiJSON(...a),
    sessionCarriedAnswer: (...a: unknown[]) => sessionCarriedAnswer(...a),
    sessionPlanFile: (s: string) => sessionPlanFile(s),
  };
});

const { HandoffProposal } = await import("./HandoffProposal.tsx");
const { usePlanActions } = await import("./parts/usePlanActions.ts");
const { useLaunchSeed, useLaunchTarget, useReposStore } = await import("../repos/store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useSpacesStore } = await import("../spaces/store.ts");
const { useHandoffStore } = await import("../sharing/handoffStore.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

const inSpace = { name: "src", kind: "claude", space: "research-aaa", dir: "/home/dev/spaces/research-aaa" } as Session;
const proposal = { id: "p-1", prompt: "Continue the survey.", title: "Survey, part 2", created_at: 1 };

let root: Root | null = null;
let host: HTMLDivElement;
const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
};

beforeEach(() => {
  spaceFolders = ["research-aaa"];
  ownedOffers = [];
  tenant = "";
  spacesAnswer = async () => ({ spaces: spaceFolders.map((folder) => ({ folder, name: "", dir: "/home/dev/spaces/" + folder })) });
  localStorage.clear();
  apiGet.mockClear();
  apiJSON.mockClear();
  sessionCarriedAnswer.mockClear();
  useHandoffStore.setState({ owned: [], received: [] });
  sessionPlanFile.mockClear();
  useSpacesStore.getState().clear();
  useReposStore.setState({ repos: [{ name: "research-aaa", path: "/home/dev/repos/research-aaa", branch: "main" }] as never, loadState: "ok" });
  useSessionsStore.setState({ startTick: 0 });
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
  useHandoffStore.setState({ owned: [], received: [] });
});

const opened = () => useSessionsStore.getState().startTick;

describe("handoff proposal launched from a space session", () => {
  const mountCard = () =>
    act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <HandoffProposal session="src" sessionMeta={inSpace} proposal={proposal} onChange={() => undefined} />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
  const press = async () => {
    const go = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(t("mirror.handoff_launch")))!;
    await act(async () => go.click());
    await settle();
  };
  const renderCard = async () => {
    await mountCard();
    await press();
  };

  it("opens the Start hub on that space, with the handoff lineage, and never the repository dialog", async () => {
    await renderCard();
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(opened()).toBe(1);
    const seed = useLaunchSeed.getState();
    expect(seed).toMatchObject({ space: "research-aaa", prompt: proposal.prompt, title: proposal.title, handoffSession: "src", handoffId: "p-1" });
  });

  it("says the space is gone and starts nothing — not in home, not in a repository", async () => {
    spaceFolders = ["another-bbb"];
    await renderCard();
    expect(document.body.textContent).toContain(t("mirror.launch_space_gone"));
    expect(opened()).toBe(0);
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(useLaunchSeed.getState()).toMatchObject({ space: "", prompt: "", handoffSession: "" });
  });

  it("goes on when the list cannot be read — it never decides 'gone' from that", async () => {
    for (const answer of [
      async () => {
        throw new Error("network");
      },
      async () => ({ error: { code: "http_503", transient: true } }),
    ]) {
      spacesAnswer = answer;
      useSessionsStore.setState({ startTick: 0 });
      useLaunchSeed.getState().clear();
      await renderCard();
      expect(opened()).toBe(1);
      expect(useLaunchSeed.getState().space).toBe("research-aaa");
      expect(document.body.textContent).not.toContain(t("mirror.launch_space_gone"));
    }
  });

  it("judges by the list it just read, not by one cached before: a space made meanwhile is there", async () => {
    useSpacesStore.setState({ spaces: [], owner: "\n" }); // an older read that did not have it
    await renderCard();
    expect(opened()).toBe(1);
  });

  it("decides nothing when the workspace changed while the list was read", async () => {
    let release!: () => void;
    spacesAnswer = () => new Promise((r) => (release = () => r({ spaces: [] })));
    await mountCard();
    await press();
    tenant = "other"; // the answer is the old workspace's: it says nothing about this one
    await act(async () => release());
    await settle();
    expect(opened()).toBe(0);
    expect(document.body.textContent).not.toContain(t("mirror.launch_space_gone"));
  });

  describe("with a shared offer still pending", () => {
    const offer = { id: "offer-1", sessionId: "cat-1", sessionName: "src", recipientUserKey: "bob", title: "Carry on", status: "pending" } as never;
    const withOffer = () => {
      ownedOffers = [offer];
      useHandoffStore.setState({ owned: [offer] });
    };

    it("does not withdraw it when the space is gone", async () => {
      spaceFolders = [];
      withOffer();
      await renderCard();
      expect(document.body.textContent).toContain(t("mirror.launch_space_gone"));
      expect(apiJSON).not.toHaveBeenCalled();
      expect(document.querySelector(".ui-confirm-actions")).toBeNull(); // no question was asked either
      expect(opened()).toBe(0);
    });

    it("does not withdraw it, or open a hub that refuses everything, for an Agent without spaces", async () => {
      spacesAnswer = async () => ({ error: { code: "http_405" } });
      withOffer();
      await renderCard();
      expect(document.body.textContent).toContain(t("start.space_unsupported"));
      expect(apiJSON).not.toHaveBeenCalled();
      expect(document.querySelector(".ui-confirm-actions")).toBeNull();
      expect(opened()).toBe(0);
    });

    it("asks the list again after the question and does not withdraw when the space was removed meanwhile", async () => {
      withOffer();
      await renderCard();
      expect(document.querySelector(".ui-confirm-actions")).not.toBeNull(); // the space was there when asked
      spaceFolders = []; // removed in another tab while the question stood open
      await answerConfirm(true);
      await settle();
      expect(apiJSON).not.toHaveBeenCalled();
      expect(document.body.textContent).toContain(t("mirror.launch_space_gone"));
      expect(opened()).toBe(0);
    });

    it("withdraws it after the question when the space is there, then opens the hub", async () => {
      withOffer();
      await renderCard();
      await answerConfirm(true);
      await settle();
      expect(apiJSON).toHaveBeenCalledWith("api/session-handoff-offers/offer-1", "DELETE");
      expect(opened()).toBe(1);
    });
  });

  it("holds the card while the list is read: sharing, discarding and a second launch wait for the answer", async () => {
    let release!: () => void;
    spacesAnswer = () => new Promise((r) => (release = () => r({ spaces: [{ folder: "research-aaa", name: "", dir: "x" }] })));
    await mountCard();
    await press();
    const buttons = () => [...host.querySelectorAll<HTMLButtonElement>(".mirror-handoff-actions button")];
    expect(buttons().length).toBeGreaterThan(0);
    expect(buttons().every((b) => b.disabled)).toBe(true); // nothing on the card can change what this press decides on
    await act(async () => release());
    await settle();
    expect(opened()).toBe(1);
    expect(buttons().every((b) => !b.disabled)).toBe(true);
  });

  it("keeps deciding on the offer seen at the press: one accepted while the list was read is refused by the conditional withdraw, and no hub opens", async () => {
    const pending = { id: "offer-3", sessionId: "cat-1", sessionName: "src", recipientUserKey: "bob", title: "Carry on", status: "pending" } as never;
    ownedOffers = [pending];
    useHandoffStore.setState({ owned: [pending] });
    let release!: () => void;
    spacesAnswer = () => new Promise((r) => (release = () => r({ spaces: [{ folder: "research-aaa", name: "", dir: "x" }] })));
    apiJSON.mockResolvedValueOnce({ error: { code: "handoff_already_decided", offerStatus: "accepted", status: 409 } });
    await mountCard();
    await press();
    const accepted = { ...(pending as object), status: "accepted" } as never;
    ownedOffers = [accepted];
    await act(async () => useHandoffStore.setState({ owned: [accepted] })); // the recipient took it meanwhile
    await act(async () => release());
    await settle();
    spacesAnswer = async () => ({ spaces: [{ folder: "research-aaa", name: "", dir: "x" }] }); // the read after the question
    await answerConfirm(true); // the question the press owed, because the offer was pending then
    await settle();
    expect(apiJSON).toHaveBeenCalledWith("api/session-handoff-offers/offer-3", "DELETE");
    expect(document.body.textContent).toContain(t("handoff.withdraw_lost"));
    expect(opened()).toBe(0);
  });

  it("starts nothing from a card that was replaced while the list was read", async () => {
    let release!: () => void;
    spacesAnswer = () => new Promise((r) => (release = () => r({ spaces: [{ folder: "research-aaa", name: "", dir: "x" }] })));
    const render = (id: string) =>
      act(async () => {
        root!.render(
          <ToastProvider>
            <ConfirmProvider>
              <HandoffProposal session="src" sessionMeta={inSpace} proposal={{ ...proposal, id }} onChange={() => undefined} />
            </ConfirmProvider>
          </ToastProvider>,
        );
      });
    await render("p-1");
    const go = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(t("mirror.handoff_launch")))!;
    await act(async () => go.click());
    await render("p-2");
    await act(async () => release());
    await settle();
    expect(opened()).toBe(0);
    expect(useLaunchSeed.getState().handoffSession).toBe("");
  });

  it("still opens the repository dialog for a session in a repository", async () => {
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <HandoffProposal session="src" sessionMeta={{ name: "src", kind: "claude", repo: "research-aaa", dir: "/home/dev/repos/research-aaa" } as Session} proposal={proposal} onChange={() => undefined} />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    const go = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(t("mirror.handoff_launch")))!;
    await act(async () => go.click());
    await settle();
    expect(useLaunchTarget.getState().target?.name).toBe("research-aaa");
    expect(opened()).toBe(0);
    expect(useLaunchSeed.getState().space).toBe("");
  });
});

describe("plan review launched from a space session", () => {
  type Actions = ReturnType<typeof usePlanActions>;
  let plan: Actions;
  const markRejected = vi.fn();
  const sendInterrupt = vi.fn(async () => {});
  const toast = vi.fn();
  const setCarried = vi.fn();
  function Probe({ meta, pending = "# Plan\n\nDo it.", carried = null }: { meta: Session; pending?: string; carried?: { kind: "plan"; plan: string } | null }) {
    plan = usePlanActions({
      session: "src",
      sessionMeta: meta,
      running: true,
      readOnly: false,
      toast: toast as never,
      st: { pendingPlan: pending, carried, alive: true, markRejected, wasWorkingRef: { current: false }, setCarried } as never,
      actions: { wsDown: () => false, sendInterrupt } as never,
      openTargetInNew: vi.fn() as never,
      setPaneTarget: vi.fn() as never,
      setActivePane: vi.fn() as never,
    });
    return null;
  }
  const mount = async () => {
    markRejected.mockClear();
    sendInterrupt.mockClear();
    toast.mockClear();
    setCarried.mockClear();
    await act(async () => root!.render(<Probe meta={inSpace} />));
    await settle();
  };

  it("review elsewhere: rejects the plan, then opens the hub on the space with the review prompt", async () => {
    await mount();
    await act(async () => plan.reviewPlanElsewhere("# Plan\n\nDo it."));
    await settle();
    expect(markRejected).toHaveBeenCalled();
    expect(sendInterrupt).toHaveBeenCalled();
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(opened()).toBe(1);
    const seed = useLaunchSeed.getState();
    expect(seed.space).toBe("research-aaa");
    expect(seed.prompt).toContain("/home/dev/spaces/research-aaa"); // the reviewer is pointed at the space's directory
    expect(seed.handoffSession).toBe("");
  });

  it("review elsewhere with the space gone: the plan is left exactly as it was", async () => {
    spaceFolders = [];
    await mount();
    await act(async () => plan.reviewPlanElsewhere("# Plan\n\nDo it."));
    await settle();
    expect(toast).toHaveBeenCalledWith(t("mirror.launch_space_gone"));
    expect(markRejected).not.toHaveBeenCalled();
    expect(sendInterrupt).not.toHaveBeenCalled();
    expect(opened()).toBe(0);
    expect(useLaunchTarget.getState().target).toBeNull();
  });

  it("a stopped session's carried plan: same hub, and the space gone says so", async () => {
    await mount();
    await act(async () => root!.render(<Probe meta={inSpace} pending="" carried={{ kind: "plan", plan: "# Plan\n\nDo it." }} />));
    await act(async () => plan.reviewCarriedPlan("# Plan\n\nDo it."));
    await settle();
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(useLaunchSeed.getState().space).toBe("research-aaa");
    expect(useLaunchSeed.getState().onLaunched).not.toBeNull();
    expect(opened()).toBe(1);

    useLaunchSeed.getState().clear();
    spaceFolders = [];
    await act(async () => plan.reviewCarriedPlan("# Plan\n\nDo it."));
    await settle();
    expect(toast).toHaveBeenCalledWith(t("mirror.launch_space_gone"));
    expect(opened()).toBe(1);
    expect(useLaunchSeed.getState().space).toBe("");
  });

  it("review elsewhere: a plan approved while the list is read is not rejected or interrupted", async () => {
    let release!: () => void;
    spacesAnswer = () => new Promise((r) => (release = () => r({ spaces: [{ folder: "research-aaa", name: "", dir: "x" }] })));
    await mount();
    let run!: Promise<void>;
    await act(async () => {
      run = plan.reviewPlanElsewhere("# Plan\n\nDo it.");
    });
    await settle();
    await act(async () => root!.render(<Probe meta={inSpace} pending="" />)); // approved: no plan is pending any more
    await act(async () => release());
    await run;
    await settle();
    expect(markRejected).not.toHaveBeenCalled();
    expect(sendInterrupt).not.toHaveBeenCalled();
    expect(opened()).toBe(0);
  });

  it("review elsewhere asks about the space before it fetches the plan file, and touches nothing for an Agent without spaces", async () => {
    let release!: () => void;
    spacesAnswer = () => new Promise((r) => (release = () => r({ error: { code: "http_404" } })));
    await mount();
    let run!: Promise<void>;
    await act(async () => {
      run = plan.reviewPlanElsewhere("# Plan\n\nDo it.");
    });
    await settle();
    expect(sessionPlanFile).not.toHaveBeenCalled(); // the wait for the list is not added to the plan file's window
    await act(async () => release());
    await run;
    await settle();
    expect(toast).toHaveBeenCalledWith(t("start.space_unsupported"));
    expect(sessionPlanFile).not.toHaveBeenCalled();
    expect(markRejected).not.toHaveBeenCalled();
    expect(sendInterrupt).not.toHaveBeenCalled();
    expect(opened()).toBe(0);
  });

  it("review elsewhere goes on when the list could not be read", async () => {
    spacesAnswer = async () => {
      throw new Error("network");
    };
    await mount();
    await act(async () => plan.reviewPlanElsewhere("# Plan\n\nDo it."));
    await settle();
    expect(markRejected).toHaveBeenCalled();
    expect(opened()).toBe(1);
  });

  describe("a carried plan reviewed in the space", () => {
    const carried = { kind: "plan" as const, plan: "# Plan\n\nDo it." };
    const start = async () => {
      markRejected.mockClear();
      await act(async () => root!.render(<Probe meta={inSpace} pending="" carried={carried} />));
      await settle();
      await act(async () => plan.reviewCarriedPlan(carried.plan));
      await settle();
    };

    it("is discarded and the reviewer remembered only once the review session exists", async () => {
      await start();
      expect(sessionCarriedAnswer).not.toHaveBeenCalled(); // opening the hub changes nothing
      expect(localStorage.getItem("af.plan-reviewers") || "").not.toContain("rev-1");
      await act(async () => useLaunchSeed.getState().onLaunched?.("rev-1"));
      await settle();
      expect(sessionCarriedAnswer).toHaveBeenCalledWith("src", { decision: "discard" });
      expect(setCarried).toHaveBeenCalledWith(null);
      expect(localStorage.getItem("af.plan-reviewers") || "").toContain("rev-1");
    });

    it("is left alone when the hub is closed without launching", async () => {
      await start();
      useLaunchSeed.getState().clear(); // the hub's close
      expect(useLaunchSeed.getState().onLaunched).toBeNull();
      expect(sessionCarriedAnswer).not.toHaveBeenCalled();
    });

    it("is not touched when the card was discarded while the list was read", async () => {
      let release!: () => void;
      spacesAnswer = () => new Promise((r) => (release = () => r({ spaces: [{ folder: "research-aaa", name: "", dir: "x" }] })));
      await act(async () => root!.render(<Probe meta={inSpace} pending="" carried={carried} />));
      let run!: Promise<void>;
      await act(async () => {
        run = plan.reviewCarriedPlan(carried.plan);
      });
      await settle();
      await act(async () => root!.render(<Probe meta={inSpace} pending="" carried={null} />));
      await act(async () => release());
      await run;
      await settle();
      expect(opened()).toBe(0);
      expect(useLaunchSeed.getState().onLaunched).toBeNull();
    });
  });
});
