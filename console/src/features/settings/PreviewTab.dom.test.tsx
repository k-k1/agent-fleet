// プレビュー用サブドメインの独立タブ。押さえるのは「発行されないデプロイに、押しても
// 何も起きない設定を出さない」こと:
//   ① usePreviewAvailable は previewDomain が空なら false（＝レールに出さない）
//   ② 判定が返るまでは null（＝出してから消える項目を作らない）
//   ③ それでも section だけ残って直接来たときは、白紙ではなく「無い」と言う
//   ④ 発行されるデプロイでは設定が出て、公開ポートは入力欄を離れたときに保存される
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  getTenant: () => "default",
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));

const wsSettings = (extra: Record<string, unknown> = {}) => ({
  agentUpdate: false,
  allowAgentUpdate: false,
  previewDomain: "example.invalid",
  previewPorts: [3000, 8080],
  previewUrls: { "8080": "https://slug-8080.example.invalid", "3000": "https://slug-3000.example.invalid" },
  previewMaxPorts: 8,
  ...extra,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

// ★ 判定はモジュールスコープにキャッシュされる（設定を開くたびに GET しないため）ので、
// テストごとに読み直す。使い回すと 2 件目以降が 1 件目の答えを見てしまう。
async function freshModule() {
  vi.resetModules();
  return await import("./PreviewTab.tsx");
}

async function mount(el: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(el);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
  apiJSON.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("usePreviewAvailable", () => {
  const probe = (usePreviewAvailable: () => boolean | null) => {
    return function Probe() {
      const v = usePreviewAvailable();
      return <span id="v">{v === null ? "pending" : String(v)}</span>;
    };
  };
  const read = () => document.querySelector("#v")!.textContent;

  it("previewDomain があれば true", async () => {
    api.mockResolvedValue(wsSettings());
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("true");
  });

  it("previewDomain が空なら false（レールに出さない）", async () => {
    api.mockResolvedValue(wsSettings({ previewDomain: "" }));
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("false");
  });

  it("答えが返るまでは null（出してから消えるのを避ける）", async () => {
    api.mockReturnValue(new Promise(() => {})); // 応答しない
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("pending");
  });
});

describe("PreviewTab", () => {
  it("発行されないデプロイでは、設定ではなく「無い」と言う", async () => {
    api.mockResolvedValue(wsSettings({ previewDomain: "" }));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    expect(document.querySelector(".ds-group")).toBeNull();
    expect(document.querySelector(".pad")!.textContent).toContain("発行されません");
  });

  it("発行されるデプロイでは、いまの URL をポート順に出す", async () => {
    api.mockResolvedValue(wsSettings());
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const urls = Array.from(document.querySelectorAll<HTMLAnchorElement>(".pv-current-url")).map((a) => a.textContent);
    expect(urls).toEqual(["slug-3000.example.invalid", "slug-8080.example.invalid"]);
  });

  it("公開ポートは打鍵ごとではなく、入力欄を離れたときに保存する", async () => {
    api.mockResolvedValue(wsSettings());
    apiJSON.mockResolvedValue(wsSettings({ previewPorts: [3000, 5173] }));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const input = document.querySelector<HTMLInputElement>(".ds-select")!;
    expect(input.value).toBe("3000, 8080");
    await act(async () => {
      // React の制御された input に値を入れる（value の setter を直接呼ぶ）。
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "3000, 5173");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(apiJSON).not.toHaveBeenCalled(); // 打ちかけの "3000, 5" では保存しない
    await act(async () => {
      // React の onBlur は native の focusout に載る（bubble しない blur では届かない）。
      input.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/ws-settings", "PUT", { previewPorts: [3000, 5173] });
  });
});
