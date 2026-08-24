// 差し出し側のゲート（docs/77 §77.5 / ADR 0057 決定 5）。
//
// ここで押さえるのは「push していない引き継ぎを送らせない」こと。相手のディスクに所有者の
// commit は無いので、push されていない引き継ぎは文章がどれだけ立派でも嘘になる —— しかも
// **一度も push していないブランチの ahead は 0** なので、素朴な実装ほど素通しする。
//
// 判定そのものは Agent が組み立てて CP がそのまま中継する。この面が守るのは「その判定を
// 表示し、送信を止める」までで、条件を画面側で組み直さないこと自体がテスト対象である。
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn(async (..._args: unknown[]) => ({}) as Record<string, unknown>);
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => api(path),
  apiJSON: (path: string, method: string, body: unknown) => apiJSON(path, method, body),
  errText: (e: unknown) => String((e as { message?: string })?.message ?? e),
  getTenant: () => "",
}));
// 共有作成モーダルは別機能。ここでは「行き止まりにしない導線がある」ことだけ見る。
vi.mock("./ShareCreateModal.tsx", () => ({ ShareCreateModal: () => <div data-share-modal /> }));

import { HandoffOfferModal } from "./HandoffOfferModal.tsx";
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function render(payload: unknown) {
  api.mockReset();
  api.mockImplementation(async () => payload);
  host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host!);
    root.render(
      <ToastProvider>
        <HandoffOfferModal session="s1" initialTitle="続き" initialPrompt="ここから続けて" onClose={() => {}} />
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

// ⚠️ Modal はポータルで document.body 直下に出る。host を見ると常に空で、テストが
// 「ボタンが無い」ではなく undefined を比べて落ちる（最初にここで 5 件落ちた）。
function sendButton(): HTMLButtonElement | undefined {
  return [...document.querySelectorAll("button")].find((b) => b.getAttribute("type") === "submit") as
    | HTMLButtonElement
    | undefined;
}
function find(sel: string): Element | null {
  return document.querySelector(sel);
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const MEMBERS = [{ userKey: "b-example-com", email: "b@example.com" }];

describe("HandoffOfferModal", () => {
  it("push 済みで clean なら送れる", async () => {
    await render({ members: MEMBERS, context: { vcs: "git", branch: "main", headSha: "abcdef1234", remote: "https://x/y.git", ahead: 0 } });
    expect(sendButton()?.disabled).toBe(false);
  });

  it("一度も push していないブランチは送れない（ahead=0 でも止まる）", async () => {
    await render({
      members: MEMBERS,
      context: { vcs: "git", branch: "temp/x", ahead: 0, noUpstream: true, blocked: "no_upstream" },
    });
    expect(sendButton()?.disabled).toBe(true);
    expect(find(".handoff-blocked")).toBeTruthy();
  });

  it("未 push の commit があると送れない", async () => {
    await render({ members: MEMBERS, context: { vcs: "git", branch: "main", ahead: 2, blocked: "unpushed_commits" } });
    expect(sendButton()?.disabled).toBe(true);
  });

  it("未コミットは止めず、承知のチェックで送れるようになる", async () => {
    await render({ members: MEMBERS, context: { vcs: "git", branch: "main", ahead: 0, dirty: true, warning: "uncommitted_changes" } });
    expect(sendButton()?.disabled).toBe(true);
    const ack = find('input[type="checkbox"]') as HTMLInputElement;
    expect(ack).toBeTruthy();
    await act(async () => {
      ack.click();
    });
    expect(sendButton()?.disabled).toBe(false);
  });

  it("共有していないセッションは行き止まりにせず共有導線を出す", async () => {
    await render({ members: [], context: { vcs: "git", branch: "main", ahead: 0 } });
    expect(sendButton()?.disabled).toBe(true);
    const labels = [...document.querySelectorAll("button")].map((b) => b.textContent || "");
    expect(labels.some((t) => t.length > 0)).toBe(true);
    expect(find(".handoff-blocked")).toBeTruthy();
  });
});
