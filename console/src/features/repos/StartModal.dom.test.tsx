// The Start hub's "start in a new folder" stage. The point is one thing: the folder just
// created is handed straight on to the launch. Without the link to onPickRepo, all the user
// gets is a working copy and they have to go hunting for the launch button in the left pane —
// the same gap that the clone flow's "start from here" was added to close.
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../lib/i18n/index.ts";

const initRepo = vi.fn();
const cloneRepo = vi.fn();
vi.mock("./clone.ts", () => ({
  cloneRepo: (...a: unknown[]) => cloneRepo(...a),
  svnCheckout: vi.fn(),
  initRepo: (...a: unknown[]) => initRepo(...a),
}));
vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));
// A fine pointer, so the composer takes the focus as it does on a desktop.
vi.mock("../../lib/device.ts", async (orig) => ({ ...(await orig<object>()), coarsePointer: () => false }));
// api has to answer differently per path, so it funnels through one replaceable mock (an empty
// array by default).
const apiGet = vi.fn(async (_path: string): Promise<unknown> => []);
const apiPost = vi.fn(async (..._a: unknown[]): Promise<unknown> => ({}));
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: (path: string) => apiGet(path),
  apiJSON: (...a: unknown[]) => apiPost(...a),
  isTransientErr: () => false,
  errText: (e: { message?: string }) => e?.message || "",
  errDetail: (e: { message?: string }) => e?.message || "",
  pasteImage: vi.fn(),
  repoPromptTemplates: vi.fn(async () => ({ groups: [] })),
  repoSkills: vi.fn(async () => ({ skills: [] })),
}));

const { StartModal } = await import("./StartModal.tsx");
const { useReposStore } = await import("./store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useUIStateStore } = await import("../../core/store/uiState.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { useHubDraft } = await import("./hubDraft.ts");
const { writeFoldValue } = await import("../../lib/foldStore.ts");

let root: Root | null = null;
let host: HTMLDivElement;
const onPickRepo = vi.fn();
const onPickHome = vi.fn();
const onLaunchSpace = vi.fn(async (..._a: unknown[]) => ({ ok: true, name: "s-1" }));
const onClose = vi.fn();
const access = { kinds: { get: (k: string) => ({ kind: k }), has: () => true } as never, noRoute: false, allOff: false, refresh: () => {} };

// The draft is the host's (StartHost); a stand-in host owns it the same way.
function Host({ kinds, pickPending }: { kinds: string[]; pickPending?: boolean }) {
  const draft = useHubDraft();
  return <StartModal kinds={kinds} access={access} draft={draft} pickPending={pickPending} onLaunchSpace={onLaunchSpace} onClose={onClose} onPickRepo={onPickRepo} onPickHome={onPickHome} />;
}

const rows = () => [...document.querySelectorAll<HTMLButtonElement>(".start-row")];
const rowFor = (label: string) => rows().find((b) => b.textContent?.includes(label))!;
const nameInput = () => document.querySelector<HTMLInputElement>(".ui-modal-body input")!;
const footButton = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>(".ui-modal-foot button")].find((b) => b.textContent?.includes(label))!;

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
}

async function type(input: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function mount(kinds: string[] = ["claude"], pickPending = false): Promise<void> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <Host kinds={kinds} pickPending={pickPending} />
      </ToastProvider>,
    );
  });
}

function unmount(): void {
  act(() => root?.unmount());
  root = null;
  host.remove();
}

beforeEach(async () => {
  localStorage.clear();
  // Most rows live in Other; the tests about the sections and the repositories open their own.
  writeFoldValue("af-start-section", "other");
  onLaunchSpace.mockClear();
  onClose.mockReset();
  initRepo.mockReset();
  cloneRepo.mockReset();
  onPickRepo.mockReset();
  onPickHome.mockReset();
  apiGet.mockReset();
  apiGet.mockImplementation(async () => []);
  apiPost.mockReset();
  apiPost.mockImplementation(async () => ({}));
  useReposStore.setState({ repos: [], loadState: "ok" });
  await mount();
});

