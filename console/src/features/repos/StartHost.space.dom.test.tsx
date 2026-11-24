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
const pasteImage = vi.fn(async (..._a: unknown[]) => ({ status: 201, path: "/home/dev/.cache/pasted/space-1/shot.png" }));
const stashLaunchDraft = vi.fn(async (..._a: unknown[]) => ({ ok: true }));
const fetchMemoAttachments = vi.fn(async (_a: unknown[]): Promise<File[]> => []);
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: (path: string) => apiGet(path),
  apiJSON: (...a: unknown[]) => apiPost(...a),
  pasteImage: (...a: unknown[]) => pasteImage(...a),
  repoPromptTemplates: vi.fn(async () => ({ groups: [] })),
  repoSkills: vi.fn(async () => ({ skills: [] })),
  isTransientErr: () => false,
  errText: (e: { message?: string }) => e?.message || "",
  errDetail: (e: { message?: string }) => e?.message || "",
}));
// The repository's memo stash is not what these tests are about.
vi.mock("../memo/launchStash.ts", () => ({
  stashStoredDraft: async () => ({ ok: true }),
  fetchMemoAttachments: (a: unknown[]) => fetchMemoAttachments(a),
  stashLaunchDraft: (...a: unknown[]) => stashLaunchDraft(...a),
}));

