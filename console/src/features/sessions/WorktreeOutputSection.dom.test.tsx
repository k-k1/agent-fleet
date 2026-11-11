// The build-output stage of the cleanup modal. What a screenshot cannot show, and what makes
// an irreversible delete safe to put behind a button: no real POST without a dry run and a
// confirm, no second POST while one is in flight, and each Agent refusal said as itself.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Call {
  url: string;
  method: string;
}
let calls: Call[] = [];
let survey: unknown = null;
let surveyStatus = 200;
// Answers for the POSTs, by dry-run or real. A function so a test can hold one open.
let postAnswer: (dry: boolean) => Promise<{ status: number; body: unknown }>;

const fetchMock = vi.fn(async (url: string, opts?: RequestInit) => {
  const u = String(url);
  const method = String(opts?.method || "GET");
  calls.push({ url: u, method });
  let status = surveyStatus;
  let body: unknown = survey;
  if (method === "POST") {
    const a = await postAnswer(u.includes("dry_run=1"));
    status = a.status;
    body = a.body;
  }
  return {
    ok: status < 400,
    status,
    statusText: "",
    headers: { get: () => null },
    text: async () => (typeof body === "string" ? body : JSON.stringify(body)),
    json: async () => body,
  } as unknown as Response;
});
vi.stubGlobal("fetch", fetchMock);

const { WorktreeOutputSection } = await import("./WorktreeOutputSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { setLocale } = await import("../../lib/i18n/index.ts");

let host: HTMLDivElement;
let root: Root;
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

const GiB = 2 ** 30;
const cand = (name: string, over: Record<string, unknown> = {}) => ({
  name,
  path: "/home/dev/repos/" + name,
  status: "candidate",
  dirs: [
    { rel: "node_modules", bytes: 2 * GiB },
    { rel: "target", bytes: GiB, link: true },
  ],
  bytes: 3 * GiB,
  ...over,
});
const refused = (name: string, reason: string, detail = "") => ({
  name,
  path: "/home/dev/repos/" + name,
  status: "refused",
  reason,
  detail,
  bytes: 0,
});

const posts = () => calls.filter((c) => c.method === "POST");
const realPosts = () => posts().filter((c) => !c.url.includes("dry_run=1"));
const text = () => host.textContent || "";
const btn = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(label));
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const click = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await new Promise((r) => setTimeout(r, 0));
  });
};

const render = async () => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(
      <ToastProvider>
        <ConfirmProvider>
          <WorktreeOutputSection />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
};

const openSurvey = async () => {
  await render();
  await click(btn("調べる"));
  await settle();
};

// The confirm dialog's buttons live in a portal on the body.
const confirmBtn = () => btn("完全に削除");
const cancelBtn = () => btn("キャンセル");

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  setLocale("ja");
  calls = [];
  surveyStatus = 200;
  survey = { dormant_days: 30, worktrees: [cand("web@a"), refused("web@b", "dirty")], measured_at: "t" };
  postAnswer = async (dry) => ({ status: 200, body: dry ? cand("web@a") : { ...cand("web@a"), dirs: undefined, removed: cand("web@a").dirs } });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  document.body.innerHTML = "";
});