afterEach(() => {
  if (root) unmount();
});

describe("StartModal — start in a new folder", () => {
  it("hands the folder it just created straight to the start-work dialog", async () => {
    const created = { name: "new-project", branch: "main", unborn: true, path: "/home/dev/repos/new-project" };
    initRepo.mockImplementation(async () => {
      useReposStore.setState({ repos: [created] });
      return { ok: true, name: "new-project" };
    });

    await click(rowFor(t("start.newdir_title")));
    await type(nameInput(), "new-project");
    await click(footButton(t("start.create_and_continue")));

    expect(initRepo).toHaveBeenCalledWith("new-project", expect.any(Function));
    // Other's draft is the home launch's: a folder created here does not take it.
    expect(onPickRepo).toHaveBeenCalledWith(created, false);
  });

  it("stays on the stage and does not proceed to the launch when creation fails", async () => {
    initRepo.mockResolvedValue({ ok: false, name: "" });

    await click(rowFor(t("start.newdir_title")));
    await type(nameInput(), "new-project");
    await click(footButton(t("start.create_and_continue")));

    expect(initRepo).toHaveBeenCalled();
    expect(onPickRepo).not.toHaveBeenCalled();
    expect(nameInput()).toBeTruthy(); // the field is still visible, so the name can be fixed
  });

  it("disables the create button for a name that already exists", async () => {
    useReposStore.setState({ repos: [{ name: "docs" }] });
    await click(rowFor(t("start.newdir_title")));
    await type(nameInput(), "docs");
    expect(footButton(t("start.create_and_continue")).disabled).toBe(true);
    await click(footButton(t("start.create_and_continue")));
    expect(initRepo).not.toHaveBeenCalled();
  });
});

// The subtitle of an SSM host card. The point is where the account id comes from: it is an
// attribute of the PROFILE, not of the host (the control-plane's ssmProfileDTO carries it and
// ssmHostDTO does not). Reading `h.accountId` instead is always undefined, since the field is
// not on the wire, and the subtitle then never renders at all — silently, because the field is
// optional and tsc says nothing. One test per reading site: the card subtitle and the dropdown.
describe("StartModal — SSM host card subtitle", () => {
  const profile = { id: "p1", label: "prod", accountId: "123456789012" };
  const host1 = {
    id: "h1",
    alias: "mng@g3prod-mon01",
    profileId: "p1",
    region: "",
    instanceId: "i-0abc123",
    documentName: "",
  };
  const ssmApi = (hosts: unknown[]) => async (path: string) => {
    if (path === "api/ssm/profiles") return [profile];
    if (path === "api/ssm/hosts") return hosts;
    return [];
  };

  // profiles is fetched at mount, so swap the mock in and then mount again.
  async function remountWith(hosts: unknown[]): Promise<void> {
    unmount();
    apiGet.mockImplementation(ssmApi(hosts));
    await mount();
    await click(rowFor(t("start.ssm_title")));
    await act(async () => {}); // let the hosts fetch settle
  }

  it("shows the profile's account id in the card subtitle", async () => {
    await remountWith([host1]);

    const sub = document.querySelector(".ssm-card-sub")!;
    expect(sub).toBeTruthy();
    // Expected: "label · account <id> · instance id".
    expect(sub.textContent).toContain(t("start.ssm_acct", { id: "123456789012" }));
    // Also check the other two parts were not lost along the way (the subtitle joins three).
    expect(sub.textContent).toContain("prod");
    expect(sub.textContent).toContain("i-0abc123");
  });

  it("shows the account id in the options too once there are enough hosts for a dropdown", async () => {
    // SSM_CARD_ALL_MAX = 8: at 9 hosts only the top ones stay cards and the dropdown appears.
    const many = Array.from({ length: 9 }, (_, i) => ({ ...host1, id: `h${i}`, instanceId: `i-${i}` }));
    await remountWith(many);

    const opts = [...document.querySelectorAll("option")].map((o) => o.textContent || "");
    const withAcct = opts.filter((tx) => tx.includes(t("start.ssm_acct", { id: "123456789012" })));
    expect(withAcct.length).toBe(9);
  });
});

