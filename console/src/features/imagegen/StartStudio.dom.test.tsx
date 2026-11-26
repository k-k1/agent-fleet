// "Start an image studio" from a working-copy row: one dialog with the image model on top, the
// remembered agent settings folded into one summary line, and the place fixed to the row. What
// is pinned here is what goes out — the session's dir/worktree/branch, the studio's model and
// its default title — because a wrong one starts an agent somewhere the member did not pick.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let personaFails = false;
let spacesAnswer: unknown = { spaces: [] };
let sessionRows: unknown[] = [];
let sessionReply: Record<string, unknown> | null = null;
let spacesCalls = 0;
const refreshSessions = vi.fn(async () => {});
const posts: { path: string; body: Record<string, unknown> }[] = [];
const puts: { path: string; body: Record<string, unknown> }[] = [];
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  getTenant: () => "t1",
  api: async (path: string) => {
    if (path === "api/spaces") {
      spacesCalls++;
      return spacesAnswer;
    }
    if (path === "api/imagegen/status")
      return {
        providers: [
          { id: "comfy", kind: "comfy", fleet: true, ready: true, models: [{ id: "sdxl", label: "SDXL" }, { id: "flux", label: "Flux" }] },
        ],
      };
    if (path.endsWith("/persona")) return personaFails ? { error: { code: "boom" } } : { prompt: "persona", lang: "ja" };
    if (path.startsWith("api/imagegen/studios/")) return { id: "st9", title: "", draft: {}, updated_at: "v1" };
    return {};
  },
  apiJSON: async (path: string, _m: string, body: Record<string, unknown>) => {
    posts.push({ path, body });
    if (path === "api/imagegen/studios") return { id: "st9", title: body.title, draft: body.draft, updated_at: "v1" };
    return (path === "api/sessions" && sessionReply) || { name: "snew" };
  },
  raw: async (path: string, init?: { method?: string; body?: string }) => {
    if (init?.method === "PUT") puts.push({ path, body: JSON.parse(init.body || "{}") });
    return new Response("{}", { status: 200 });
  },
}));
vi.mock("../repos/useRepoRail.ts", () => ({
  useRepoRailContext: () => ({ launchKinds: ["claude", "codex"], connsSettling: false }),
}));
vi.mock("../../ui/ModelPicker.tsx", () => ({
  ModelPicker: () => <span className="model-picker-stub" />,
  EffortPicker: () => <span className="effort-picker-stub" />,
}));
vi.mock("../repos/SubdirPicker.tsx", () => ({ SubdirPicker: () => <span /> }));
const toast = vi.fn();
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
vi.mock("../sessions/store.ts", () => ({
  useSessionsStore: (sel: (s: { refresh: () => Promise<void>; sessions: unknown[] }) => unknown) => sel({ refresh: refreshSessions, sessions: sessionRows }),
}));
const opened: unknown[] = [];
vi.mock("./open.ts", () => ({ openImagegen: (o: unknown) => opened.push(o) }));

const { StartStudioModal } = await import("./parts/StartStudioModal.tsx");
const { useReposStore } = await import("../repos/store.ts");
const { useSpacesStore } = await import("../spaces/store.ts");
const { attachLastKey } = await import("./attachPlan.ts");
import { setLocale } from "../../lib/i18n/index.ts";
import type { Repo } from "../repos/store.ts";

const BASE: Repo = { name: "app", path: "/home/dev/repos/app", branch: "main" };
const WT: Repo = { name: "app@wip-x", path: "/home/dev/repos/app@wip-x", branch: "temp/x", worktree: true, parent: "app" };

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root;
let host: HTMLDivElement;

async function render(repo: Repo, onClose: () => void = () => {}): Promise<void> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root.render(<StartStudioModal repo={repo} onClose={onClose} />));
  await act(async () => {
    await Promise.resolve();
  });
}

