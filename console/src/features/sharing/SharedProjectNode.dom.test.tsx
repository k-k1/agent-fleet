// Render test for the 共有セッション tree: the recipient gets one node per working copy
// the owner shares, and a project can hold a dozen worktrees — so it has to fold, and a
// row has to say WHICH agent the conversation is with (kind icon), the recipient having
// no other clue.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("./open.ts", () => ({ openSharedSession: vi.fn() }));

const { SharedProjectNode } = await import("./SharedProjectNode.tsx");
const { groupedSharedSessions } = await import("./sharedProject.ts");
type SharedSession = import("./store.ts").SharedSession;

let root: Root | null = null;
let host: HTMLDivElement;

const session = (over: Partial<SharedSession> & { id: string }): SharedSession => ({
  // CP が返す2つ組: 正規化キー(グルーピング/永続キー)と、表示に使うログイン ID。
  ownerUserKey: "owner-example-com",
  ownerEmail: "owner@example.com",
  name: over.id,
  kind: "claude",
  state: "stopped",
  permission: "ro",
  workspaceState: "running",
  ...over,
});

async function render(sessions: SharedSession[]): Promise<void> {
  const groups = groupedSharedSessions(sessions);
  await act(async () => {
    root!.render(
      <ul>
        {groups.map((g) => <SharedProjectNode key={g.projectName} group={g} />)}
      </ul>,
    );
  });
}

const rows = () => [...host.querySelectorAll<HTMLElement>(".shared-rail-row .name")].map((el) => el.textContent);

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("SharedProjectNode", () => {
  it("セッション行に kind のアイコンを出す", async () => {
    await render([session({ id: "a", title: "調査", kind: "codex", repo: "proj", workingCopyId: "wc" })]);
    expect(host.querySelector(".shared-rail-row .sess-kic")?.className).toContain("kind-codex");
  });

  it("稼働中は所有者側と同じ状態チップ、所有者WS停止中はワークスペース停止だけを出す", async () => {
    await render([
      session({ id: "a", title: "質問中", repo: "proj", workingCopyId: "wc", state: "running", activity: "question" }),
      session({ id: "b", title: "進行中", repo: "proj", workingCopyId: "wc", state: "running", activity: "working" }),
      session({ id: "c", title: "入力待ち", repo: "proj", workingCopyId: "wc", state: "running" }),
      session({ id: "d", title: "停止中", repo: "proj", workingCopyId: "wc", state: "stopped" }),
    ]);
    const chips = [...host.querySelectorAll<HTMLElement>(".session-state")].map((el) => el.className);
    expect(chips).toEqual([
      "session-state mini question",
      "session-state mini working",
      "session-state mini on",
      "session-state mini off",
    ]);
    // 所有者 Workspace が停止していれば、行ごとの状態ではなくその1事実だけを出す。
    await render([session({ id: "a", title: "会話", repo: "proj", workingCopyId: "wc", workspaceState: "stopped" })]);
    expect(host.querySelectorAll(".session-state")).toHaveLength(0);
    expect(host.querySelector(".shared-rail-row .codicon-debug-pause")).toBeTruthy();
  });

  it("プロジェクト見出しで畳むと配下のセッションが消える", async () => {
    await render([
      session({ id: "a", title: "ベースの会話", repo: "proj", workingCopyId: "wc-base" }),
      session({ id: "b", title: "WTの会話", repo: "proj@feat", workingCopyId: "wc-wt", worktree: true, parent: "proj" }),
    ]);
    expect(rows()).toEqual(["ベースの会話", "WTの会話"]);
    const head = host.querySelector<HTMLButtonElement>(".shared-project-head")!;
    await act(async () => head.click());
    expect(rows()).toEqual([]);
    expect(head.getAttribute("aria-expanded")).toBe("false");
    await act(async () => head.click());
    expect(rows()).toEqual(["ベースの会話", "WTの会話"]);
  });

  it("worktree はブランチ名で名乗り、ベースはフォルダ名＋ブランチ", async () => {
    await render([
      session({ id: "a", title: "ベースの会話", repo: "proj", workingCopyId: "wc-base", branch: "develop" }),
      session({
        id: "b", title: "WTの会話", repo: "proj@wip-abc", workingCopyId: "wc-wt",
        worktree: true, parent: "proj", branch: "feature/G3-1159",
      }),
    ]);
    const names = [...host.querySelectorAll<HTMLElement>(".shared-copy-name")].map((el) => el.textContent);
    expect(names).toEqual(["proj", "feature/G3-1159"]);
    expect([...host.querySelectorAll<HTMLElement>(".repo-branch-inline")].map((el) => el.textContent)).toEqual(["develop"]);
  });

  it("working copy 単位でも畳める(畳んだ側だけが隠れる)", async () => {
    await render([
      session({ id: "a", title: "ベースの会話", repo: "proj", workingCopyId: "wc-base" }),
      session({ id: "b", title: "WTの会話", repo: "proj@feat", workingCopyId: "wc-wt", worktree: true, parent: "proj" }),
    ]);
    const wt = [...host.querySelectorAll<HTMLButtonElement>(".shared-node-toggle")].find((b) =>
      b.textContent?.includes("proj@feat"),
    )!;
    await act(async () => wt.click());
    expect(rows()).toEqual(["ベースの会話"]);
  });

  it("畳んだ状態は localStorage に残る", async () => {
    await render([session({ id: "a", title: "会話", repo: "proj", workingCopyId: "wc-base" })]);
    await act(async () => host.querySelector<HTMLButtonElement>(".shared-project-head")!.click());
    expect(localStorage.getItem("af-shared-proj-owner-example-com:proj")).toBe("0");
  });

  it("プロジェクト見出しに所有者のログイン ID(メールアドレス)を出す", async () => {
    await render([session({ id: "a", title: "会話", repo: "proj", workingCopyId: "wc-base" })]);
    expect(host.querySelector(".shared-project-owner")?.textContent).toBe("owner@example.com");
    // email を持たない identity(管理者が user_key だけで足した場合)だけ正規化キーへ落ちる。
    await render([session({ id: "a", title: "会話", repo: "proj", workingCopyId: "wc-base", ownerEmail: undefined })]);
    expect(host.querySelector(".shared-project-owner")?.textContent).toBe("owner-example-com");
  });
});