const initRepo = vi.fn();
vi.mock("./clone.ts", () => ({ cloneRepo: vi.fn(), svnCheckout: vi.fn(), initRepo: (...a: unknown[]) => initRepo(...a) }));

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
const head = (label: string) => [...document.querySelectorAll<HTMLButtonElement>(".start-sec-head")].find((b) => b.textContent?.includes(label))!;
const chips = () => document.querySelectorAll(".ma-chip").length;
async function attachToHub(file: File): Promise<void> {
  const input = document.querySelector<HTMLInputElement>(".start-draft input[type=file]")!;
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  await act(async () => {
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await settle();
}
async function openHub(section: "space" | "repo" | "other" = "space"): Promise<void> {
  writeFoldValue("af-start-section", section);
  await act(async () => useSessionsStore.getState().openStart());
  await settle();
}

beforeEach(async () => {
  localStorage.clear();
  pasteImage.mockClear();
  stashLaunchDraft.mockClear();
  fetchMemoAttachments.mockReset();
  fetchMemoAttachments.mockImplementation(async () => []);
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

describe("Start hub — the draft's files and the memo it came from", () => {
  const shot = () => new File(["BYTES"], "shot.png", { type: "image/png" });

  it("keeps attached files across sections and uploads them to the new space's session", async () => {
    await openHub();
    await typeInto(hubBox(), "このスクショを見て");
    const file = shot();
    await attachToHub(file);
    await click(head(t("start.sec_other")));
    expect(chips()).toBe(1);
    await click(head(t("start.sec_space")));
    expect(chips()).toBe(1);
    await click(spaceGo());
    expect(createCalls()).toHaveLength(1);
    expect(pasteImage).toHaveBeenCalledTimes(1);
    expect(pasteImage.mock.calls[0]).toEqual(["space-1", file]);
  });

  it("does not launch an agent that cannot take the attached files, and keeps them", async () => {
    launchKinds = ["cursor"];
    await openHub();
    await attachToHub(shot());
    expect(spaceGo().disabled).toBe(true);
    expect(document.body.textContent).toContain(t("launch.attach_not_taken"));
    await act(async () => {
      hubBox().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
    });
    await settle();
    expect(createCalls()).toHaveLength(0);
    expect(chips()).toBe(1);
  });

  it("takes the files into the repository dialog and back", async () => {
    await openHub("repo");
    await attachToHub(shot());
    await click([...document.querySelectorAll<HTMLButtonElement>(".start-row")].find((b) => b.textContent?.includes("app"))!);
    const modals = document.querySelectorAll(".ui-modal");
    expect(modals[modals.length - 1].querySelectorAll(".ma-chip")).toHaveLength(1);
    const foots = document.querySelectorAll(".ui-modal-foot");
    await click([...foots[foots.length - 1].querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("launch.back_to_hub")))!);
    expect(chips()).toBe(1);
  });

  it("saves every file to the memo, even from an agent that cannot take them", async () => {
    launchKinds = ["cursor"];
    await openHub("repo");
    await typeInto(hubBox(), "あとでやる");
    const file = shot();
    await attachToHub(file);
    await click([...document.querySelectorAll<HTMLButtonElement>(".start-row")].find((b) => b.textContent?.includes("app"))!);
    await click(document.querySelector<HTMLButtonElement>(".launch-stash-btn")!);
    expect(stashLaunchDraft).toHaveBeenCalledWith("app", "あとでやる", [file]);
  });

  it("refuses the send key and keeps the files in a home dialog whose agent cannot take them", async () => {
    launchKinds = ["cursor"];
    await openHub("other");
    await attachToHub(shot());
    await click([...document.querySelectorAll<HTMLButtonElement>(".start-row")].find((b) => b.textContent?.includes(t("start.home_title")))!);
    expect(document.querySelectorAll(".ui-modal")).toHaveLength(2);
    await act(async () => {
      lastBox().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
    });
    await settle();
    expect(createCalls()).toHaveLength(0);
    const modals = document.querySelectorAll(".ui-modal");
    expect(modals[modals.length - 1].querySelectorAll(".ma-chip")).toHaveLength(1);
  });

  it("keeps the memo when the launch did not take the hub's draft (Other → Start in a new folder)", async () => {
    const launched = vi.fn();
    initRepo.mockImplementation(async () => {
      useReposStore.setState({ repos: [{ name: "app", path: "/home/dev/repos/app", branch: "main" }, { name: "fresh", path: "/home/dev/repos/fresh", branch: "main", unborn: true }] as never });
      return { ok: true, name: "fresh" };
    });
    useLaunchSeed.getState().set("メモの本文");
    useLaunchSeed.getState().setMemoId("m1");
    useLaunchSeed.getState().setOnLaunched(launched);
    await openHub("other");
    await click([...document.querySelectorAll<HTMLButtonElement>(".start-row")].find((b) => b.textContent?.includes(t("start.newdir_title")))!);
    const name = document.querySelector<HTMLInputElement>(".ui-modal-body input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(name, "fresh");
      name.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const foot = () => document.querySelectorAll(".ui-modal-foot")[document.querySelectorAll(".ui-modal-foot").length - 1];
    await click([...foot().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("start.create_and_continue")))!);
    expect(lastBox().value).not.toBe("メモの本文"); // the dialog shows its own draft, not the memo
    await click([...foot().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t("launch.launch"))!);
    expect(createCalls()).toHaveLength(1);
    expect(launched).not.toHaveBeenCalled();
  });

  it("keeps a files-only memo whose file could not be fetched when a repository is launched", async () => {
    const launched = vi.fn();
    fetchMemoAttachments.mockImplementation(async () => []);
    useLaunchSeed.getState().set("");
    useLaunchSeed.getState().setAttachments([{ path: "/m/a.png", name: "a.png" }]);
    useLaunchSeed.getState().setMemoId("m1");
    useLaunchSeed.getState().setOnLaunched(launched);
    await openHub("repo");
    await click([...document.querySelectorAll<HTMLButtonElement>(".start-row")].find((b) => b.textContent?.includes("app"))!);
    const foots = document.querySelectorAll(".ui-modal-foot");
    await click([...foots[foots.length - 1].querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t("launch.start_worktree"))!);
    expect(createCalls()).toHaveLength(1);
    expect(launched).not.toHaveBeenCalled();
  });

  it("waits for a memo's files, and keeps the memo when one of them could not be fetched", async () => {
    let release!: (f: File[]) => void;
    fetchMemoAttachments.mockImplementation(() => new Promise<File[]>((r) => (release = r)));
    const launched = vi.fn();
    useLaunchSeed.getState().set("メモの本文");
    useLaunchSeed.getState().setAttachments([
      { path: "/m/a.png", name: "a.png" },
      { path: "/m/b.png", name: "b.png" },
    ]);
    useLaunchSeed.getState().setMemoId("m1");
    useLaunchSeed.getState().setOnLaunched(launched);
    await openHub();
    expect(spaceGo().disabled).toBe(true);
    await act(async () => release([shot()]));
    await settle();
    expect(chips()).toBe(1);
    expect(document.body.textContent).toContain(t("launch.memo_attach_missing", { count: 1 }));
    await click(spaceGo());
    expect(createCalls()).toHaveLength(1);
    expect(launched).not.toHaveBeenCalled();
  });

  it("comes back with the draft when the create got no answer, and says the session may exist", async () => {
    apiPost.mockImplementation(async (...a: unknown[]) => {
      if (a[0] === "api/sessions") throw new TypeError("network");
      return {};
    });
    await openHub();
    await typeInto(hubBox(), "調べものをして");
    await click(spaceGo());
    expect(spaceGo().disabled).toBe(false);
    expect(hubBox().value).toBe("調べものをして");
    expect(document.body.textContent).toContain(t("launch.result_unknown"));
  });

  it("keeps the memo to finish when the repository dialog is closed back to the hub", async () => {
    const launched = vi.fn();
    useLaunchSeed.getState().set("メモの本文");
    useLaunchSeed.getState().setMemoId("m1");
    useLaunchSeed.getState().setOnLaunched(launched);
    await openHub("repo");
    await click([...document.querySelectorAll<HTMLButtonElement>(".start-row")].find((b) => b.textContent?.includes("app"))!);
    const closes = document.querySelectorAll<HTMLButtonElement>(`button[aria-label="${t("ui.close")}"]`);
    await click(closes[closes.length - 1]);
    expect(document.querySelectorAll(".ui-modal")).toHaveLength(1);
    await click(head(t("start.sec_space")));
    await click(spaceGo());
    expect(launched).toHaveBeenCalledWith("space-1");
  });
});