describe("WorktreeOutputSection", () => {
  it("does not touch the Agent until asked, then lists candidates with sizes and the grade", async () => {
    await render();
    expect(calls).toEqual([]);
    await click(btn("調べる"));
    await settle();
    expect(calls.map((c) => c.method + " " + c.url.replace(/^.*\/api\//, ""))).toEqual(["GET cleanup/worktree-output"]);
    const row = host.querySelector('[data-name="web@a"]')!;
    expect(row.textContent).toContain("再生成可");
    expect(row.textContent).toContain("node_modules");
    expect(row.textContent).toContain("3.0 GB");
    // A candidate offers a preview, never a delete yet.
    expect(btn("プレビュー")).toBeTruthy();
    expect(btn("ビルド成果物を削除…")).toBeFalsy();
  });

  it("says each refusal as its own reason, behind a toggle", async () => {
    await openSurvey();
    expect(text()).not.toContain("未コミット・未追跡");
    await click(btn("対象外の作業コピー 1 件"));
    const row = host.querySelector('[data-name="web@b"]')!;
    expect(row.textContent).toContain("未コミット・未追跡の変更があります");
    expect(row.textContent).not.toContain("再生成可");
    expect(row.querySelector("button")).toBeNull();
  });

  it("names the sessions of a live_session refusal and falls back on a reason it does not know", async () => {
    survey = {
      dormant_days: 30,
      worktrees: [refused("web@c", "live_session", "sess-1, sess-2"), refused("web@d", "from_the_future")],
      measured_at: "t",
    };
    await openSurvey();
    await click(btn("対象外の作業コピー 2 件"));
    expect(host.querySelector('[data-name="web@c"]')!.textContent).toContain("セッションが動いています。 (sess-1, sess-2)");
    expect(host.querySelector('[data-name="web@d"]')!.textContent).toContain("この Console が知らない理由");
  });

  it("previews with dry_run=1 only, and shows what would go and that it cannot be undone", async () => {
    await openSurvey();
    await click(btn("プレビュー"));
    expect(posts()).toHaveLength(1);
    expect(posts()[0].url).toContain("worktree=web%40a&dry_run=1");
    expect(realPosts()).toHaveLength(0);
    expect(text()).toContain("元に戻せません");
    expect(text()).toContain("リンクだけを消します");
    expect(btn("ビルド成果物を削除…")).toBeTruthy();
  });

  it("sends the real POST only after the confirm, and not when the confirm is cancelled", async () => {
    await openSurvey();
    await click(btn("プレビュー"));
    await click(btn("ビルド成果物を削除…"));
    expect(realPosts()).toHaveLength(0); // the confirm is up, nothing deleted
    expect(document.body.textContent).toContain("完全に削除");
    expect(document.body.textContent).toContain("ごみ箱には入らず、元に戻せません");
    await click(cancelBtn());
    expect(realPosts()).toHaveLength(0);

    await click(btn("ビルド成果物を削除…"));
    await click(confirmBtn());
    expect(realPosts()).toHaveLength(1);
    expect(realPosts()[0].url).toMatch(/worktree=web%40a$/);
    expect(text()).toContain("web@a のビルド成果物 3.0 GB を削除しました");
    expect(btn("ビルド成果物を削除…")).toBeFalsy(); // done: no second delete from a spent plan
  });

  it("cannot be double-submitted while the delete is in flight", async () => {
    await openSurvey();
    await click(btn("プレビュー"));
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    postAnswer = async () => {
      await held;
      return { status: 200, body: { ...cand("web@a"), removed: cand("web@a").dirs } };
    };
    await click(btn("ビルド成果物を削除…"));
    await click(confirmBtn());
    expect(realPosts()).toHaveLength(1);
    // Everything that could start another request is disabled while it runs.
    expect(btn("削除しています…")!.disabled).toBe(true);
    expect(btn("もう一度調べる")!.disabled).toBe(true);
    await click(btn("削除しています…"));
    expect(realPosts()).toHaveLength(1);
    await act(async () => {
      release();
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(realPosts()).toHaveLength(1);
    expect(text()).toContain("を削除しました");
  });

  it("shows a refusal on preview and on the real delete as the Agent's reason, with nothing deleted", async () => {
    postAnswer = async () => ({ status: 409, body: refused("web@a", "live_session", "s9") });
    await openSurvey();
    await click(btn("プレビュー"));
    expect(text()).toContain("セッションが動いています。 (s9)");
    expect(btn("ビルド成果物を削除…")).toBeFalsy();

    // The plan was fine, then a session started before the real delete: 409 with the reason.
    await click(btn("もう一度調べる"));
    postAnswer = async (dry) =>
      dry ? { status: 200, body: cand("web@a") } : { status: 409, body: refused("web@a", "in_use", "node_modules: in_use") };
    await click(btn("プレビュー"));
    await click(btn("ビルド成果物を削除…"));
    await click(confirmBtn());
    expect(realPosts()).toHaveLength(1);
    expect(text()).toContain("何も削除していません");
    expect(text()).toContain("いま動いているプロセスがビルド成果物を使っています");
    expect(text()).not.toContain("リクエストに失敗");
  });

  it("reports a partial delete's skipped directories", async () => {
    postAnswer = async (dry) =>
      dry
        ? { status: 200, body: cand("web@a") }
        : { status: 200, body: { ...cand("web@a"), dirs: undefined, bytes: 2 * GiB, removed: [cand("web@a").dirs[0]], skipped: ["target: in_use"] } };
    await openSurvey();
    await click(btn("プレビュー"));
    await click(btn("ビルド成果物を削除…"));
    await click(confirmBtn());
    expect(text()).toContain("2.0 GB を削除しました");
    expect(text()).toContain("残したもの: target: in_use");
  });

  it("does not offer a retry of a delete whose answer was lost", async () => {
    await openSurvey();
    await click(btn("プレビュー"));
    postAnswer = async (dry) => (dry ? { status: 200, body: cand("web@a") } : { status: 502, body: "bad gateway" });
    await click(btn("ビルド成果物を削除…"));
    await click(confirmBtn());
    expect(text()).toContain("削除されたかどうか分かりません");
    expect(btn("ビルド成果物を削除…")).toBeFalsy();
    expect(realPosts()).toHaveLength(1);
  });

  it("calls a 4xx refusal of the delete a definite no-op, not an unknown outcome", async () => {
    await openSurvey();
    await click(btn("プレビュー"));
    postAnswer = async (dry) =>
      dry ? { status: 200, body: cand("web@a") } : { status: 404, body: { error: { code: "not_found", message: "no such working copy" } } };
    await click(btn("ビルド成果物を削除…"));
    await click(confirmBtn());
    expect(text()).toContain("何も削除していません");
    expect(text()).not.toContain("分かりません");
  });

  it("starts one preview for two clicks in the same tick (the ref lock, not the disabled state)", async () => {
    await openSurvey();
    const b = btn("プレビュー")!;
    await act(async () => {
      b.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      b.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(posts()).toHaveLength(1);
  });

  it("says plainly when the Agent has no such endpoint", async () => {
    surveyStatus = 404;
    survey = "404 page not found";
    await openSurvey();
    expect(text()).toContain("この Agent は古くて");
  });
});
