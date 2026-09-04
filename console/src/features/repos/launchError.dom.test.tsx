// The launch-failure toast must not drop WHY the launch failed.
//
// Damage: the Agent reports a managed-runtime launch failure with a generic code and puts
// the cause in message only. While this went through errText, a code that has a translation
// (runtime_failed) was rendered from that translation alone and the message was thrown away,
// so the screen said no more than "could not start the agent, wait a while and retry".
// When the shared daemon was never woken because it is unauthenticated (docs/log/27's auth
// gate) waiting never helps, so a user who saw only that line retried forever.
//
// client.ts is used FOR REAL here: the errText/errDetail difference is the point, and
// mocking it away would leave nothing to measure. Only api is swapped out.
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
    // The point: message survived. Reverting to errText turns exactly this line red.
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
    // The wait-and-retry wording must never be shown for a permanent cause.
    expect(shown).not.toContain(t("err.runtime_failed"));
  });
});