// The Terminal view's "SSM session" opens the hub on the host picker instead of the places list.
describe("StartModal — opened on the SSM stage", () => {
  const profile = { id: "p1", label: "prod", accountId: "123456789012" };
  afterEach(() => useSessionsStore.setState({ startStage: null }));

  it("starts on the host picker when the Terminal view asked for it", async () => {
    unmount();
    apiGet.mockImplementation(async (path: string) => (path === "api/ssm/profiles" ? [profile] : path === "api/ssm/hosts" ? [] : []));
    useSessionsStore.getState().openStartAt("ssm");
    await mount();
    await act(async () => {});
    // The host picker is on screen, and the places list is not.
    expect(document.body.textContent).toContain(t("start.ssm_host_label"));
    expect(rows().some((b) => b.textContent?.includes(t("start.ssm_title")))).toBe(false);
  });

  it("starts on the places list for every other way in", async () => {
    unmount();
    apiGet.mockImplementation(async (path: string) => (path === "api/ssm/profiles" ? [profile] : []));
    useSessionsStore.getState().openStart();
    await mount();
    await act(async () => {});
    expect(rowFor(t("start.ssm_title"))).toBeTruthy();
    expect(document.body.textContent).not.toContain(t("start.ssm_host_label"));
  });
});

// The home launch itself lives in StartHost (StartHost.home.dom.test.tsx): the hub only reports the pick.
describe("StartModal — Launch an agent at home", () => {
  it("hands the pick to the host instead of rendering a form of its own", async () => {
    await click(rowFor(t("start.home_title")));
    expect(onPickHome).toHaveBeenCalledTimes(1);
    expect(document.querySelector(".ui-seg.big")).toBeNull(); // no agent picker of its own
  });
});

