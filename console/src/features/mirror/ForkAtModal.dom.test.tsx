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
const done: string[] = [];
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
        onDone={(n) => done.push(n)}
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

  it("posts the anchor and hands the new session back", async () => {
    apiJSON.mockResolvedValue({ name: "oc-2" });
    mount();
    await act(async () => {
      goButton().click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/sessions/oc-1/fork", "POST", { at: "msg_7" });
    expect(done).toEqual(["oc-2"]);
    expect(closed).toBe(1);
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
