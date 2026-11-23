// Render test for Home (#2028) and the first-run state it reads from the CP (ADR 0114).
//
//   1. First run: the setup card shows and Home leaves out its own start buttons (the card's goal
//      tiles are the same two actions). "Later" is stored in the CP, not in this browser.
//   2. A "later" stored by another device wins over this browser's empty cache.
//   3. The pre-ADR-0114 localStorage keys are uploaded once and then removed.
//   4. A session waiting for a person is listed under "waiting for you".
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Session = import("../../types/session.ts").Session;

// What the CP holds for this member, and every PATCH body it received.
let cpState: Record<string, unknown> = {};
let patches: unknown[] = [];
// The agent connections GET /api/connections reports.
let conns: Record<string, unknown> = { claude: { connected: true } };
const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });
vi.stubGlobal(
  "fetch",
  vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("api/me/ui-state")) {
      if ((init?.method || "GET").toUpperCase() === "PATCH") {
        const body = JSON.parse(String(init?.body));
        patches.push(body);
        cpState = { ...cpState, ...body };
      }
      return json(cpState);
    }
    if (url.includes("api/connections")) return json(conns);
    return json({});
  }),
);

const openAssistantDraft = vi.fn();
vi.mock("../chat/open.ts", () => ({ openAssistantDraft: (id: string) => openAssistantDraft(id) }));

const { HomeView } = await import("./HomeView.tsx");
const { useUIStateStore } = await import("../../core/store/uiState.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { PaneHoverProvider } = await import("../../lib/panehover.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
};

const mount = async (sessions: Session[] = []) => {
  useSessionsStore.setState({ sessions });
  await act(async () => {
    await useUIStateStore.getState().load();
  });
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <PaneHoverProvider>
            <HomeView embedded />
          </PaneHoverProvider>
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
};

const setupCard = () => host.querySelector(".home-setup");
const startButton = () => [...host.querySelectorAll(".home-actions button")].find((b) => b.textContent?.includes(t("onb.get_started")));

beforeEach(() => {
  localStorage.clear();
  cpState = {};
  patches = [];
  conns = { claude: { connected: true } };
  openAssistantDraft.mockClear();
  useWorkspaceStore.setState({ state: "running" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("Home", () => {
  it("shows the setup card on first run without a second pair of start buttons, and stores 'later' in the CP", async () => {
    await mount();
    expect(setupCard()).not.toBeNull();
    expect(startButton()).toBeUndefined();

    const later = [...setupCard()!.querySelectorAll("button")].find((b) => b.textContent === t("onb.later"))!;
    await act(async () => later.click());
    await settle();

    expect(patches).toEqual([{ onboarding: { dismissed: true, track: "" } }]);
    expect(setupCard()).toBeNull();
    expect(startButton()).toBeDefined();
  });

  it("honours a 'later' stored by another device, and its chat button opens the assistant", async () => {
    cpState = { onboarding: { dismissed: true, track: "" } };
    await mount();
    expect(setupCard()).toBeNull();
    expect(startButton()).toBeDefined();
    const chat = [...host.querySelectorAll<HTMLButtonElement>(".home-actions button")].find((b) => b.textContent?.includes(t("onb.start_chat")))!;
    await act(async () => chat.click());
    expect(openAssistantDraft).toHaveBeenCalledWith("af");
  });

  it("names the tenant's default agent in the connect step", async () => {
    cpState = { defaults: { agent: "codex" } };
    await mount();
    expect(setupCard()?.textContent).toContain(t("onb.connect_agent_hint_default", { agent: "Codex" }));
  });

  it("completes the connect step when only the named default (Cursor) is connected", async () => {
    cpState = { defaults: { agent: "cursor" } };
    conns = { cursor: { connected: true } };
    await mount();
    const steps = [...setupCard()!.querySelectorAll(".onboard-step")];
    expect(steps[1].classList.contains("onboard-step--done")).toBe(true);
    const chat = [...setupCard()!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("onb.start_chat")))!;
    expect(chat.disabled).toBe(false);
  });

  it("does not name a default agent this Console does not know", async () => {
    cpState = { defaults: { agent: "future-kind" } };
    await mount();
    expect(setupCard()?.textContent).toContain(t("onb.connect_agent_hint"));
    expect(setupCard()?.textContent).not.toContain("Claude Code");
  });

  it("uploads the old localStorage keys once and removes them", async () => {
    localStorage.setItem("af.onboarding.dismissed", "1");
    localStorage.setItem("af.onboarding.track", "dev");
    await mount();
    expect(patches).toEqual([{ onboarding: { dismissed: true, track: "dev" } }]);
    expect(localStorage.getItem("af.onboarding.dismissed")).toBeNull();
    expect(localStorage.getItem("af.onboarding.track")).toBeNull();
    expect(setupCard()).toBeNull();
  });

  it("lists a session waiting for a person under 'waiting for you'", async () => {
    cpState = { onboarding: { dismissed: true, track: "" } };
    await mount([
      { name: "s1", kind: "claude", alive: true, state: "question", title: "返金 API の契約整理" },
      { name: "s2", kind: "claude", alive: true, state: "", title: "在庫同期バッチの調査" },
    ]);
    const waiting = host.querySelector(".home-waiting");
    expect(waiting?.textContent).toContain(t("home.waiting"));
    expect(waiting?.textContent).toContain("返金 API の契約整理");
    expect(waiting?.textContent).not.toContain("在庫同期バッチの調査");
    expect(host.textContent).toContain(t("home.continue"));
  });
});
