// The Start hub's "start in a new folder" stage. The point is one thing: the folder just
// created is handed straight on to the launch. Without the link to onPickRepo, all the user
// gets is a working copy and they have to go hunting for the launch button in the left pane —
// the same gap that the clone flow's "start from here" was added to close.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../lib/i18n/index.ts";

const initRepo = vi.fn();
vi.mock("./clone.ts", () => ({
  cloneRepo: vi.fn(),
  svnCheckout: vi.fn(),
  initRepo: (...a: unknown[]) => initRepo(...a),
}));
vi.mock("../chat/api.ts", () => ({ assistantList: vi.fn(async () => ({ assistants: [] })) }));
// api has to answer differently per path, so it funnels through one replaceable mock (an empty
// array by default).
const apiGet = vi.fn(async (_path: string): Promise<unknown> => []);
const apiPost = vi.fn(async (..._a: unknown[]): Promise<unknown> => ({}));
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => apiGet(path),
  apiJSON: (...a: unknown[]) => apiPost(...a),
  isTransientErr: () => false,
  errText: (e: { message?: string }) => e?.message || "",
  errDetail: (e: { message?: string }) => e?.message || "",
  pasteImage: vi.fn(),
}));

const { StartModal } = await import("./StartModal.tsx");
const { useReposStore } = await import("./store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
const onPickRepo = vi.fn();

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
        <StartModal kinds={kinds} onClose={() => {}} onPickRepo={onPickRepo} />
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
  onPickRepo.mockReset();
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

// The home stage's own launch button — a separate component from LaunchModal.tsx with its own
// model state wiring, so a copy-paste mistake there would not be caught by LaunchModal's own
// tests. lcpp has no CLI-picked own default (docs/log/109): unlike codex/opencode, launching it
// with no model selected used to POST model="" and fail the very first turn silently.
describe("StartModal — home stage lcpp model requirement (docs/log/109)", () => {
  async function settle(): Promise<void> {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("disables Begin while the lcpp catalog is empty, then sends the auto-picked model", async () => {
    unmount();
    apiGet.mockImplementation(async (path: string) =>
      path.includes("agents/lcpp/models") ? { models: [] } : [],
    );
    await mount(["lcpp"]);
    await click(rowFor(t("start.home_title")));
    await settle();
    expect(footButton(t("launch.launch")).disabled).toBe(true);

    unmount();
    apiGet.mockImplementation(async (path: string) =>
      path.includes("agents/lcpp/models") ? { models: [{ id: "m1", label: "m1" }] } : [],
    );
    // Stop startWork right after it captures the POST body, before it reaches session-opening
    // code this test does not mock (open.ts).
    apiPost.mockImplementation(async (...a: unknown[]) =>
      a[0] === "api/sessions" ? { error: { message: "stop-here" } } : {},
    );
    await mount(["lcpp"]);
    await click(rowFor(t("start.home_title")));
    await settle();
    expect(footButton(t("launch.launch")).disabled).toBe(false);

    await click(footButton(t("launch.launch")));
    const call = apiPost.mock.calls.find((c) => c[0] === "api/sessions");
    expect(call?.[2]).toMatchObject({ model: "m1" });
  });
});
