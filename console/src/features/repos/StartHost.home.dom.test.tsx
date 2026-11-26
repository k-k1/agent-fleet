// "Launch an agent at home" in the Start hub renders the same start-work dialog as a repo launch
// (LaunchModal in its home form) instead of a form of its own, so image paste / attach, the
// folded tiers and the advanced fields are the same code. This drives the whole stack
// (StartHost -> StartModal -> LaunchModal -> useStartWork) against a mocked client.
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../lib/i18n/index.ts";

vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));
let claudeExpired = false;
let launchKinds = ["claude"];
vi.mock("./useRepoRail.ts", () => ({
  useRepoRailContext: () => ({ access: { kinds: { get: (k: string) => ({ kind: k }), has: () => true }, noRoute: false, allOff: false, refresh: () => {} }, launchKinds, connsSettling: false, claudeExpired }),
}));
vi.mock("../mirror/HandoffProposal.tsx", () => ({ markHandoffLaunched: vi.fn() }));
vi.mock("../chat/api.ts", () => ({ assistantList: vi.fn(async () => ({ assistants: [] })) }));

const apiGet = vi.fn(async (_path: string): Promise<unknown> => []);
const apiPost = vi.fn(async (..._a: unknown[]): Promise<unknown> => ({}));
const pasteImage = vi.fn(async (..._a: unknown[]) => ({ status: 201, path: "/home/dev/.cache/pasted/home-1/shot.png" }));
const repoPromptTemplates = vi.fn(async (_name: string) => ({ groups: [] }));
const repoSkills = vi.fn(async (..._a: unknown[]) => ({ skills: [] }));
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: (path: string) => apiGet(path),
  apiJSON: (...a: unknown[]) => apiPost(...a),
  pasteImage: (...a: unknown[]) => pasteImage(...a),
  repoPromptTemplates: (n: string) => repoPromptTemplates(n),
  repoSkills: (...a: unknown[]) => repoSkills(...a),
  isTransientErr: () => false,
  errText: (e: { message?: string }) => e?.message || "",
  errDetail: (e: { message?: string }) => e?.message || "",
}));