const buttons = () => [...document.querySelectorAll<HTMLButtonElement>("button")];
const byText = (t: string) => {
  const b = buttons().find((x) => x.textContent?.trim() === t || x.textContent?.includes(t));
  if (!b) throw new Error(`no button "${t}"`);
  return b;
};
const click = async (el: HTMLElement) => act(async () => el.click());
const startBtn = () => byText("始める");
const summary = () => document.querySelector("[data-testid=attach-summary]")?.textContent ?? null;
const wtBox = () => document.querySelector<HTMLInputElement>(".igen-attach-wt input");
async function pickImage(id: string) {
  const sel = [...document.querySelectorAll<HTMLSelectElement>(".igen-attach-image select")].pop()!;
  await act(async () => {
    sel.value = id;
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });
}
async function settle() {
  for (let i = 0; i < 5; i++)
    await act(async () => {
      await Promise.resolve();
    });
}
const sessionPost = () => posts.find((p) => p.path === "api/sessions")?.body;
const studioPost = () => posts.find((p) => p.path === "api/imagegen/studios")?.body;

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  setLocale("ja");
  localStorage.clear();
  posts.length = 0;
  puts.length = 0;
  opened.length = 0;
  toast.mockClear();
  spacesAnswer = { spaces: [] };
  sessionRows = [];
  sessionReply = null;
  spacesCalls = 0;
  refreshSessions.mockClear();
  useSpacesStore.getState().clear();
  useReposStore.setState({ repos: [BASE, WT] });
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("start a studio from a working-copy row", () => {
  it("will not start until an image model is picked", async () => {
    await render(BASE);
    expect(startBtn().disabled).toBe(true);
    await pickImage("flux");
    expect(startBtn().disabled).toBe(false);
  });

  it("starts in a worktree row's own folder by default, titled after it", async () => {
    await render(WT);
    expect(document.querySelector("[data-testid=attach-place]")?.textContent).toContain("app@wip-x");
    expect(wtBox()?.checked).toBe(false);
    await pickImage("sdxl");
    await click(startBtn());
    await settle();
    expect(studioPost()).toEqual({ draft: { provider: "comfy", model: "sdxl" }, title: "app@wip-x · Claude Code" });
    const s = sessionPost()!;
    expect(s.dir).toBe(WT.path);
    expect(s.worktree).toBeUndefined();
    expect(s.branch).toBeUndefined();
    expect(s.studio).toBe("st9");
    expect(opened).toEqual([{ studioId: "st9", newPane: true }]);
  });

  it("cuts a new worktree from the parent at the row's branch when the agent needs one", async () => {
    await render(WT);
    await click(byText("マネージド"));
    await click(document.querySelector<HTMLButtonElement>('[data-kind="codex"]')!);
    expect(wtBox()?.checked).toBe(true);
    expect(wtBox()?.disabled).toBe(true);
    expect(document.body.textContent).toContain("新しい worktree が必要");
    await pickImage("sdxl");
    await click(startBtn());
    await settle();
    const s = sessionPost()!;
    expect(s).toMatchObject({ dir: BASE.path, worktree: true, branch: "temp/x", new_branch: "", kind: "codex", driver: "managed" });
  });

  it("starts a base clone in a new worktree off its branch", async () => {
    await render(BASE);
    expect(wtBox()?.checked).toBe(true);
    await pickImage("sdxl");
    await click(startBtn());
    await settle();
    expect(sessionPost()).toMatchObject({ dir: BASE.path, worktree: true, branch: "main" });
  });

  it("opens folded to last time's settings, and one press starts with them", async () => {
    localStorage.setItem(
      attachLastKey("t1"),
      JSON.stringify({ driver: "tui", kind: "codex", model: "gpt-x", effort: "", repo: "app", worktree: true, imageProvider: "comfy", imageModel: "flux" }),
    );
    await render(BASE);
    await settle();
    expect(summary()).toBe("Codex · ターミナル（CLI） · gpt-x · app（新しい worktree · main から）");
    // Folded: the agent controls are not on screen.
    expect(document.querySelector('[data-kind="codex"]')).toBeNull();
    expect(startBtn().disabled).toBe(false);
    await click(startBtn());
    await settle();
    expect(sessionPost()).toMatchObject({ kind: "codex", driver: "tui", model: "gpt-x", dir: BASE.path, worktree: true });
    expect(studioPost()?.draft).toEqual({ provider: "comfy", model: "flux" });
  });

  it("opens unfolded when the remembered working copy is gone", async () => {
    localStorage.setItem(
      attachLastKey("t1"),
      JSON.stringify({ driver: "tui", kind: "claude", model: "", effort: "", repo: "deleted-repo", worktree: true, imageProvider: "", imageModel: "" }),
    );
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    const { AttachAgentModal } = await import("./parts/AttachAgentModal.tsx");
    await act(async () => root.render(<AttachAgentModal onClose={() => {}} onAttach={async () => true} />));
    await settle();
    expect(summary()).toBeNull();
    expect(document.querySelector('[data-kind="claude"]')).not.toBeNull();
  });

  it("remembers what it started with", async () => {
    await render(BASE);
    await pickImage("flux");
    await click(startBtn());
    await settle();
    const saved = JSON.parse(localStorage.getItem(attachLastKey("t1")) || "null");
    expect(saved).toMatchObject({ kind: "claude", driver: "tui", imageProvider: "comfy", imageModel: "flux" });
  });

  it("does not fall back to another engine when the remembered one is gone", async () => {
    localStorage.setItem(
      attachLastKey("t1"),
      JSON.stringify({ driver: "tui", kind: "claude", model: "", effort: "", repo: "app", worktree: true, imageProvider: "gone", imageModel: "sdxl" }),
    );
    await render(BASE);
    await settle();
    expect(summary()).toBeNull();
    expect(startBtn().disabled).toBe(true);
  });

  it("opens the studio it made when the dialog is closed after a failed start", async () => {
    personaFails = true;
    const onClose = vi.fn();
    await render(BASE, onClose);
    await pickImage("sdxl");
    await click(startBtn());
    await settle();
    personaFails = false;
    expect(sessionPost()).toBeUndefined();
    expect(opened).toEqual([]);
    await click(byText("キャンセル"));
    expect(opened).toEqual([{ studioId: "st9", newPane: true }]);
    expect(onClose).toHaveBeenCalled();
  });

  it("unfolds with the reason when last time's new worktree cannot be cut any more", async () => {
    useReposStore.setState({ repos: [WT] });
    localStorage.setItem(
      attachLastKey("t1"),
      JSON.stringify({ driver: "tui", kind: "claude", model: "", effort: "", repo: WT.name, worktree: true, imageProvider: "comfy", imageModel: "sdxl" }),
    );
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    const { AttachAgentModal } = await import("./parts/AttachAgentModal.tsx");
    await act(async () => root.render(<AttachAgentModal onClose={() => {}} onAttach={async () => true} />));
    await settle();
    expect(summary()).toBeNull();
    expect(document.body.textContent).toContain("親のクローンが一覧にない");
  });

  it("names the image model and the agent's model apart", async () => {
    await render(BASE);
    const labels = [...document.querySelectorAll(".igen-attach .igen-label, .igen-attach .ui-field-label")].map((e) => e.textContent);
    expect(labels).toContain("画像モデル");
    expect(labels).toContain("エージェントのモデル");
    expect(labels).not.toContain("モデル");
  });
});

