// 変更ファイル帯（docs/68 §68.5）の描画。押さえたいのは見た目ではなく 3 つの約束:
//   - 材料が無いときは「0 件」ではなく帯ごと出さない（未対応 kind と本当に 0 件は
//     利用者から区別できない）
//   - 作業ツリーに差分が無い行も消さない（消すと「さっき直したのに居ない」になる）
//   - 開閉はセッション毎に憶える（ToDo と同じ作法。ターミナル⇄チャットを往復しても
//     畳んだままでいてほしい）
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiMock = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  isTransientErr: (d: unknown) => !!d && typeof d === "object" && "error" in (d as object),
}));

import { FileChangeStrip } from "./FileChangeStrip.tsx";
import type { SessionFile } from "./sessionFiles.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const file = (over: Partial<SessionFile> = {}): SessionFile => ({
  path: "repos/r/src/a.ts",
  repo: "r",
  rel: "src/a.ts",
  verb: "edit",
  added: 4,
  removed: 1,
  count: 1,
  lastIdx: 3,
  lastTs: "2026-08-17T10:00:00Z",
  ...over,
});

async function render(session: string, files: SessionFile[]) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<FileChangeStrip session={session} files={files} />);
  });
  return host;
}

beforeEach(() => {
  localStorage.clear();
  apiMock.mockReset();
  apiMock.mockResolvedValue({ changes: [{ path: "repos/r/src/a.ts", repo: "r", index: " ", worktree: "M" }] });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("FileChangeStrip", () => {
  it("編集が 1 件も無ければ帯そのものを描かない", async () => {
    const el = await render("s1", []);
    expect(el.querySelector(".mirror-files")).toBeNull();
  });

  it("既定は畳まれていて、件数と直近のファイル名だけ見える", async () => {
    const el = await render("s1", [file()]);
    const strip = el.querySelector(".mirror-files");
    expect(strip).not.toBeNull();
    expect(strip!.classList.contains("open")).toBe(false);
    expect(el.querySelector(".mfl-count")!.textContent).toBe("1");
    expect(el.querySelector(".mfl-lead")!.textContent).toBe("a.ts");
    // 合計の増減は畳んだままでも出す（一目で規模が分かる）
    expect(el.querySelector(".mfl-stat .dv-add")!.textContent).toBe("+4");
  });

  it("開くと行が出て、状態バッジが git 由来になる", async () => {
    const el = await render("s1", [file()]);
    await act(async () => {
      (el.querySelector(".mirror-files-toggle") as HTMLButtonElement).click();
    });
    expect(el.querySelector(".mirror-files")!.classList.contains("open")).toBe(true);
    const rows = el.querySelectorAll(".mfl-item");
    expect(rows).toHaveLength(1);
    expect(rows[0].classList.contains("mfl-unstaged")).toBe(true);
    expect(el.querySelector(".mfl-name")!.textContent).toBe("a.ts");
    expect(el.querySelector(".mfl-dir")!.textContent).toBe("src");
  });

  it("⚠️ 作業ツリーに差分が無い行も残す（灰色で）", async () => {
    apiMock.mockResolvedValue({ changes: [] });
    const el = await render("s1", [file()]);
    await act(async () => {
      (el.querySelector(".mirror-files-toggle") as HTMLButtonElement).click();
    });
    const row = el.querySelector(".mfl-item")!;
    expect(row.classList.contains("mfl-clean")).toBe(true);
    // 開けなくなってはいけない——ファイル自体はまだそこにある
    expect((row.querySelector(".mfl-row") as HTMLButtonElement).disabled).toBe(false);
  });

  it("開閉の選択はセッション毎に憶える", async () => {
    const el = await render("s1", [file()]);
    await act(async () => {
      (el.querySelector(".mirror-files-toggle") as HTMLButtonElement).click();
    });
    expect(localStorage.getItem("af.mirror-files-open.s1")).toBe("1");

    act(() => root?.unmount());
    host?.remove();
    const again = await render("s1", [file()]);
    expect(again.querySelector(".mirror-files")!.classList.contains("open")).toBe(true);

    // 別セッションはその選択を引き継がない
    act(() => root?.unmount());
    host?.remove();
    const other = await render("s2", [file()]);
    expect(other.querySelector(".mirror-files")!.classList.contains("open")).toBe(false);
  });

  it("消えたファイルの行は開けない", async () => {
    apiMock.mockResolvedValue({ changes: [] });
    const el = await render("s1", [file({ verb: "delete" })]);
    await act(async () => {
      (el.querySelector(".mirror-files-toggle") as HTMLButtonElement).click();
    });
    expect((el.querySelector(".mfl-row") as HTMLButtonElement).disabled).toBe(true);
  });
});
