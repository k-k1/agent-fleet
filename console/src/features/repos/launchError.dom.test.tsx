// 起動失敗のトーストが「なぜ失敗したか」を落とさないこと。
//
// 実害: Agent は managed runtime の起動失敗を汎用コードで返し、原因は message にしか
// 入れない。ここが errText だった間、翻訳のあるコード（runtime_failed）は message ごと
// 捨てられ、画面には「エージェントを起動できませんでした。しばらく待ってから再試行して
// ください。」だけが出た。共有 daemon が未認証で起こされなかった場合（docs/log/27 の
// 認証ゲート）は待っても直らないので、この文言だけを見た利用者は直らない再試行を
// 繰り返すことになる。
//
// ここでは client.ts を**本物**で使う（errText/errDetail の差が芯なので、モックで
// 潰すと検査にならない）。api だけを差し替える。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, apiJSON: (...a: unknown[]) => apiJSON(...a), pasteImage: vi.fn() };
});
vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));

const { useStartWork } = await import("./useStartWork.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;
let start: ReturnType<typeof useStartWork> | null = null;

function Probe() {
  start = useStartWork();
  return null;
}

const toasts = () => [...document.querySelectorAll(".ui-toast-msg")].map((n) => n.textContent || "");

beforeEach(async () => {
  apiJSON.mockReset();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <Probe />
      </ToastProvider>,
    );
  });
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  start = null;
});

const launch = async () => {
  await act(async () => {
    await start!(
      { dir: "/repos/x", repo: "x" },
      {
        kind: "codex", driver: "managed", model: "", effort: "", startMode: "normal",
        prompt: "", title: "", images: [], subdir: "", base: "", newBranch: "", worktree: false,
      },
    );
  });
};

describe("起動失敗のトースト", () => {
  it("翻訳のある汎用コードでも、サーバが返した原因を併記する", async () => {
    apiJSON.mockResolvedValue({
      error: { code: "runtime_failed", message: "codex app-server writer 接続に失敗しました" },
    });
    await launch();
    const shown = toasts().join("\n");
    expect(shown).toContain(t("err.runtime_failed"));
    // 芯: message が落ちていない。errText へ戻すとここだけが赤くなる。
    expect(shown).toContain("codex app-server writer 接続に失敗しました");
  });

  it("未ログインは専用コードで、待てば直る文言にならない", async () => {
    apiJSON.mockResolvedValue({
      error: {
        code: "agent_not_connected",
        message: "codex にログインしていないため app-server を起動しません",
      },
    });
    await launch();
    const shown = toasts().join("\n");
    expect(shown).toContain(t("err.agent_not_connected"));
    expect(shown).toContain("codex にログインしていないため app-server を起動しません");
    // 「しばらく待ってから再試行」は恒久要因には出さない。
    expect(shown).not.toContain(t("err.runtime_failed"));
  });
});