// A space as the place (#5232): the same `space` field the Start hub sends, in place of a
// directory, so a member with no working copy can still start a studio.
describe("start a studio in a space", () => {
  const placeSelect = () => [...document.querySelectorAll<HTMLSelectElement>(".igen-attach select")].find((el) => [...el.options].some((o) => o.text.includes("ホーム")))!;
  const goBtn = () => document.querySelector<HTMLButtonElement>(".ui-modal-foot .ui-btn-primary")!;
  async function renderOpen(): Promise<void> {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    const { AttachAgentModal } = await import("./parts/AttachAgentModal.tsx");
    const { attachAgent } = await import("./attach.ts");
    await act(async () =>
      root.render(<AttachAgentModal onClose={() => {}} onAttach={async (o) => {
            const r = await attachAgent({ studioId: null, draft: () => ({}), opts: o });
            return r.session !== undefined ? true : r.spaceRefused ? "space_refused" : false;
          }} />),
    );
    await settle();
  }
  async function choose(label: string) {
    const sel = placeSelect();
    const opt = [...sel.options].find((o) => o.text.includes(label))!;
    await act(async () => {
      sel.value = opt.value;
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
  }

  it("a member without working copies starts in a new space by default, and nothing names a directory", async () => {
    useReposStore.setState({ repos: [] });
    const attached: Record<string, unknown>[] = [];
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    const { AttachAgentModal } = await import("./parts/AttachAgentModal.tsx");
    const { attachAgent } = await import("./attach.ts");
    await act(async () =>
      root.render(
        <AttachAgentModal
          onClose={() => {}}
          onAttach={async (o) => {
            attached.push(o as never);
            return (await attachAgent({ studioId: null, draft: () => ({}), opts: o })).session !== undefined;
          }}
        />,
      ),
    );
    await settle();
    expect(placeSelect().selectedOptions[0].text).toBe("新しいスペース");
    await pickImage("sdxl");
    await click(goBtn());
    await settle();
    expect(attached[0]).toMatchObject({ dir: "", space: { folder: "" }, worktree: false, subdir: "" });
    const s = sessionPost()!;
    expect(s).toMatchObject({ dir: "", space: { name: "" }, studio: "st9" });
    for (const k of ["worktree", "branch", "new_branch", "subdir"]) expect(s).not.toHaveProperty(k);
    expect(studioPost()).toMatchObject({ title: "新しいスペース · Claude Code" });
    expect(JSON.parse(localStorage.getItem(attachLastKey("t1")) || "null")).toMatchObject({ repo: "", space: true });
  });

  it("starts in an existing space by its folder and titles the studio with its name", async () => {
    spacesAnswer = { spaces: [{ folder: "s-abc", name: "調べもの", dir: "/home/dev/spaces/s-abc", createdAt: "2026-11-01T00:00:00Z" }] };
    await renderOpen();
    await choose("調べもの");
    await pickImage("sdxl");
    await click(goBtn());
    await settle();
    const s = sessionPost()!;
    expect(s).toMatchObject({ dir: "", space: { folder: "s-abc" } });
    expect(s).not.toHaveProperty("worktree");
    expect(studioPost()).toMatchObject({ title: "調べもの · Claude Code" });
  });

  it("does not start in a space an agent is already running in", async () => {
    spacesAnswer = { spaces: [{ folder: "s-abc", name: "調べもの", dir: "/home/dev/spaces/s-abc" }] };
    sessionRows = [{ name: "s1", kind: "claude", space: "s-abc", alive: true }];
    await renderOpen();
    await choose("調べもの");
    await pickImage("sdxl");
    expect(goBtn().disabled).toBe(true);
    expect(document.body.textContent).toContain("すでにエージェントが動いて");
    await choose("新しいスペース");
    expect(goBtn().disabled).toBe(false);
  });

  it("offers no space when the Agent has none, and a member without working copies starts at home", async () => {
    spacesAnswer = { error: { code: "http_404" } };
    useReposStore.setState({ repos: [] });
    await renderOpen();
    expect([...placeSelect().options].some((o) => o.text.includes("スペース"))).toBe(false);
    expect(placeSelect().selectedOptions[0].text).toContain("ホーム");
    await pickImage("sdxl");
    await click(goBtn());
    await settle();
    expect(sessionPost()).toMatchObject({ dir: "" });
    expect(sessionPost()).not.toHaveProperty("space");
  });

  it("holds the start until the Agent has said it supports spaces", async () => {
    spacesAnswer = { error: { code: "boom" } };
    useReposStore.setState({ repos: [] });
    await renderOpen();
    await pickImage("sdxl");
    expect(goBtn().disabled).toBe(true);
    expect(document.body.textContent).toContain("もう一度確かめる");
  });

  it("from a working copy's row, 'somewhere else' can be a space: no directory, no worktree, and the row's choice is not remembered", async () => {
    localStorage.setItem(
      attachLastKey("t1"),
      JSON.stringify({ driver: "tui", kind: "claude", model: "", effort: "", repo: "", space: true, worktree: true, imageProvider: "comfy", imageModel: "sdxl" }),
    );
    await render(BASE);
    await settle();
    await click(document.querySelector<HTMLButtonElement>(".igen-attach-last .ui-btn")!); // unfold the summary
    await click(document.querySelector<HTMLButtonElement>(".igen-attach-place .ui-btn")!);
    await choose("新しいスペース");
    await pickImage("sdxl");
    await click(goBtn());
    await settle();
    const s = sessionPost()!;
    expect(s).toMatchObject({ dir: "", space: { name: "" } });
    for (const k of ["worktree", "branch", "new_branch", "subdir"]) expect(s).not.toHaveProperty(k);
    expect(JSON.parse(localStorage.getItem(attachLastKey("t1")) || "null")).toMatchObject({ repo: "", space: true });
  });

  it("a start from the row's own place leaves a remembered space as it was", async () => {
    localStorage.setItem(
      attachLastKey("t1"),
      JSON.stringify({ driver: "tui", kind: "claude", model: "", effort: "", repo: "", space: true, worktree: true, imageProvider: "comfy", imageModel: "sdxl" }),
    );
    await render(BASE);
    await settle();
    await click(startBtn());
    await settle();
    expect(sessionPost()).toMatchObject({ dir: BASE.path });
    expect(JSON.parse(localStorage.getItem(attachLastKey("t1")) || "null")).toMatchObject({ repo: "", space: true });
  });

  it("will not turn a remembered space into a home start when the Agent has no spaces", async () => {
    spacesAnswer = { error: { code: "http_404" } };
    useReposStore.setState({ repos: [] });
    localStorage.setItem(
      attachLastKey("t1"),
      JSON.stringify({ driver: "tui", kind: "claude", model: "", effort: "", repo: "", space: true, worktree: true, imageProvider: "comfy", imageModel: "sdxl" }),
    );
    await renderOpen();
    expect(summary()).toBeNull(); // unfolded, with the reason
    expect(document.body.textContent).toContain("スペースでは始められません");
    expect(goBtn().disabled).toBe(true);
    await click(goBtn());
    await settle();
    expect(sessionPost()).toBeUndefined();
    // Home stays one explicit pick away.
    await choose("ホーム");
    expect(goBtn().disabled).toBe(false);
  });

  it("stops sending a place the Agent refused at once, before the lists are read again", async () => {
    for (const code of ["space_not_found", "space_busy"]) {
      posts.length = 0;
      spacesAnswer = { spaces: [{ folder: "s-abc", name: "調べもの", dir: "/home/dev/spaces/s-abc" }] };
      await renderOpen();
      await choose("調べもの");
      await pickImage("sdxl");
      sessionReply = { error: { code, message: "refused" } };
      // The re-read is still on the wire when the member presses again.
      spacesAnswer = new Promise(() => {});
      refreshSessions.mockClear();
      await click(goBtn());
      await settle();
      expect(refreshSessions).toHaveBeenCalled();
      expect(goBtn().disabled).toBe(true);
      expect(document.body.textContent).toContain("断りました");
      const sent = posts.filter((p) => p.path === "api/sessions").length;
      await click(goBtn());
      expect(posts.filter((p) => p.path === "api/sessions").length).toBe(sent);
      // Another place frees the button; the refused one does not come back by itself.
      spacesAnswer = { spaces: [{ folder: "s-abc", name: "調べもの", dir: "/home/dev/spaces/s-abc" }] };
      sessionReply = null;
      await choose("新しいスペース");
      expect(goBtn().disabled).toBe(false);
      await act(async () => root.unmount());
      host.remove();
      useSpacesStore.getState().clear();
    }
  });

  it("keeps a plain failure retryable: only a refused space blocks the same place", async () => {
    spacesAnswer = { spaces: [{ folder: "s-abc", name: "調べもの", dir: "/home/dev/spaces/s-abc" }] };
    await renderOpen();
    await choose("調べもの");
    await pickImage("sdxl");
    sessionReply = { error: { code: "runtime_failed", message: "boom" } };
    await click(goBtn());
    await settle();
    expect(goBtn().disabled).toBe(false);
  });

  it("does not turn a place the member picked into home when a late 404 arrives", async () => {
    let answer!: (v: unknown) => void;
    spacesAnswer = new Promise((r) => (answer = r));
    useReposStore.setState({ repos: [] });
    // A list cached from this workspace before the probe answers.
    useSpacesStore.setState({ spaces: [{ folder: "s-abc", name: "調べもの", dir: "/home/dev/spaces/s-abc" }], owner: "t1\n" });
    await renderOpen();
    // While the check is out only the dialog's own default is offered, not the cached spaces.
    expect([...placeSelect().options].some((o) => o.text.includes("調べもの"))).toBe(false);
    await choose("新しいスペース");
    answer({ error: { code: "http_404" } });
    await settle();
    expect(placeSelect().selectedOptions[0].text).toBe("新しいスペース");
    await pickImage("sdxl");
    expect(goBtn().disabled).toBe(true);
  });

  it("offers the recheck when the check failed, even when last time's place was a working copy (folded summary)", async () => {
    spacesAnswer = { error: { code: "boom" } };
    localStorage.setItem(
      attachLastKey("t1"),
      JSON.stringify({ driver: "tui", kind: "claude", model: "", effort: "", repo: "app", worktree: true, imageProvider: "comfy", imageModel: "sdxl" }),
    );
    await render(BASE);
    await settle();
    expect(summary()).not.toBeNull(); // still folded
    expect(document.body.textContent).toContain("もう一度確かめる");
  });

  it("offers the recheck when the Agent's answer failed, even with a working copy or home shown", async () => {
    spacesAnswer = { error: { code: "boom" } };
    await renderOpen();
    expect(placeSelect().selectedOptions[0].text).toContain("ホーム");
    expect(document.body.textContent).toContain("もう一度確かめる");
    spacesAnswer = { spaces: [] };
    await click(byText("もう一度確かめる"));
    await settle();
    expect([...placeSelect().options].some((o) => o.text === "新しいスペース")).toBe(true);
  });
});
