// 作業項目レール（docs/80 P0）の描画テスト。芯は 3 つ:
//   ① Workspace 停止中でもキャッシュの行が出て、「最終取得」と停止中の断りが付く
//      —— この画面が使えないなら機能そのものが無い（ADR 0061 決定 1）
//   ② 着手済みの行にバッジが出る（同じ課題に 2 人目が入るのを起動前に止める）
//   ③ 始める が既存の起動スタックへ種（プロンプト・タイトル・ブランチ）を渡す
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../lib/i18n/index.ts";

const workItemList = vi.fn();
vi.mock("./api.ts", () => ({
  workItemList: (...a: unknown[]) => workItemList(...a),
  workItemRefresh: vi.fn(async () => ({ items: [], queries: [] })),
  workItemQueryCreate: vi.fn(),
  workItemQueryUpdate: vi.fn(),
  workItemQueryDelete: vi.fn(),
  workItemSessionCreate: vi.fn(),
  workItemSessionDelete: vi.fn(),
}));

const { WorkItemsSection } = await import("./WorkItemsSection.tsx");
const { useWorkItemStore } = await import("./store.ts");
const { useLaunchSeed, useLaunchTarget, useReposStore } = await import("../repos/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");

const item = (over: Record<string, unknown> = {}) => ({
  id: "1",
  queryId: "q1",
  provider: "github",
  kind: "issue",
  key: "acme/web#45",
  title: "ログイン後に一覧が空になる",
  state: "open",
  url: "https://github.com/acme/web/issues/45",
  assignee: "taro",
  labels: ["bug"],
  repo: "acme/web",
  updatedAt: "2026-08-26T00:00:00Z",
  ...over,
});

const query = { id: "q1", provider: "github", label: "自分の未完了", query: "assignee:@me", repoHint: "", enabled: true, position: 0, fetchedAt: "2026-08-26T09:00:00Z", lastError: "" };

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <WorkItemsSection />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const text = () => host.textContent || "";
const rows = () => host.querySelectorAll(".wi-row").length;

beforeEach(() => {
  workItemList.mockReset();
  useWorkItemStore.getState().reset();
  useLaunchSeed.getState().clear();
  useLaunchTarget.getState().clear();
  useReposStore.setState({ repos: [] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("WorkItemsSection", () => {
  it("★ 停止中でもキャッシュの行を出し、最終取得と停止中の断りを添える", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "2026-08-26T09:00:00Z", running: false });
    await render();
    expect(rows()).toBe(1);
    expect(text()).toContain("ログイン後に一覧が空になる");
    expect(text()).toContain(t("wi.stopped_note"));
    // 「いつ取ったか」を言わずに古い一覧を出さない。
    expect(host.querySelector(".wi-stamp")?.textContent || "").not.toBe("");
  });

  it("取得失敗は「空」と別物として出し、直前の行を残す", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    expect(rows()).toBe(1);
    workItemList.mockResolvedValue({ error: { code: "boom", message: "nope" } });
    await act(async () => {
      await useWorkItemStore.getState().refresh();
    });
    expect(host.querySelector(".wi-err")).not.toBeNull();
    expect(rows()).toBe(1); // 行は消えない
    expect(text()).not.toContain(t("wi.empty"));
  });

  it("クエリが 1 本も無いときは「空」ではなく追加導線を出す", async () => {
    workItemList.mockResolvedValue({ items: [], queries: [], sessions: [], fetchedAt: "", running: true });
    await render();
    expect(text()).toContain(t("wi.no_queries"));
    expect(text()).not.toContain(t("wi.empty"));
  });

  it("クエリ単位の失敗はそのクエリの名前で出る", async () => {
    workItemList.mockResolvedValue({
      items: [item()],
      queries: [{ ...query, lastError: "github rejected the token" }],
      sessions: [],
      fetchedAt: "2026-08-26T09:00:00Z",
      running: true,
    });
    await render();
    expect(text()).toContain(t("wi.query_failed", { label: "自分の未完了" }));
    expect(rows()).toBe(1); // 失敗しても他の行は出たまま
  });

  it("★ 着手済みの行にはバッジが出る", async () => {
    workItemList.mockResolvedValue({
      items: [item(), item({ id: "2", key: "acme/web#46", title: "まだ誰も見ていない" })],
      queries: [query],
      sessions: [{ id: "l1", provider: "github", itemKey: "acme/web#45", sessionName: "sk7f3q9", repo: "web", branch: "", createdAt: "" }],
      fetchedAt: "2026-08-26T09:00:00Z",
      running: true,
    });
    await render();
    const badges = host.querySelectorAll(".wi-started");
    expect(badges.length).toBe(1);
    expect(badges[0].getAttribute("title")).toContain("sk7f3q9");
  });

  it("★ 始める が起動スタックへ種を渡す（作業コピーが分かれば LaunchModal 直行）", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".wi-start")!.click();
    });
    const seed = useLaunchSeed.getState();
    expect(seed.prompt).toContain("acme/web#45");
    expect(seed.prompt).toContain("gh issue view 45");
    // 既定では本文を貼らない（引用ブロックが無い）。
    expect(seed.prompt).not.toContain(">");
    expect(seed.title).toContain("#45");
    expect(seed.workItem).toEqual({ provider: "github", key: "acme/web#45", branch: "feature/issue-45" });
    // repo が特定できたので、ハブではなく直接その作業コピーの起動ダイアログへ。
    expect(useLaunchTarget.getState().target?.name).toBe("web");
  });

  it("作業コピーが分からないときは はじめる ハブに委ねる", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".wi-start")!.click();
    });
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(useLaunchSeed.getState().workItem?.key).toBe("acme/web#45");
  });
});
