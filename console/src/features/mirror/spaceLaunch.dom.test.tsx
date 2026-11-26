// A handoff proposal or a plan review launched from a session that runs in a space starts in that
// space — the Start hub's space form on that folder — and never opens the repository launch dialog,
// whose worktree and branch fields do not exist in a space. When the space is gone the launch says
// so and starts nothing.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import type { Session } from "../../types/session.ts";

let spaceFolders = ["research-aaa"];
const apiGet = vi.fn(async (path: string): Promise<unknown> =>
  path === "api/spaces" ? { spaces: spaceFolders.map((folder) => ({ folder, name: "", dir: "/home/dev/spaces/" + folder })) } : {},
);
const sessionPlanFile = vi.fn(async (_s: string) => ({ ok: true, path: "/home/dev/.claude/plans/p.md", source: "claude" }));
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return {
    ...real,
    getTenant: () => "",
    getUser: () => "",
    isTransientErr: () => false,
    api: (path: string) => apiGet(path),
    apiJSON: vi.fn(async () => ({})),
    sessionPlanFile: (s: string) => sessionPlanFile(s),
  };
});

const { HandoffProposal } = await import("./HandoffProposal.tsx");
const { usePlanActions } = await import("./parts/usePlanActions.ts");
const { useLaunchSeed, useLaunchTarget, useReposStore } = await import("../repos/store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useSpacesStore } = await import("../spaces/store.ts");
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
  apiGet.mockClear();
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
});

const opened = () => useSessionsStore.getState().startTick;

describe("handoff proposal launched from a space session", () => {
  const renderCard = async () => {
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <HandoffProposal session="src" sessionMeta={inSpace} proposal={proposal} onChange={() => undefined} />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    const go = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(t("mirror.handoff_launch")))!;
    await act(async () => go.click());
    await settle();
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
  function Probe({ meta }: { meta: Session }) {
    plan = usePlanActions({
      session: "src",
      sessionMeta: meta,
      running: true,
      readOnly: false,
      toast: toast as never,
      st: { pendingPlan: "# Plan\n\nDo it.", carried: null, alive: true, markRejected, wasWorkingRef: { current: false }, setCarried: vi.fn() } as never,
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
});
