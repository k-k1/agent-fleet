// 作業項目レール（docs/80 P0）の描画テスト。芯は 4 つ:
//   ① Workspace 停止中でもキャッシュの行が出て、「最終取得」と停止中の断りが付く
//      —— この画面が使えないなら機能そのものが無い（ADR 0061 決定 1）
//   ② 着手済みの行にバッジが出る（同じ課題に 2 人目が入るのを起動前に止める）
//   ③ 行にボタンを並べない（§80.20）。行を押すと詳細が開き、操作はそこに集まる
//   ④ 詳細の 始める が既存の起動スタックへ種（プロンプト・タイトル・ブランチ）を渡す
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
/** 行を押す＝詳細を開く（§80.20 でボタンから置き換わった導線）。 */
const openRow = async (n = 0) => {
  await act(async () => {
    host.querySelectorAll<HTMLElement>(".wi-row")[n].click();
  });
};
/** 詳細モーダルの footer 右端＝始める。 */
const detailStart = () =>
  [...document.querySelectorAll<HTMLButtonElement>(".wi-dmodal .ui-modal-foot button")].pop()!;

// 制御された input に打つ。★ `el.value = x` だけでは React の value tracker が
// 「変わっていない」と見なして onChange が鳴らない（絞り込みが効いていないのに
// テストは緑、という一番いらない形になる）ので、プロトタイプ側の setter を使う。
const typeInto = (el: HTMLInputElement, value: string) => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
};