const { StartHost } = await import("./StartHost.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useLaunchSeed, useLaunchTarget } = await import("./store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { writeFoldValue } = await import("../../lib/foldStore.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
};
const click = async (el: Element) => {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
};
// The hub stays mounted under the dialog, so look at the topmost footer only (and exact text:
// "Launch" is also a prefix of the hub's own rows).
const button = (label: string) => {
  const foots = document.querySelectorAll(".ui-modal-foot");
  const foot = foots[foots.length - 1];
  return [...(foot?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find((b) => b.textContent?.trim() === label);
};
// The launch button names what it runs with ("Start with Claude Code · Opus"), so it is found by
// being the primary one rather than by its text.
const launchButton = () => {
  const foots = document.querySelectorAll(".ui-modal-foot");
  return foots[foots.length - 1]?.querySelector<HTMLButtonElement>("button.ui-btn-primary") ?? undefined;
};
const homeRow = () => [...document.querySelectorAll<HTMLButtonElement>(".start-row")].find((b) => b.textContent?.includes(t("start.home_title")))!;
// The hub's own composer stays mounted under the dialog: the dialog's box is the last one.
const promptBox = () => [...document.querySelectorAll<HTMLTextAreaElement>("textarea")].pop()!;
const createCalls = () => apiPost.mock.calls.filter((c) => c[0] === "api/sessions");

async function openHome(): Promise<void> {
  await act(async () => useSessionsStore.getState().openStart());
  await settle();
  await click(homeRow());
}

async function typePrompt(text: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(promptBox(), text);
    promptBox().dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(async () => {
  localStorage.clear();
  writeFoldValue("af-start-section", "other"); // the home row is in Other
  claudeExpired = false;
  launchKinds = ["claude"];
  apiGet.mockReset();
  apiGet.mockImplementation(async () => []);
  apiPost.mockReset();
  apiPost.mockImplementation(async (...a: unknown[]) => (a[0] === "api/sessions" ? { name: "home-1" } : {}));
  pasteImage.mockClear();
  repoPromptTemplates.mockClear();
  repoSkills.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <StartHost />
      </ToastProvider>,
    );
  });
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  useLaunchSeed.getState().clear();
  useLaunchTarget.getState().clear();
});

describe("Start hub — Launch an agent at home", () => {
  it("opens the start-work dialog in its home form: fixed location note, no worktree controls, no repo lookups", async () => {
    await openHome();
    expect(document.body.textContent).toContain(t("launch.title_home"));
    // What -> where -> details (ADR 0142 decision 7): the prompt, then the fixed home note, then the
    // one fold, Details. There is no location fold.
    const folds = [...document.querySelectorAll(".launch-sec-label")];
    expect(folds.map((el) => el.textContent)).toEqual([t("start.details")]);
    const prompt = document.querySelector("textarea")!;
    const place = [...document.querySelectorAll(".ui-field-label")].find((el) => el.textContent === t("launch.field.location"))!;
    expect(prompt.compareDocumentPosition(place) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(place.compareDocumentPosition(folds[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(document.body.textContent).not.toContain(t("launch.new_worktree"));
    expect(document.body.textContent).not.toContain(t("launch.field.subdir"));
    expect(document.body.textContent).toContain("~");
    expect(repoPromptTemplates).not.toHaveBeenCalled();
    expect(repoSkills).not.toHaveBeenCalled();
    expect(apiGet.mock.calls.some((c) => String(c[0]).includes("api/repos//"))).toBe(false);
  });

  it("launches in the home directory with no worktree and nothing untouched sent", async () => {
    await openHome();
    await typePrompt("調べもの");
    await click(launchButton()!);
    const calls = createCalls();
    expect(calls).toHaveLength(1);
    const body = calls[0][2] as Record<string, unknown>;
    expect(body).toMatchObject({ dir: "", kind: "claude", initial_prompt: "調べもの" });
    for (const k of ["worktree", "branch", "new_branch", "subdir", "route", "title", "spend_cap_usd", "skip_permissions"]) {
      expect(body).not.toHaveProperty(k);
    }
    // Launched: the whole stack, hub included, is gone.
    expect(document.querySelector(".ui-modal-body")).toBeNull();
  });

  it("uploads an attached file to the new session (dir '') and weaves its path into the first prompt", async () => {
    await openHome();
    await typePrompt("このスクショを見て");
    const input = [...document.querySelectorAll<HTMLInputElement>("input[type=file]")].pop()!;
    expect(input).toBeTruthy();
    const file = new File(["BYTES"], "shot.png", { type: "image/png" });
    Object.defineProperty(input, "files", { value: [file], configurable: true });
    await act(async () => {
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    await click(launchButton()!);

    expect(createCalls()).toHaveLength(1);
    const body = createCalls()[0][2] as Record<string, unknown>;
    expect(body.dir).toBe("");
    // With attachments the prompt waits for the session, so the create request carries none.
    expect(body).not.toHaveProperty("initial_prompt");
    expect(pasteImage).toHaveBeenCalledTimes(1);
    expect(pasteImage.mock.calls[0][0]).toBe("home-1");
    const input2 = apiPost.mock.calls.find((c) => String(c[0]).endsWith("/input"));
    expect(String((input2?.[2] as { prompt: string }).prompt)).toContain("/home/dev/.cache/pasted/home-1/shot.png");
  });

  const attach = async (name: string) => {
    const input = [...document.querySelectorAll<HTMLInputElement>("input[type=file]")].pop()!;
    Object.defineProperty(input, "files", { value: [new File(["B"], name, { type: "image/png" })], configurable: true });
    await act(async () => {
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
  };

  it("finishes the seed (deletes the memo) when the file and the first prompt both arrive", async () => {
    const onLaunched = vi.fn();
    useLaunchSeed.getState().setOnLaunched(onLaunched);
    await openHome();
    await typePrompt("see this");
    await attach("shot.png");
    await click(launchButton()!);
    expect(onLaunched).toHaveBeenCalledTimes(1);
  });

  it("keeps the memo when the file upload fails after the session was created (#5077)", async () => {
    const onLaunched = vi.fn();
    useLaunchSeed.getState().setOnLaunched(onLaunched);
    pasteImage.mockResolvedValueOnce({ status: 413, path: "" } as never);
    await openHome();
    await typePrompt("see this");
    await attach("shot.png");
    await click(launchButton()!);
    expect(createCalls()).toHaveLength(1);
    expect(onLaunched).not.toHaveBeenCalled();
  });

  it("keeps the memo when the first prompt is refused after the session was created (#5077)", async () => {
    const onLaunched = vi.fn();
    useLaunchSeed.getState().setOnLaunched(onLaunched);
    apiPost.mockImplementation(async (...a: unknown[]) =>
      a[0] === "api/sessions" ? { name: "home-1" } : String(a[0]).endsWith("/input") ? { error: { code: "busy", message: "no" } } : {},
    );
    await openHome();
    await typePrompt("see this");
    await attach("shot.png");
    await click(launchButton()!);
    expect(createCalls()).toHaveLength(1);
    expect(onLaunched).not.toHaveBeenCalled();
  });

  it("Back returns to the hub; Cancel peels the dialog off the stack", async () => {
    await openHome();
    await click(button(t("launch.back_to_hub"))!);
    expect(homeRow()).toBeTruthy();
    expect(document.body.textContent).not.toContain(t("launch.title_home"));
  });

  it("keeps the lcpp model requirement: Launch is disabled until a model is auto-picked", async () => {
    launchKinds = ["lcpp"];
    apiGet.mockImplementation(async (path: string) => (path.includes("agents/lcpp/models") ? { models: [] } : []));
    await openHome();
    expect(launchButton()!.disabled).toBe(true);
  });

  it("shows the expired-Claude notice without gating the launch", async () => {
    claudeExpired = true;
    await openHome();
    const link = document.querySelector(".ui-field-hint .linklike");
    expect(link?.textContent).toBe(t("mirror.error_auth_action"));
    expect(launchButton()!.disabled).toBe(false);
  });
});
