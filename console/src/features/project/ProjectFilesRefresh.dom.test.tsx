// ツリーの自動更新（セッションが入力待ちに入った合図＝ files ストアの scoped）と、
// フォルダを開き直したときの裏側の読み直し。
//
// 押さえたいのは 3 つ:
//   1. 合図が来たら、その作業コピー配下の**開いている**ディレクトリだけ読み直す
//      （全体を引き直す 更新 ボタンより軽い、が売りなので、範囲が広がったら意味が無い）
//   2. ★ 読み直しに失敗しても、行を消さない。api の失敗を空一覧として書き込むと
//      「ターンが終わるたびにツリーが空になる」という、古い表示より遥かに悪い壊れ方をする
//   3. 畳んで開き直したフォルダは、キャッシュを出しつつ裏で読み直す
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Entry {
  name: string;
  type: string;
}

let served: Record<string, Entry[]> = {};
let failing = new Set<string>(); // 5xx を返すパス（過渡的失敗）
let calls: string[] = [];

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (url: string) => {
    const p = decodeURIComponent(new URL(url, "http://x/").searchParams.get("path") || "");
    calls.push(p);
    if (failing.has(p)) return { error: { code: "http_502", status: 502 } };
    if (!(p in served)) return { error: { code: "not_dir", status: 404 } };
    return { entries: served[p] };
  }),
  // 実物と同じ判定（5xx だけが過渡的、4xx は終端）。
  isTransientErr: (d: unknown) => {
    const err = (d as { error?: { code?: string; status?: number } } | null)?.error;
    if (!err) return false;
    if (typeof err.status === "number" && err.status >= 500) return true;
    return typeof err.code === "string" && /^http_5\d\d$/.test(err.code);
  },
  uploadFiles: vi.fn(),
  downloadURL: vi.fn(),
  fsMkdir: vi.fn(),
  fsNewFile: vi.fn(),
  fsRename: vi.fn(),
  fsDelete: vi.fn(),
  fsSearch: vi.fn(async () => ({ results: [], truncated: false })),
}));

const { ProjectFiles } = await import("./ProjectFiles.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useFilesStore } = await import("../files/store.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ProjectFiles root="repos" markRepos />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}

// 描画が落ち着くまで進める。偽タイマーを使う節（ハイライトの消灯）だけは rAF が
// 進まないので、実装を差し替える。
const settleFrames = async (): Promise<void> => {
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await new Promise((r) => requestAnimationFrame(() => r(null)));
    });
  }
};
const settleTimers = async (): Promise<void> => {
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20);
    });
  }
};
let settleImpl: () => Promise<void> = settleFrames;
async function settle(): Promise<void> {
  await settleImpl();
}

const paths = () => [...host.querySelectorAll<HTMLLIElement>(".fsrow")].map((li) => li.dataset.path);
const row = (p: string) => host.querySelector<HTMLLIElement>(`li[data-path="${p}"]`);
const click = async (el: Element | null) => {
  await act(async () => (el as HTMLElement).click());
  await settle();
};

/** 「入力待ちになった」合図（wireFilesSessionRefresh が撃つのと同じもの）。 */
const turnEnded = async (prefix: string) => {
  await act(async () => {
    useFilesStore.getState().refreshUnder(prefix);
  });
  await settle();
};