// ui-modal の直下に居てよいのは shell の 3 つだけ。それ以外がここに出るということは、
// padding を持つ器の外に中身が落ちているということ。
const strayChildren = (panel: Element) =>
  [...panel.children]
    .filter((el) => !el.matches(".ui-modal-head, .ui-modal-body, .ui-modal-foot"))
    .map((el) => el.className);

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

  // --- §80.20: 行はボタンを並べない。押すと詳細が開き、操作はそこに集まる ---

  it("★ 行に「始める」ボタンを並べない（41 行 × 1 ボタンをやめた）", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({ items: jiraRows(5), queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    expect(host.querySelectorAll(".wi-start").length).toBe(0);
    expect(host.querySelectorAll(".wi-report").length).toBe(0);
    // 行に残るのは 🔗 と（着手済みなら）バッジだけ。行そのものが押せる。
    expect(host.querySelectorAll(".wi-link").length).toBe(5);
    expect(host.querySelector(".wi-row")?.getAttribute("role")).toBe("button");
  });

  it("★ 行を押すと詳細が開き、CP が持っている項目がそのまま並ぶ（本文は取りに行かない）", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await openRow();
    const modal = document.querySelector(".wi-dmodal")!;
    const shown = modal.textContent || "";
    expect(shown).toContain("ログイン後に一覧が空になる");
    expect(shown).toContain("acme/web#45");
    expect(shown).toContain(t("wi.state_open"));
    expect(shown).toContain("@taro");
    expect(shown).toContain("bug");
    expect(modal.querySelector<HTMLAnchorElement>(".wi-dlink")?.href).toBe("https://github.com/acme/web/issues/45");
  });

  it("★ 行の 🔗 と着手済みバッジは詳細を開かない（入れ子の操作要素）", async () => {
    workItemList.mockResolvedValue({
      items: [item()],
      queries: [query],
      sessions: [{ id: "l1", provider: "github", itemKey: "acme/web#45", sessionName: "sk7f3q9", repo: "web", branch: "", createdAt: "" }],
      fetchedAt: "",
      running: true,
    });
    await render();
    await act(async () => {
      host.querySelector<HTMLElement>(".wi-link")!.click();
    });
    expect(document.querySelector(".wi-dmodal")).toBeNull();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".wi-started")!.click();
    });
    expect(document.querySelector(".wi-dmodal")).toBeNull();
  });

  it("★ 詳細はまず起動先を聞く（チケットは作業コピーを知らない）", async () => {
    useReposStore.setState({
      repos: [
        { name: "web", path: "/home/dev/repos/web" },
        { name: "web@wip-abc", path: "/home/dev/repos/web@wip-abc", worktree: true, parent: "web", branch: "feature/issue-9" },
      ],
    });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await openRow();
    // 開いただけでは起動しない。種もまだ置かない（キャンセルされうる）。
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
    await openRow();
    await act(async () => {
      detailStart().click();
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
    await openRow();
    const where = document.querySelectorAll<HTMLSelectElement>(".wi-sfield select")[1];
    await act(async () => {
      where.value = "web@wip-abc";
      where.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      detailStart().click();
    });
    expect(useLaunchTarget.getState().target?.name).toBe("web@wip-abc");
    expect(useLaunchTarget.getState().inPlace).toBe(true);
  });

  // ★ 余白の回帰はここで止める。ui-modal 自身に padding は無く（見出しと footer が
  // 自分で持つ形）、中身を直下に置くと本文だけが枠に貼りつく —— 実機で「はじめる」が
  // その形になっていた。構造で書けば、見た目を見なくても壊れたと分かる。
  it("★ モーダルの中身は共有の ui-modal-body / ui-modal-foot に載る（他のダイアログと余白が揃う）", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();

    await openRow();
    const detail = document.querySelector(".wi-dmodal")!;
    expect(detail.querySelector(":scope > .ui-modal-body .wi-sfield")).not.toBeNull();
    expect(detail.querySelector(":scope > .ui-modal-foot button")).not.toBeNull();
    expect(strayChildren(detail)).toEqual([]);

    // 詳細は Esc / キャンセルで閉じる。歯車を開く前に畳んでおかないと 2 枚重なる。
    await act(async () => {
      [...document.querySelectorAll<HTMLButtonElement>(".wi-dmodal .ui-modal-foot button")][0].click();
    });

    // 保存したクエリ（歯車）も同じ形であること。
    const gear = [...host.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.getAttribute("aria-label") === t("wi.queries"),
    )!;
    await act(async () => gear.click());
    const queries = document.querySelector(".wi-qmodal")!;
    expect(queries.querySelector(":scope > .ui-modal-body .wi-qform")).not.toBeNull();
    expect(strayChildren(queries)).toEqual([]);
  });

  // --- docs/80 §80.18: 実データ（Jira 41 件・全行同じ担当者）で作り直した情報設計 ---

  const jiraRows = (n: number) =>
    Array.from({ length: n }, (_, i) =>
      item({
        id: "j" + i,
        queryId: "q1",
        provider: "jira",
        key: `G3M-${100 + i}`,
        title: `課題 ${i}`,
        assignee: "Rin Aoyagi",
        repo: "",
        labels: [],
        // ★ 明確に古い日付にする。「24 時間以内なら時刻を出さない」を入れたので、
        // 実行日に近い値だと走らせる日によって .wi-when の数が変わってしまう。
        updatedAt: new Date(Date.UTC(2025, 11, 1, 0, i)).toISOString(),
      }),
    );

  it("★ 41 件でも既定は 10 行。件数バッジは全件のままで、残りは数で書く", async () => {
    workItemList.mockResolvedValue({ items: jiraRows(41), queries: [query], sessions: [], fetchedAt: "2026-08-26T09:00:00Z", running: true });
    await render();
    expect(rows()).toBe(10);
    // 畳んでいるだけで隠していない —— 見出しのバッジは 41 のまま。
    expect(host.querySelector(".ui-section-count")?.textContent).toBe("41");
    const more = host.querySelector<HTMLButtonElement>(".wi-more")!;
    expect(more.textContent).toBe(t("wi.show_more", { n: 31 }));
    await act(async () => more.click());
    expect(rows()).toBe(41);
    // たたむ で戻れる（開きっぱなしにしない）。
    await act(async () => host.querySelector<HTMLButtonElement>(".wi-more")!.click());
    expect(rows()).toBe(10);
  });

  it("★ 全行が同じ担当者なら行から消え、メタが空なら 2 行目そのものを描かない", async () => {
    workItemList.mockResolvedValue({ items: jiraRows(41), queries: [query], sessions: [], fetchedAt: "2026-08-26T09:00:00Z", running: true });
    await render();
    expect(text()).not.toContain("@Rin Aoyagi");
    expect(host.querySelectorAll(".wi-meta").length).toBe(0);
    // 消したのは表示だけ —— 担当者は行の tooltip に残っている。
    expect(host.querySelector(".wi-title")?.getAttribute("title")).toContain("Rin Aoyagi");
    // 空いた高さの代わりに、並び順の理由（更新の相対時刻）が出る。
    expect(host.querySelectorAll(".wi-when").length).toBe(10);
  });

  it("担当者が割れていれば出す（チームのクエリ）", async () => {
    const mixed = jiraRows(41).map((r, i) => (i === 3 ? { ...r, assignee: "Sora Ueda" } : r));
    workItemList.mockResolvedValue({ items: mixed, queries: [query], sessions: [], fetchedAt: "2026-08-26T09:00:00Z", running: true });
    await render();
    expect(text()).toContain("@Rin Aoyagi");
  });

  it("★ レール内の絞り込みは行を減らすだけ（絞ってから畳む）", async () => {
    const mixed = [...jiraRows(40), item({ id: "gh", key: "acme/web#45", title: "ログイン後に一覧が空になる" })];
    workItemList.mockResolvedValue({ items: mixed, queries: [query], sessions: [], fetchedAt: "2026-08-26T09:00:00Z", running: true });
    await render();
    const input = host.querySelector<HTMLInputElement>(".wi-filter input")!;
    await act(async () => typeInto(input, "ログイン"));
    expect(rows()).toBe(1);
    expect(host.querySelector(".wi-more")).toBeNull(); // 1 件しか残らなければ折りたたみは要らない
    await act(async () => typeInto(input, "存在しない語"));
    expect(rows()).toBe(0);
    expect(text()).toContain(t("wi.filter_empty"));
  });

  it("混んでいないレールに検索窓を出さない", async () => {
    workItemList.mockResolvedValue({ items: jiraRows(4), queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    expect(host.querySelector(".wi-filter")).toBeNull();
    expect(host.querySelector(".wi-more")).toBeNull();
    expect(rows()).toBe(4);
  });

  it("作業コピーが 1 つも無いときだけ はじめる ハブに委ねる", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await openRow();
    // 選ばせるものが無いので選択肢は出さず、始める がそのまま clone 導線へ渡す。
    expect(document.querySelector(".wi-sfield")).toBeNull();
    await act(async () => {
      detailStart().click();
    });
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(useLaunchSeed.getState().workItem?.key).toBe("acme/web#45");
  });

  it("★ 報告は着手済みのときだけ詳細に出て、押すと下書きモーダルへ入れ替わる", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({
      items: [item(), item({ id: "2", key: "acme/web#46", title: "まだ誰も見ていない" })],
      queries: [query],
      sessions: [{ id: "l1", provider: "github", itemKey: "acme/web#45", sessionName: "sk7f3q9", repo: "web", branch: "feature/issue-45", createdAt: "" }],
      fetchedAt: "",
      running: true,
    });
    await render();
    // 着手していない行の詳細には報告も着手中セクションも出ない。
    await openRow(1);
    expect(document.querySelector(".wi-dstarted")).toBeNull();
    await act(async () => {
      [...document.querySelectorAll<HTMLButtonElement>(".wi-dmodal .ui-modal-foot button")][0].click();
    });

    await openRow(0);
    const started = document.querySelector(".wi-dstarted")!;
    expect(started.textContent).toContain("sk7f3q9");
    expect(started.textContent).toContain("feature/issue-45");
    const report = [...started.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      (b.textContent || "").includes(t("wi.report_title")),
    )!;
    await act(async () => report.click());
    // ★ 2 枚重ねない —— 詳細は閉じ、報告だけが残る。
    expect(document.querySelector(".wi-dmodal")).toBeNull();
    expect(document.querySelector(".wi-rmodal")).not.toBeNull();
  });

  it("bitbucket の項目には報告を出さない（押した先で必ず断られる操作要素を出さない）", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({
      items: [item({ provider: "bitbucket", kind: "pr", key: "acme/web#45" })],
      queries: [query],
      sessions: [{ id: "l1", provider: "bitbucket", itemKey: "acme/web#45", sessionName: "sk7f3q9", repo: "web", branch: "", createdAt: "" }],
      fetchedAt: "",
      running: true,
    });
    await render();
    await openRow();
    expect(document.querySelector(".wi-dstarted")).not.toBeNull();
    expect(document.querySelector(".wi-dmodal")?.textContent).not.toContain(t("wi.report_title"));
  });

  // --- 保存クエリの取得元（3 択なので select をやめてセグメントにした） ---

  const openQueries = async () => {
    const gear = [...host.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.getAttribute("aria-label") === t("wi.queries"),
    )!;
    await act(async () => gear.click());
    return document.querySelector(".wi-qmodal")!;
  };

  it("★ 取得元は select ではなくボタン 3 つ（畳む理由が無く、暗色で選択肢が読めなかった）", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    const modal = await openQueries();

    const segs = [...modal.querySelectorAll<HTMLButtonElement>(".wi-qform .ui-seg .seg-btn")];
    expect(segs.map((b) => b.textContent)).toEqual(["GitHub", "Jira", "Bitbucket"]);
    expect(segs.map((b) => b.getAttribute("aria-pressed"))).toEqual(["true", "false", "false"]);
    // 残ってよい select は「既定の作業コピー」の 1 つだけ（取得元は消えている）。
    expect(modal.querySelectorAll(".wi-qform select").length).toBe(1);

    await act(async () => segs[1].click());
    expect(segs.map((b) => b.getAttribute("aria-pressed"))).toEqual(["false", "true", "false"]);
    // 押した取得元の既定クエリ（JQL）に入れ替わる＝ select と同じ副作用が生きている。
    const expr = [...modal.querySelectorAll<HTMLInputElement>(".wi-qform input")].pop()!;
    expect(expr.value).toContain("currentUser()");
  });
});