// Team repositories that are not cloned yet are offered as clone shortcuts into the one clone flow.
describe("StartModal — uncloned team repositories", () => {
  const ops = { grafana_url: "", aws_sso_start_url: "", aws_account_id: "", aws_role_name: "" };
  const launch = { agent: "", model: "", driver: "", permission_mode: "", budget_usd: null };
  const team = (repos: string[]) =>
    act(async () => useUIStateStore.setState({ state: { effective: { launch, repos, ops } } as never }));

  async function remount(): Promise<void> {
    unmount();
    writeFoldValue("af-start-section", "repo");
    await mount();
  }

  it("lists the uncloned one after the cloned team repositories and clones it from the one connected host", async () => {
    apiGet.mockImplementation(async (path: string) => (path === "api/connections" ? { github: { connected: true } } : []));
    useReposStore.setState({
      loadState: "ok",
      repos: [
        { name: "zeta", branch: "main", remotePath: "other/zeta" },
        { name: "api", branch: "main", remotePath: "Acme/Api" },
      ],
    });
    await team(["acme/api", "acme/docs"]);
    await remount();
    const titles = rows().map((b) => b.querySelector(".start-row-title")?.textContent);
    expect(titles.indexOf("api")).toBeLessThan(titles.indexOf("acme/docs"));
    expect(titles.indexOf("acme/docs")).toBeLessThan(titles.indexOf("zeta"));
    expect(titles.filter((x) => x === "acme/api")).toHaveLength(0); // cloned: not listed twice
    cloneRepo.mockResolvedValue({ ok: false });
    await click(rowFor("acme/docs"));
    expect(cloneRepo).toHaveBeenCalledWith({ remote_url: "https://github.com/acme/docs.git", branch: "", name: "" }, expect.any(Function));
  });

  it("offers no shortcut when both providers are connected (the host would be a guess)", async () => {
    apiGet.mockImplementation(async (path: string) => (path === "api/connections" ? { github: { connected: true }, bitbucket: { connected: true } } : []));
    await team(["acme/docs"]);
    await remount();
    expect(rows().some((b) => b.textContent?.includes("acme/docs"))).toBe(false);
  });

  it("shows no shortcut until the repository list has loaded (an existing repo would look uncloned)", async () => {
    apiGet.mockImplementation(async (path: string) => (path === "api/connections" ? { github: { connected: true } } : []));
    useReposStore.setState({ repos: [], loadState: "loading" });
    await team(["acme/docs"]);
    await remount();
    expect(rows().some((b) => b.textContent?.includes("acme/docs"))).toBe(false);
    await act(async () => useReposStore.setState({ loadState: "ok" }));
    expect(rows().some((b) => b.textContent?.includes("acme/docs"))).toBe(true);
  });

  it("offers no team clone while a pick waits on a memo restore", async () => {
    apiGet.mockImplementation(async (path: string) => (path === "api/connections" ? { github: { connected: true } } : []));
    await team(["acme/docs"]);
    unmount();
    writeFoldValue("af-start-section", "repo");
    await mount(["claude"], true);
    expect(rowFor("acme/docs").disabled).toBe(true);
  });

  it("refuses to clone when the connections changed after the list was drawn", async () => {
    let conns: unknown = { github: { connected: true } };
    apiGet.mockImplementation(async (path: string) => (path === "api/connections" ? conns : []));
    await team(["acme/docs"]);
    await remount();
    conns = { github: { connected: true }, bitbucket: { connected: true } };
    await click(rowFor("acme/docs"));
    expect(cloneRepo).not.toHaveBeenCalled();
  });

  it("starts one clone for a double click while the connections check is still pending", async () => {
    let release: (v: unknown) => void = () => {};
    const slow = new Promise((r) => (release = r));
    let gate = false;
    apiGet.mockImplementation(async (path: string) => {
      if (path !== "api/connections") return [];
      return gate ? slow : { github: { connected: true } }; // the mount-time reads answer at once
    });
    await team(["acme/docs"]);
    await remount();
    gate = true;
    cloneRepo.mockResolvedValue({ ok: false });
    const row = rowFor("acme/docs");
    await click(row);
    await click(row);
    await act(async () => release({ github: { connected: true } }));
    expect(cloneRepo).toHaveBeenCalledTimes(1);
    await click(rowFor("acme/docs")); // released again after the failed clone
    expect(cloneRepo).toHaveBeenCalledTimes(2);
  });
});