beforeEach(async () => {
  useWorkspaceStore.setState({ state: "running" });
  useFilesStore.setState({ reveal: { path: null, n: 0, focus: false }, tick: 0, scoped: { prefix: "", n: 0 } });
  useReposStore.setState({
    repos: [
      { name: "agent-fleet", branch: "develop" },
      { name: "other", branch: "develop" },
    ],
  });
  served = {
    repos: [
      { name: "agent-fleet", type: "dir" },
      { name: "other", type: "dir" },
    ],
    "repos/agent-fleet": [
      { name: "docs", type: "dir" },
      { name: "README.md", type: "file" },
    ],
    "repos/agent-fleet/docs": [{ name: "a.md", type: "file" }],
    "repos/other": [{ name: "x.md", type: "file" }],
  };
  failing = new Set();
  calls = [];
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("セッションが入力待ちになったときの自動更新", () => {
  it("開いているディレクトリに増えたファイル・消えたファイルが出る", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    expect(paths()).toContain("repos/agent-fleet/README.md");

    // エージェントがターン中に 1 つ足して 1 つ消した。
    served["repos/agent-fleet"] = [
      { name: "docs", type: "dir" },
      { name: "NEW.md", type: "file" },
    ];
    await turnEnded("repos/agent-fleet");

    expect(paths()).toContain("repos/agent-fleet/NEW.md");
    expect(paths()).not.toContain("repos/agent-fleet/README.md");
  });

  it("読み直すのは範囲内で画面に出ている場所だけ（別の作業コピーには触らない）", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    await click(row("repos/other"));
    calls = [];
    await turnEnded("repos/agent-fleet");

    // 開いた repos/agent-fleet、その中に見えている docs（畳み表示 a/b/c の判断に
    // 中身が要るので、ツリーは元から先読みしている）、そして作業コピーの増減が出る
    // 親＝この木の根。開いている別の作業コピーは引かない。
    expect(new Set(calls)).toEqual(new Set(["repos", "repos/agent-fleet", "repos/agent-fleet/docs"]));
    expect(calls).not.toContain("repos/other"); // 範囲外
  });

  it("★ 読み直しが 502 で失敗しても、今の行を消さない", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    const before = paths();
    expect(before).toContain("repos/agent-fleet/README.md");

    // WS の再起動直後などに CP が返す過渡的失敗。api は entries を持たない。
    failing = new Set(["repos", "repos/agent-fleet"]);
    await turnEnded("repos/agent-fleet");

    expect(paths()).toEqual(before);
  });

  it("別の作業コピーの合図では読み直さない", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    served["repos/agent-fleet"] = [{ name: "NEW.md", type: "file" }];
    calls = [];
    await turnEnded("repos/other");

    expect(calls).not.toContain("repos/agent-fleet");
    expect(paths()).toContain("repos/agent-fleet/README.md"); // 触られていない
  });
});

describe("フォルダを開き直したとき", () => {
  it("畳んでいる間の増減が、開き直しで反映される", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    await click(row("repos/agent-fleet")); // 畳む
    expect(paths()).not.toContain("repos/agent-fleet/README.md");

    served["repos/agent-fleet"] = [
      { name: "docs", type: "dir" },
      { name: "LATER.md", type: "file" },
    ];
    await click(row("repos/agent-fleet")); // 開き直す

    expect(paths()).toContain("repos/agent-fleet/LATER.md");
  });
});

// タブ／ウィンドウに戻ったときの再検証（P2 ③）。離席中に終わったターンと、
// state を持たないセッション（shell / SSM）や Agent Fleet の外での変更を拾う唯一の道。
describe("タブに戻ったとき", () => {
  const comeBack = async () => {
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await settle();
  };

  it("画面に出ているディレクトリを読み直す", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    served["repos/agent-fleet"] = [
      { name: "docs", type: "dir" },
      { name: "AWAY.md", type: "file" },
    ];
    calls = [];
    await comeBack();

    expect(calls).toContain("repos/agent-fleet");
    expect(paths()).toContain("repos/agent-fleet/AWAY.md");
  });

  it("戻るたびには撃たない（最短間隔を空ける）", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    await comeBack();
    calls = [];
    await comeBack(); // すぐもう一度戻ってきても、往復は増やさない
    expect(calls).toEqual([]);
  });
});

// 増えた行のハイライト（P2 ⑤）。「増えた」ことより「どれが増えたか」が知りたい情報で、
// かつ数秒で消えることまでが仕様 — 消えないと、次にどれが増えたのか分からなくなる。
describe("増えた行のハイライト", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    settleImpl = settleTimers;
  });
  afterEach(() => {
    settleImpl = settleFrames;
    vi.useRealTimers();
  });

  const classOf = (p: string) => row(p)?.className ?? "";

  it("増えた行にだけ付き、数秒で消える", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    served["repos/agent-fleet"] = [
      { name: "docs", type: "dir" },
      { name: "NEW.md", type: "file" },
      { name: "README.md", type: "file" },
    ];
    await turnEnded("repos/agent-fleet");

    expect(classOf("repos/agent-fleet/NEW.md")).toContain("fs-new");
    expect(classOf("repos/agent-fleet/README.md")).not.toContain("fs-new"); // 元からある行

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(classOf("repos/agent-fleet/NEW.md")).not.toContain("fs-new");
  });

  it("初回の読み込みでは光らない（全部が「増えた」ことになってしまう）", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    expect([...host.querySelectorAll(".fsrow.fs-new")]).toHaveLength(0);
  });
});
