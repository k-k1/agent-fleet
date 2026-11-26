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
const { useSpacesStore } = await import("../spaces/store.ts");

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
  useSpacesStore.getState().clear();
  useSessionsStore.setState({ sessions: [] });
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
    useLaunchSeed.getState().setInboxMemo("im1");
    useLaunchSeed.getState().setOnLaunched(launched);
    await openHub();
    expect(hubBox().value).toBe("メモの本文");
    await click(spaceGo());
    expect((createCalls()[0][2] as Record<string, unknown>).initial_prompt).toBe("メモの本文");
    expect(launched).toHaveBeenCalledWith("space-1");
    expect((createCalls()[0][2] as Record<string, unknown>).inbox_memo_id).toBe("im1"); // it did get the memo
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
    useLaunchSeed.getState().setInboxMemo("im1"); // a memo taken in from the inbox
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
    await click(foot().querySelector<HTMLButtonElement>("button.ui-btn-primary")!);
    expect(createCalls()).toHaveLength(1);
    expect(createCalls()[0][2]).not.toHaveProperty("inbox_memo_id"); // no provenance for a memo it never got
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
    await click(foots[foots.length - 1].querySelector<HTMLButtonElement>("button.ui-btn-primary")!);
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

describe("Start hub — an existing space", () => {
  const space = (folder: string, name: string, createdAt: string) => ({ folder, name, dir: "/home/dev/spaces/" + folder, createdAt });
  const withSpaces = (spaces: ReturnType<typeof space>[]) =>
    apiGet.mockImplementation(async (path: string) => (path === "api/spaces" ? { spaces } : []));
  const chip = () => document.querySelector<HTMLButtonElement>(".start-place-chip--btn")!;
  const options = () => [...document.querySelectorAll<HTMLButtonElement>(".start-place-opt")];
  const optionLabels = () => options().map((o) => o.textContent);
  const nameBox = () => document.querySelector<HTMLInputElement>(".start-place input[type=text]");

  it("offers the spaces by last use, and starts a session in the one picked, by folder alone", async () => {
    withSpaces([space("old-aaa", "Budget", "2026-09-01T00:00:00Z"), space("new-bbb", "", "2026-11-20T00:00:00Z")]);
    // Budget's own age is older, but a session started in it yesterday.
    useSessionsStore.setState({ sessions: [{ name: "s1", kind: "claude", space: "old-aaa", createdAt: "2026-11-23T10:00:00Z" }] as never });
    await openHub();
    expect(nameBox()).not.toBeNull(); // a new space is named in place
    await click(chip());
    expect(optionLabels()).toEqual([t("start.place_new_space"), "Budget", "new-bbb"]);
    await click(options()[1]);
    expect(chip().textContent).toContain("Budget");
    expect(nameBox()).toBeNull(); // an existing space is not renamed by a launch
    expect(spaceGo().textContent).toContain(t("start.space_launch_existing"));
    await typeInto(hubBox(), "続きをお願い");
    await click(spaceGo());
    const body = createCalls()[0][2] as Record<string, unknown>;
    expect(body).toMatchObject({ dir: "", space: { folder: "old-aaa" }, initial_prompt: "続きをお願い" });
    expect((body.space as Record<string, unknown>).name).toBeUndefined();
  });

  it("lists five as recent and the rest behind All spaces", async () => {
    const many = Array.from({ length: 8 }, (_, i) => space("sp-" + i, "Space " + i, `2026-11-0${i + 1}T00:00:00Z`));
    withSpaces(many);
    await openHub();
    await click(chip());
    expect(options()).toHaveLength(1 + 5); // New space + five recent
    expect(optionLabels().slice(1)).toEqual(["Space 7", "Space 6", "Space 5", "Space 4", "Space 3"]);
    await click(document.querySelector<HTMLButtonElement>(".start-place-more")!);
    expect(optionLabels().slice(6)).toEqual(["Space 2", "Space 1", "Space 0"]);
    await click(options()[8]); // an older one can be picked too
    expect(chip().textContent).toContain("Space 0");
  });

  const spaceAsks = () => apiGet.mock.calls.filter((c) => c[0] === "api/spaces").length;

  it("stops a launch aimed at a space that is gone, keeps the draft, and goes on once another place is picked", async () => {
    withSpaces([space("gone-aaa", "Gone", "2026-11-01T00:00:00Z")]);
    await openHub();
    await click(chip());
    await click(options()[1]);
    await typeInto(hubBox(), "続きの作業");
    await act(async () => useSpacesStore.setState({ spaces: [] }));
    await settle();
    // Not silently a new space: the pick stays, marked, and nothing can launch it.
    expect(chip().textContent).toContain("gone-aaa"); // its name left with the list; the folder stands in
    expect(chip().className).toContain("start-place-chip--gone");
    expect(document.body.textContent).toContain(t("start.space_gone"));
    expect(spaceGo().disabled).toBe(true);
    await act(async () => {
      hubBox().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
    });
    await settle();
    expect(createCalls()).toHaveLength(0);
    expect(hubBox().value).toBe("続きの作業");
    await click(chip());
    await click(options()[0]); // New space, chosen on purpose
    await click(spaceGo());
    expect(createCalls()[0][2]).toMatchObject({ space: { name: "" }, initial_prompt: "続きの作業" });
  });

  it("refuses the same pick right after the Agent says the space is not found, even while the list is still being read", async () => {
    withSpaces([space("gone-aaa", "Gone", "2026-11-01T00:00:00Z")]);
    apiPost.mockImplementation(async (...a: unknown[]) =>
      a[0] === "api/sessions" ? { error: { code: "space_not_found", message: "no such space: gone-aaa" } } : {},
    );
    await openHub();
    await click(chip());
    await click(options()[1]);
    await typeInto(hubBox(), "続き");
    // The re-read that follows the 404 never answers: the refusal cannot wait for it.
    apiGet.mockImplementation(() => new Promise(() => {}));
    await click(spaceGo());
    expect(createCalls()).toHaveLength(1);
    expect(spaceAsks()).toBe(2); // the hub's one ask, then the re-read after the 404
    expect(spaceGo().disabled).toBe(true);
    expect(chip().className).toContain("start-place-chip--gone");
    await click(spaceGo());
    await act(async () => {
      hubBox().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
    });
    await settle();
    expect(createCalls()).toHaveLength(1); // the retry is not sent
    expect(hubBox().value).toBe("続き");
  });

  it("asks the Agent for the spaces once when the hub opens, and \"Check again\" refills the chip's list", async () => {
    apiGet.mockImplementation(async (path: string) => (path === "api/spaces" ? { error: { code: "http_500", message: "x" } } : []));
    await openHub();
    expect(spaceAsks()).toBe(1);
    expect(document.body.textContent).toContain(t("start.space_check_failed"));
    withSpaces([space("sp-a", "Notes", "2026-11-01T00:00:00Z")]);
    await click([...document.querySelectorAll<HTMLButtonElement>("button.linklike")].find((b) => b.textContent === t("start.space_check_again"))!);
    expect(spaceAsks()).toBe(2);
    await click(chip());
    expect(optionLabels()).toEqual([t("start.place_new_space"), "Notes"]);
  });

  it("still gets the list when the probe's answer arrives after something cleared or superseded it", async () => {
    // First ask (the probe) is held; every later ask answers at once.
    let answerProbe!: (v: unknown) => void;
    let asks = 0;
    apiGet.mockImplementation((path: string) => {
      if (path !== "api/spaces") return Promise.resolve([]);
      asks++;
      return asks === 1 ? new Promise((r) => (answerProbe = r)) : Promise.resolve({ spaces: [space("sp-a", "Notes", "2026-11-01T00:00:00Z")] });
    });
    await openHub();
    // A switch of workspace clears the store after the probe began (App does this).
    await act(async () => useSpacesStore.getState().clear());
    await act(async () => answerProbe({ spaces: [space("sp-a", "Notes", "2026-11-01T00:00:00Z")] }));
    await settle();
    expect(spaceAsks()).toBe(2); // the probe, then the re-read for the workspace now current
    await click(chip());
    expect(optionLabels()).toEqual([t("start.place_new_space"), "Notes"]);
    expect(document.body.textContent).not.toContain(t("start.space_check_failed"));
  });

  it("closes the list with Escape alone, and returns the focus to the chip after a pick", async () => {
    withSpaces([space("sp-a", "Notes", "2026-11-01T00:00:00Z")]);
    await openHub();
    await click(chip());
    expect(options()).toHaveLength(2);
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    await settle();
    expect(options()).toHaveLength(0);
    expect(document.querySelector(".start-hub")).not.toBeNull(); // the hub itself stayed open
    expect(document.activeElement).toBe(chip());
    await click(chip());
    await click(options()[1]);
    expect(document.activeElement).toBe(chip());
  });

  it("uploads the draft's files to the existing space's new session like a new one's", async () => {
    withSpaces([space("sp-a", "Notes", "2026-11-01T00:00:00Z")]);
    await openHub();
    await click(chip());
    await click(options()[1]);
    const file = new File(["B"], "shot.png", { type: "image/png" });
    await attachToHub(file);
    await click(spaceGo());
    expect(pasteImage.mock.calls[0]).toEqual(["space-1", file]);
  });

  describe("a space an agent is running in (#5230)", () => {
    const busyText = (who: string) => t("start.space_busy", { who });
    const askButtons = () => [...document.querySelectorAll<HTMLButtonElement>(".start-space-busy button")];
    const pickNotes = async () => {
      await openHub();
      await click(chip());
      await click(options()[1]);
    };
    beforeEach(() => withSpaces([space("sp-a", "Notes", "2026-11-01T00:00:00Z")]));

    it("marks the space, asks before launching, and starts in a new space by default", async () => {
      useSessionsStore.setState({ sessions: [{ name: "s1", kind: "codex", space: "sp-a", alive: true, title: "Report draft" }] as never });
      await openHub();
      await click(chip());
      expect(options()[1].querySelector(".start-place-busy")?.textContent).toBe(t("start.place_running"));
      expect(options()[0].querySelector(".start-place-busy")).toBeNull();
      await click(options()[1]);
      expect(chip().querySelector(".start-place-busy")).not.toBeNull();
      await typeInto(hubBox(), "続き");
      await click(spaceGo());
      expect(createCalls()).toHaveLength(0);
      expect(document.body.textContent).toContain(busyText("Report draft"));
      const [fresh, here] = askButtons();
      expect(fresh.textContent).toBe(t("start.space_busy_new"));
      expect(here.textContent).toBe(t("start.space_busy_here"));
      expect(document.activeElement).toBe(fresh); // the default is the safe answer
      await click(fresh);
      expect(createCalls()).toHaveLength(1);
      expect(createCalls()[0][2]).toMatchObject({ space: { name: "" }, initial_prompt: "続き" });
    });

    it("starts in the busy space only on the explicit answer, and says so to the Agent", async () => {
      useSessionsStore.setState({ sessions: [{ name: "s1", kind: "claude", space: "sp-a", alive: true }] as never });
      await pickNotes();
      await click(spaceGo());
      await click(askButtons()[1]);
      expect(createCalls()).toHaveLength(1);
      expect(createCalls()[0][2]).toMatchObject({ space: { folder: "sp-a", allow_busy: true } });
    });

    it("does not ask for a terminal or a stopped session, and sends no opt-in", async () => {
      useSessionsStore.setState({
        sessions: [
          { name: "t1", kind: "shell", space: "sp-a", alive: true },
          { name: "s1", kind: "claude", space: "sp-a", alive: false },
        ] as never,
      });
      await pickNotes();
      expect(chip().querySelector(".start-place-busy")).toBeNull();
      await click(spaceGo());
      expect(askButtons()).toHaveLength(0);
      const body = createCalls()[0][2] as { space: Record<string, unknown> };
      expect(body.space).toEqual({ folder: "sp-a" });
    });

    it("asks with the Agent's answer when the list was stale, without a toast, and drops the question on another pick", async () => {
      apiPost.mockImplementation(async (...a: unknown[]) => {
        if (a[0] !== "api/sessions") return {};
        const sp = (a[2] as { space: { allow_busy?: boolean } }).space;
        return sp.allow_busy ? { name: "space-2" } : { error: { code: "space_busy", message: "busy", session: "s9", title: "Nightly run" } };
      });
      await pickNotes();
      await typeInto(hubBox(), "続き");
      await click(spaceGo());
      expect(createCalls()).toHaveLength(1);
      expect(document.body.textContent).toContain(busyText("Nightly run"));
      expect(document.querySelector(".ui-toast")).toBeNull();
      expect(hubBox().value).toBe("続き");
      await click(chip());
      await click(options()[0]);
      expect(askButtons()).toHaveLength(0);
      await click(chip());
      await click(options()[1]);
      await click(spaceGo());
      await click(askButtons()[1]);
      expect(createCalls()).toHaveLength(3);
      expect(createCalls()[2][2]).toMatchObject({ space: { folder: "sp-a", allow_busy: true } });
    });

    it("after the Agent's 409, sends nothing but an explicit answer while the session list is still being read", async () => {
      apiPost.mockImplementation(async (...a: unknown[]) => {
        if (a[0] !== "api/sessions") return {};
        const sp = (a[2] as { space: { allow_busy?: boolean } }).space;
        return sp.allow_busy ? { name: "space-2" } : { error: { code: "space_busy", message: "busy", session: "s9", title: "Nightly run" } };
      });
      await pickNotes();
      await typeInto(hubBox(), "続き");
      // The re-read after the 409 never answers: the store still says nobody runs there.
      apiGet.mockImplementation((path: string) => (path === "api/sessions" ? new Promise(() => {}) : Promise.resolve(path === "api/spaces" ? { spaces: [] } : [])));
      await click(spaceGo());
      expect(createCalls()).toHaveLength(1);
      expect(askButtons()).toHaveLength(2);
      expect(spaceGo().disabled).toBe(true);
      await click(spaceGo());
      await act(async () => {
        hubBox().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
      });
      await settle();
      expect(createCalls()).toHaveLength(1); // neither the button nor the send key re-sent it
      expect(document.activeElement).toBe(askButtons()[0]); // the send key points at the default answer
      // Picking the same space again keeps the question.
      await click(chip());
      await click(options()[1]);
      expect(askButtons()).toHaveLength(2);
      await click(askButtons()[1]);
      expect(createCalls()).toHaveLength(2);
      expect(createCalls()[1][2]).toMatchObject({ space: { folder: "sp-a", allow_busy: true } });
    });

    it("starts the new space under the typed name, and keeps it picked with the draft when that launch fails", async () => {
      useSessionsStore.setState({ sessions: [{ name: "s1", kind: "claude", space: "sp-a", alive: true }] as never });
      apiPost.mockImplementation(async (...a: unknown[]) => (a[0] === "api/sessions" ? { error: { code: "runtime_failed", message: "no" } } : {}));
      await openHub();
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(nameBox()!, "Draft 2");
        nameBox()!.dispatchEvent(new Event("input", { bubbles: true }));
      });
      await click(chip());
      await click(options()[1]);
      await typeInto(hubBox(), "続き");
      await act(async () => {
        hubBox().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
      });
      await settle();
      expect(createCalls()).toHaveLength(0);
      await click(askButtons()[0]);
      expect(createCalls()).toHaveLength(1);
      expect(createCalls()[0][2]).toMatchObject({ space: { name: "Draft 2" }, initial_prompt: "続き" });
      // Failed: the form is on New space now, with the name and the draft, and launches again.
      expect(chip().textContent).toContain(t("start.place_new_space"));
      expect(nameBox()!.value).toBe("Draft 2");
      expect(hubBox().value).toBe("続き");
      expect(askButtons()).toHaveLength(0);
      await click(spaceGo());
      expect(createCalls()).toHaveLength(2);
      expect(createCalls()[1][2]).toMatchObject({ space: { name: "Draft 2" } });
    });
  });
});