// The hub is an accordion (ADR 0142 decision 5): one section open, remembered per user and
// tenant, space the very first time; one draft is shown in every section.
describe("StartModal — sections and the shared draft", () => {
  const heads = () => [...document.querySelectorAll<HTMLButtonElement>(".start-sec-head")];
  const head = (label: string) => heads().find((b) => b.textContent?.includes(label))!;
  const openHeads = () => heads().filter((b) => b.getAttribute("aria-expanded") === "true").map((b) => b.textContent);
  const box = () => document.querySelector<HTMLTextAreaElement>("#start-draft-text")!;
  async function typeBox(value: string): Promise<void> {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box(), value);
      box().dispatchEvent(new Event("input", { bubbles: true }));
    });
  }
  async function fresh(spaces: unknown = { spaces: [] }): Promise<void> {
    unmount();
    localStorage.clear();
    apiGet.mockImplementation(async (path: string) => (path === "api/spaces" ? spaces : []));
    await mount();
    await act(async () => {});
  }

  it("opens Start in a space the first time, then the section last opened", async () => {
    await fresh();
    expect(openHeads()).toEqual([t("start.sec_space")]);
    await click(head(t("start.sec_repo")));
    expect(openHeads()).toEqual([t("start.sec_repo")]);
    unmount();
    await mount();
    expect(openHeads()).toEqual([t("start.sec_repo")]);
  });

  it("keeps the text across sections and focuses the open section's box", async () => {
    await fresh();
    await typeBox("議事録をまとめて");
    await click(head(t("start.sec_other")));
    expect(box().value).toBe("議事録をまとめて");
    expect(document.activeElement).toBe(box());
    expect(document.body.textContent).toContain(t("start.draft_note_other"));
    await click(head(t("start.sec_repo")));
    expect(box().value).toBe("議事録をまとめて");
  });

  it("offers no Chat (assistant) row", async () => {
    const titles = rows().map((b) => b.querySelector(".start-row-title")?.textContent);
    expect(titles).toEqual([t("start.home_title"), "shell", t("start.clone_title"), t("start.newdir_title")]);
  });

  it("launches a new space with the draft and the name typed, straight from the hub", async () => {
    await fresh();
    await typeBox("見積もりの比較表を作って");
    await type(document.querySelector<HTMLInputElement>(`input[aria-label="${t("start.space_name_label")}"]`)!, "見積もり");
    const go = [...document.querySelectorAll<HTMLButtonElement>(".start-space-actions button")][0];
    expect(go.disabled).toBe(false);
    await click(go);
    expect(onLaunchSpace).toHaveBeenCalledTimes(1);
    const o = onLaunchSpace.mock.calls[0][0] as Record<string, unknown>;
    expect(o).toMatchObject({ kind: "claude", prompt: "見積もりの比較表を作って", worktree: false, subdir: "", space: { name: "見積もり" } });
    expect(onClose).toHaveBeenCalled();
  });

  it("refuses the space launch on an Agent without spaces, and says so", async () => {
    await fresh({ error: { code: "not_found", status: 404 } });
    expect(document.body.textContent).toContain(t("start.space_unsupported"));
    const go = [...document.querySelectorAll<HTMLButtonElement>(".start-space-actions button")][0];
    expect(go.disabled).toBe(true);
    // The send key is refused the same way: an older Agent would start the session in home.
    await act(async () => {
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
    });
    expect(onLaunchSpace).not.toHaveBeenCalled();
  });

  it("reads a plain-text 404 (an Agent without the route) as no spaces, not as a failed check", async () => {
    await fresh({ error: { code: "http_404", message: "404 page not found" } });
    expect(document.body.textContent).toContain(t("start.space_unsupported"));
    expect(document.body.textContent).not.toContain(t("start.space_check_failed"));
  });

  it("hands a repository row's pick on as one from the repository section", async () => {
    unmount();
    useReposStore.setState({ repos: [{ name: "app", branch: "main" }] as never, loadState: "ok" });
    writeFoldValue("af-start-section", "repo");
    await mount();
    await click(rowFor("app"));
    expect(onPickRepo).toHaveBeenCalledWith(expect.objectContaining({ name: "app" }), true);
  });

  it("takes no other way out while a pick waits on a memo restore", async () => {
    unmount();
    apiGet.mockImplementation(async (path: string) => (path === "api/spaces" ? { spaces: [] } : []));
    writeFoldValue("af-start-section", "repo");
    await mount(["claude"], true);
    await act(async () => {});
    expect(head(t("start.sec_space")).disabled).toBe(true);
    expect(head(t("start.sec_other")).disabled).toBe(true);
  });

  it("offers a second check when the first could not be answered", async () => {
    await fresh({ error: { code: "http_502", status: 502 } });
    expect(document.body.textContent).toContain(t("start.space_check_failed"));
    apiGet.mockImplementation(async (path: string) => (path === "api/spaces" ? { spaces: [] } : []));
    await click([...document.querySelectorAll<HTMLButtonElement>(".linklike")].find((b) => b.textContent === t("start.space_check_again"))!);
    expect(document.body.textContent).not.toContain(t("start.space_check_failed"));
    expect([...document.querySelectorAll<HTMLButtonElement>(".start-space-actions button")][0].disabled).toBe(false);
  });
});
