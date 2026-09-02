// コマンドパレットのセッション欄（Ctrl/⌘+P で最初に出る面）。押さえるのは 3 点で、
// どれも壊れても画面は出るぶん気づきにくい:
//
// ①**開いた瞬間の面がセッション欄で、並びが「最後に入力待ちになった順 → 稼働中 → 停止中」**。
//   ここが崩れると、Ctrl+P の一番上が「今すぐ答えるべきセッション」でなくなる。
// ②**開いている間は並びが動かない**。一覧は 4 秒ごとに更新されるので、選択（添字）の下で
//   行が入れ替わると Enter が別のセッションを開く。バッジだけは生で追随する。
// ③**行にレポ名・WT 名・状態バッジが出る**。どれが欠けても「どのセッションか」を決められない。
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
// 一覧はストアへ直接積むので、ネットワークは「何も返さない」で十分（開いた直後の
// repos リフレッシュだけが飛ぶ）。
const fetchMock = vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
vi.stubGlobal("fetch", fetchMock);
window.fetch = fetchMock as unknown as typeof window.fetch;

import { CommandPalette } from "./CommandPalette.tsx";
import { useKeysStore } from "./store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useNotificationStore } from "../notifications/store.ts";
import { useReposStore } from "../repos/store.ts";
import { resetWaitingLedgerForTest } from "../sessions/waiting.ts";
import type { Session } from "../../types/session.ts";

const session = (name: string, extra: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  alive: true,
  title: name,
  repo: "webshop",
  createdAt: "2026-09-01T00:00:00Z",
  ...extra,
});

const askedAt = (id: string, createdAt: string) => ({
  seq: 1,
  id,
  kind: "question",
  target: { type: "session", id },
  displayName: id,
  payload: {},
  createdAt,
  seen: false,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const titles = () => [...document.querySelectorAll(".cp-item .cp-title")].map((el) => el.textContent);

function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<CommandPalette />));
}

beforeEach(() => {
  values.clear();
  resetWaitingLedgerForTest();
  useReposStore.setState({
    repos: [
      { name: "webshop", branch: "main" },
      { name: "webshop@checkout", branch: "feat/checkout", worktree: true, parent: "webshop" },
    ],
  });
  useNotificationStore.setState({
    items: [askedAt("askedFirst", "2026-09-01T10:00:00Z"), askedAt("askedLast", "2026-09-01T11:00:00Z")],
  });
  useSessionsStore.setState({
    sessions: [
      session("stopped", { alive: false }),
      session("askedFirst", { state: "question" }),
      session("busy", { state: "working", repo: "webshop@checkout", worktree: true }),
      session("askedLast", { state: "permission" }),
    ],
  });
  act(() => useKeysStore.getState().openPalette());
});

afterEach(() => {
  act(() => {
    root?.unmount();
  });
  host?.remove();
  root = null;
  host = null;
  act(() => useKeysStore.getState().closePalette());
});

describe("command palette — sessions mode", () => {
  it("opens on the sessions tab, newest 入力待ち first and stopped at the foot", () => {
    mount();
    // 最初のモードタブがセッションで、それが選ばれている。
    const tabs = [...document.querySelectorAll(".cp-mode")];
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
    expect(titles()).toEqual(["askedLast", "askedFirst", "busy", "stopped"]);
  });

  it("shows the working copy, the worktree and the state chip on a row", () => {
    mount();
    const rows = [...document.querySelectorAll<HTMLElement>(".cp-item.cp-sess")];
    const busy = rows.find((r) => r.querySelector(".cp-title")?.textContent === "busy")!;
    expect(busy.querySelector(".cp-sess-repo")?.textContent).toBe("webshop");
    expect(busy.querySelector(".cp-sess-wt")?.textContent).toBe("checkout");
    expect(busy.querySelector(".session-state")?.textContent?.trim()).toBeTruthy();
    expect(busy.querySelector(".sess-kic")?.className).toContain("kind-claude");
    // 素のクローンで走っているセッションには WT の欄が出ない（出すと嘘になる）。
    const stopped = rows.find((r) => r.querySelector(".cp-title")?.textContent === "stopped")!;
    expect(stopped.querySelector(".cp-sess-wt")).toBeNull();
    expect(stopped.className).toContain("cp-stopped");
  });

  it("still orders by attention when the list only arrives after it opened", () => {
    // 起動直後（ポーリング前）にパレットを開いた場合。一覧が空なのでコマンド欄で開き、
    // セッションは後から届く。順序の材料が無いからといって名前順で固めてしまうと、
    // 注目度順という約束がその 1 回だけ静かに破れる。
    const list = useSessionsStore.getState().sessions;
    act(() => useSessionsStore.setState({ sessions: [] }));
    mount();
    act(() => {
      useSessionsStore.getState().applyList(list);
    });
    const sessionsTab = document.querySelector<HTMLElement>(".cp-mode")!;
    act(() => {
      sessionsTab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
    });
    expect(titles()).toEqual(["askedLast", "askedFirst", "busy", "stopped"]);
  });

  it("keeps the order frozen while open, but lets the badges follow the live list", () => {
    mount();
    act(() => {
      useSessionsStore.getState().applyList([
        session("stopped", { alive: false }),
        session("askedFirst", { state: "question" }),
        session("busy", { state: "question", repo: "webshop@checkout", worktree: true }), // 開いている間に質問した
        session("askedLast", { state: "permission" }),
      ]);
    });
    // 並びは動かない（カーソルの下で行が入れ替わらない）…
    expect(titles()).toEqual(["askedLast", "askedFirst", "busy", "stopped"]);
    // …が、バッジは新しい状態を映す。
    const busy = [...document.querySelectorAll<HTMLElement>(".cp-item.cp-sess")].find(
      (r) => r.querySelector(".cp-title")?.textContent === "busy",
    )!;
    expect(busy.querySelector(".session-state")?.className).toContain("question");
  });
});
