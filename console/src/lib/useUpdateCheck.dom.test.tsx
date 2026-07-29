// 更新トーストの文言契約。ここは「押していいのか」を判断する唯一の材料なので、
// 2 つの事実が別物であることを固定する。
//   ・更新（リロード）で実行中のセッションは止まらない → 常に言う
//   ・バックエンドも更新されている（CP が検出した stale）→ そのときだけ、任意の
//     タイミングでワークスペースの停止→起動が要ると言う
// 後者を無条件に出すと、Console だけの更新でも「要再起動」と言うことになり、WS バー
// のバッジごと信用されなくなる（狼少年）。
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { UpdateToast } = await import("./useUpdateCheck.tsx");
const { useWorkspaceStore } = await import("../core/store/workspace.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(<UpdateToast server={{ time: "2026-07-29T00:00:00Z", sha: "abc1234" }} />);
  });
}

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
});

describe("update toast", () => {
  it("always says sessions keep running, and offers 更新", async () => {
    act(() => useWorkspaceStore.setState({ state: "running", stale: false }));
    await render();
    expect(host.textContent).toContain("セッションは止まりません");
    expect(host.textContent).not.toContain("停止→起動");
    expect(host.querySelector(".update-toast-btn")?.textContent).toBe("更新");
  });

  it("adds the stop→start note only when the backend also moved", async () => {
    act(() => useWorkspaceStore.setState({ state: "running", stale: true }));
    await render();
    expect(host.textContent).toContain("セッションは止まりません");
    expect(host.textContent).toContain("停止→起動");
  });

  it("picks the note up live, so a toast opened before the poll still tells the truth", async () => {
    act(() => useWorkspaceStore.setState({ state: "running", stale: false }));
    await render();
    expect(host.textContent).not.toContain("停止→起動");
    await act(async () => useWorkspaceStore.setState({ stale: true }));
    expect(host.textContent).toContain("停止→起動");
  });
});
