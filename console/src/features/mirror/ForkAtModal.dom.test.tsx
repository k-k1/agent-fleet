// 分岐確認ダイアログ。ここが壊れると分岐という操作そのものが到達不能になるので、
// 「押したら at 付きで fork を叩く」「成功したら生まれたセッション名を渡す」「失敗したら
// 閉じずに理由を出す」の3点だけを jsdom で押さえる。特に最後の1つは、閉じてしまうと
// ユーザーには「押したのに何も起きなかった」に見える。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
}));

import { ForkAtModal } from "./ForkAtModal.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const done: Array<{ name: string; draft: string }> = [];
let closed = 0;

function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <ForkAtModal
        session="oc-1"
        target={{ anchorId: "msg_7", text: "やっぱり別の方法で", carried: 3 }}
        onDone={(name, opts) => done.push({ name, draft: opts.draft })}
        onClose={() => closed++}
      />,
    ),
  );
}

// 確認ボタン = フッターの primary。文言はロケール依存なのでクラスで引く。
function goButton() {
  const el = document.querySelector<HTMLButtonElement>(".ui-modal-foot .ui-btn-primary");
  if (!el) throw new Error("confirm button not rendered");
  return el;
}

// モード切替（やり直す / 続きから）は radiogroup の 2 つ目が「続きから」。
function modeButton(mode: "redo" | "continue") {
  const els = Array.from(document.querySelectorAll<HTMLButtonElement>('[role="radiogroup"] .seg-btn'));
  if (els.length !== 2) throw new Error(`expected 2 mode buttons, got ${els.length}`);
  return mode === "redo" ? els[0] : els[1];
}

beforeEach(() => {
  apiJSON.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  done.length = 0;
  closed = 0;
});

describe("ForkAtModal", () => {
  it("shows the branch point so the user can tell WHICH message they picked", () => {
    mount();
    expect(document.querySelector(".mirror-fork-preview")?.textContent).toContain("やっぱり別の方法で");
  });

  it("defaults to redo: posts include=false and seeds the prompt as a draft", async () => {
    apiJSON.mockResolvedValue({ name: "oc-2" });
    mount();
    await act(async () => {
      goButton().click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/sessions/oc-1/fork", "POST", { at: "msg_7", include: false });
    expect(done).toEqual([{ name: "oc-2", draft: "やっぱり別の方法で" }]);
    expect(closed).toBe(1);
  });

  it("continue mode posts include=true and seeds NO draft", async () => {
    // 「続きから」ではその発言が分岐先に残っているので、入力欄にも同じ文が入ると二重に見える。
    apiJSON.mockResolvedValue({ name: "oc-3" });
    mount();
    await act(async () => {
      modeButton("continue").click();
    });
    await act(async () => {
      goButton().click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/sessions/oc-1/fork", "POST", { at: "msg_7", include: true });
    expect(done).toEqual([{ name: "oc-3", draft: "" }]);
  });

  it("keeps the dialog open and shows why when the fork is refused", async () => {
    apiJSON.mockRejectedValue({ code: "fork_bad_anchor", message: "この分岐点は使えません" });
    mount();
    await act(async () => {
      goButton().click();
    });
    expect(done).toEqual([]);
    expect(closed).toBe(0);
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("この分岐点は使えません");
  });

  it("surfaces the server's reason from a RESOLVED {error} body, not a generic message", async () => {
    // api() は失敗しても reject しない — 4xx/5xx は {error:{code,message}} として
    // **解決**する（client.ts）。ここを resolve で書かないと、reject しか試さないテストが
    // 通ってしまい、実際の画面では理由が全部「no session in fork response」に化ける。
    apiJSON.mockResolvedValue({ error: { code: "fork_bad_anchor", message: "エージェントの発言からは分岐できません" } });
    mount();
    await act(async () => {
      goButton().click();
    });
    expect(done).toEqual([]);
    expect(closed).toBe(0);
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("エージェントの発言からは分岐できません");
  });

  it("does not report success when the response carries no session", async () => {
    // 200 でも name が無ければ分岐できていない。ここで通すと、開くべきペインが無いまま
    // 「分岐しました」だけが出る。
    apiJSON.mockResolvedValue({});
    mount();
    await act(async () => {
      goButton().click();
    });
    expect(done).toEqual([]);
    expect(closed).toBe(0);
  });
});
