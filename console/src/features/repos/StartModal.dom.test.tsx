// The Start hub's "start in a new folder" stage. The point is one thing: the folder just
// created is handed straight on to the launch. Without the link to onPickRepo, all the user
// gets is a working copy and they have to go hunting for the launch button in the left pane —
// the same gap that the clone flow's "start from here" was added to close.
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
vi.mock("../chat/api.ts", () => ({ assistantList: vi.fn(async () => ({ assistants: [] })) }));
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
}));

const { StartModal } = await import("./StartModal.tsx");
const { useReposStore } = await import("./store.ts");
const { useUIStateStore } = await import("../../core/store/uiState.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
const onPickRepo = vi.fn();
const onPickHome = vi.fn();

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

async function mount(kinds: string[] = ["claude"]): Promise<void> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <StartModal kinds={kinds} onClose={() => {}} onPickRepo={onPickRepo} onPickHome={onPickHome} />
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
  initRepo.mockReset();
  cloneRepo.mockReset();
  onPickRepo.mockReset();
  onPickHome.mockReset();
  apiGet.mockReset();
  apiGet.mockImplementation(async () => []);
  apiPost.mockReset();
  apiPost.mockImplementation(async () => ({}));
  useReposStore.setState({ repos: [] });
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
    expect(onPickRepo).toHaveBeenCalledWith(created);
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

// The home launch itself lives in StartHost (StartHost.home.dom.test.tsx): the hub only reports the pick.
describe("StartModal — Launch an agent at home", () => {
  it("hands the pick to the host instead of rendering a form of its own", async () => {
    await click(rowFor(t("start.home_title")));
    expect(onPickHome).toHaveBeenCalledTimes(1);
    expect(document.querySelector("textarea")).toBeNull();
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
    await mount();
  }

  it("lists the uncloned one after the cloned team repositories and clones it from the one connected host", async () => {
    apiGet.mockImplementation(async (path: string) => (path === "api/connections" ? { github: { connected: true } } : []));
    useReposStore.setState({
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
});
