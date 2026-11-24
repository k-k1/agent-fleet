// The Start hub's space section and its one draft, through the whole stack (StartHost ->
// StartModal -> the space form -> useStartWork) against a mocked client: a space launch sends
// `space` with the draft, a memo that seeded the draft is finished by it, and a repository picked
// in the hub takes the draft into its start-work dialog.
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../lib/i18n/index.ts";

vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));
let launchKinds = ["claude"];
vi.mock("./useRepoRail.ts", () => ({
  useRepoRailContext: () => ({ access: { kinds: { get: (k: string) => ({ kind: k }), has: () => true }, noRoute: false, allOff: false, refresh: () => {} }, launchKinds, connsSettling: false, claudeExpired: false }),
}));
vi.mock("../mirror/HandoffProposal.tsx", () => ({ markHandoffLaunched: vi.fn() }));

const apiGet = vi.fn(async (_path: string): Promise<unknown> => []);
const apiPost = vi.fn(async (..._a: unknown[]): Promise<unknown> => ({}));
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: (path: string) => apiGet(path),
  apiJSON: (...a: unknown[]) => apiPost(...a),
  pasteImage: vi.fn(),
  repoPromptTemplates: vi.fn(async () => ({ groups: [] })),
  repoSkills: vi.fn(async () => ({ skills: [] })),
  isTransientErr: () => false,
  errText: (e: { message?: string }) => e?.message || "",
  errDetail: (e: { message?: string }) => e?.message || "",
}));
// The repository's memo stash is not what these tests are about.
vi.mock("../memo/launchStash.ts", () => ({ stashStoredDraft: async () => ({ ok: true }), fetchMemoAttachments: async () => [], stashLaunchDraft: vi.fn() }));

const { StartHost } = await import("./StartHost.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useLaunchSeed, useLaunchTarget, useReposStore } = await import("./store.ts");
const { useUIStateStore } = await import("../../core/store/uiState.ts");
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
const hubBox = () => document.querySelector<HTMLTextAreaElement>("#start-draft-text")!;
const lastBox = () => [...document.querySelectorAll<HTMLTextAreaElement>("textarea")].pop()!;
const createCalls = () => apiPost.mock.calls.filter((c) => c[0] === "api/sessions");
const spaceGo = () => document.querySelector<HTMLButtonElement>(".start-space-actions button")!;

async function typeInto(box: HTMLTextAreaElement, text: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, text);
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function openHub(section: "space" | "repo" | "other" = "space"): Promise<void> {
  writeFoldValue("af-start-section", section);
  await act(async () => useSessionsStore.getState().openStart());
  await settle();
}

beforeEach(async () => {
  localStorage.clear();
  launchKinds = ["claude"];
  useUIStateStore.setState({ state: {} as never });
  useReposStore.setState({ repos: [{ name: "app", path: "/home/dev/repos/app", branch: "main" }] as never, loadState: "ok" });
  apiGet.mockReset();
  apiGet.mockImplementation(async (path: string) => (path === "api/spaces" ? { spaces: [] } : []));
  apiPost.mockReset();
  apiPost.mockImplementation(async (...a: unknown[]) => (a[0] === "api/sessions" ? { name: "space-1" } : {}));
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

describe("Start hub — start in a space", () => {
  it("creates a new space with the draft as the first prompt, and nothing that names a directory", async () => {
    await openHub();
    await typeInto(hubBox(), "調べものをして");
    await click(spaceGo());
    expect(createCalls()).toHaveLength(1);
    const body = createCalls()[0][2] as Record<string, unknown>;
    expect(body).toMatchObject({ dir: "", kind: "claude", space: { name: "" }, initial_prompt: "調べものをして" });
    for (const k of ["worktree", "branch", "new_branch", "subdir"]) expect(body).not.toHaveProperty(k);
    expect(document.querySelector(".ui-modal-body")).toBeNull(); // launched: the hub is gone
  });

  it("launches managed where the harness has a managed driver, whatever the driver default says", async () => {
    launchKinds = ["codex"];
    const launch = { agent: "", model: "", driver: "terminal", permission_mode: "", budget_usd: null };
    const ops = { grafana_url: "", aws_sso_start_url: "", aws_account_id: "", aws_role_name: "" };
    useUIStateStore.setState({ state: { effective: { launch, repos: [], ops } } as never });
    await openHub();
    await click(spaceGo());
    expect((createCalls()[0][2] as Record<string, unknown>).driver).toBe("managed");
  });

  it("opens with the memo being sent, and finishes the memo once a space session has it", async () => {
    const launched = vi.fn();
    useLaunchSeed.getState().set("メモの本文");
    useLaunchSeed.getState().setMemoId("m1");
    useLaunchSeed.getState().setOnLaunched(launched);
    await openHub();
    expect(hubBox().value).toBe("メモの本文");
    await click(spaceGo());
    expect((createCalls()[0][2] as Record<string, unknown>).initial_prompt).toBe("メモの本文");
    expect(launched).toHaveBeenCalledWith("space-1");
  });

  it("starts the draft over at each opening of the hub", async () => {
    await openHub();
    await typeInto(hubBox(), "古い下書き");
    await openHub();
    expect(hubBox().value).toBe("");
  });
});

describe("Start hub — a repository picked with the draft", () => {
  const repoRow = () => [...document.querySelectorAll<HTMLButtonElement>(".start-row")].find((b) => b.textContent?.includes("app"))!;

  it("takes the draft into the start-work dialog, and back to the hub with the edits", async () => {
    localStorage.setItem("af.launch-prompt.app", "保存済みの下書き");
    await openHub("repo");
    await typeInto(hubBox(), "ハブで書いた文");
    await click(repoRow());
    expect(lastBox().value).toBe("ハブで書いた文");
    await typeInto(lastBox(), "ハブで書いた文、追記");
    const foots = document.querySelectorAll(".ui-modal-foot");
    const back = [...foots[foots.length - 1].querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("launch.back_to_hub")))!;
    await click(back);
    expect(hubBox().value).toBe("ハブで書いた文、追記");
    // The repository's own stored draft was never overwritten.
    expect(localStorage.getItem("af.launch-prompt.app")).toBe("保存済みの下書き");
  });

  it("shows the repository's own stored draft when the hub's is empty", async () => {
    localStorage.setItem("af.launch-prompt.app", "保存済みの下書き");
    await openHub("repo");
    await click(repoRow());
    expect(lastBox().value).toBe("保存済みの下書き");
  });
});
