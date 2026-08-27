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

  it("★ labels が null の行でもレールが落ちない（Console が真っ白になった実バグ）", async () => {
    // CP が Go の nil スライスを JSON の null として出していた。行の
    // item.labels.slice(0, 2) が TypeError になり、アプリに ErrorBoundary が
    // 無いので Console 全体が消えた。生成側は直したが、古い CP から来ても
    // 描けることをここで固定する。
    workItemList.mockResolvedValue({
      items: [{ ...item(), labels: null }, { ...item(), id: "2", key: "acme/web#46", labels: ["bug"] }],
      queries: [query],
      sessions: [],
      fetchedAt: "2026-08-26T09:00:00Z",
      running: true,
    });
    await render();
    expect(rows()).toBe(2);
    expect(text()).toContain("ログイン後に一覧が空になる");
    expect(text()).toContain("bug");
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

  it("★ 始める はまず起動先を聞く（チケットは作業コピーを知らない）", async () => {
    useReposStore.setState({
      repos: [
        { name: "web", path: "/home/dev/repos/web" },
        { name: "web@wip-abc", path: "/home/dev/repos/web@wip-abc", worktree: true, parent: "web", branch: "feature/issue-9" },
      ],
    });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".wi-start")!.click();
    });
    // 押しただけでは起動しない。種もまだ置かない（キャンセルされうる）。
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(useLaunchSeed.getState().workItem).toBeNull();
    const selects = document.querySelectorAll<HTMLSelectElement>(".wi-sfield select");
    expect(selects.length).toBe(2);
    // 起動先には「新しい worktree」「base でそのまま」「既存の worktree」が並ぶ。
    expect(selects[1].options.length).toBe(3);
    expect(document.body.textContent || "").toContain("feature/issue-9");
  });

  it("★ 新しい worktree を選ぶと base 宛に、種つきで起動スタックへ渡る", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".wi-start")!.click();
    });
    const buttons = [...document.querySelectorAll<HTMLButtonElement>(".wi-sactions button")];
    await act(async () => {
      buttons[buttons.length - 1].click();
    });
    const seed = useLaunchSeed.getState();
    expect(seed.prompt).toContain("acme/web#45");
    expect(seed.prompt).toContain("gh issue view 45");
    expect(seed.prompt).not.toContain(">"); // 既定では本文を貼らない
    expect(seed.title).toContain("#45");
    expect(seed.workItem).toEqual({ provider: "github", key: "acme/web#45", branch: "feature/issue-45" });
    expect(useLaunchTarget.getState().target?.name).toBe("web");
    expect(useLaunchTarget.getState().inPlace).toBe(false);
  });

  it("★ 既存の作業コピーを選ぶと inPlace で渡る（起動ダイアログが新規 worktree に戻さない）", async () => {
    useReposStore.setState({
      repos: [
        { name: "web", path: "/home/dev/repos/web" },
        { name: "web@wip-abc", path: "/home/dev/repos/web@wip-abc", worktree: true, parent: "web", branch: "feature/issue-9" },
      ],
    });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".wi-start")!.click();
    });
    const where = document.querySelectorAll<HTMLSelectElement>(".wi-sfield select")[1];
    await act(async () => {
      where.value = "web@wip-abc";
      where.dispatchEvent(new Event("change", { bubbles: true }));
    });
    const buttons = [...document.querySelectorAll<HTMLButtonElement>(".wi-sactions button")];
    await act(async () => {
      buttons[buttons.length - 1].click();
    });
    expect(useLaunchTarget.getState().target?.name).toBe("web@wip-abc");
    expect(useLaunchTarget.getState().inPlace).toBe(true);
  });

  it("作業コピーが 1 つも無いときだけ はじめる ハブに委ねる", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".wi-start")!.click();
    });
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(useLaunchSeed.getState().workItem?.key).toBe("acme/web#45");
  });
});
