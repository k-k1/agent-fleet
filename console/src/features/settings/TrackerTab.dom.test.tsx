// Jira 接続カード（docs/80 §80.17）。芯は 2 つ:
//   ① OAuth ボタンは「テナントがアプリを登録しているか」で出し分ける —— 押してから
//      not_configured が返る形にしない（押した本人には直せない設定なので）
//   ② API トークン経路を残す（アプリ未登録のテナントには他に入口が無い）
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../lib/i18n/index.ts";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
  raw: vi.fn(async () => ({ ok: true })),
  errText: (e: unknown) => String(e),
}));

const { TrackerTab } = await import("./TrackerTab.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
// 接続済みのカードは切断ボタンを描き、それが useConfirm を要求する。
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

const conns = (jira: unknown) => ({ jira });

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <TrackerTab />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

const text = () => host.textContent || "";
const buttons = () => [...host.querySelectorAll("button")].map((b) => b.textContent || "");

beforeEach(() => {
  apiMock.mockReset();
  apiJSONMock.mockReset();
  useWorkspaceStore.setState({ state: "running" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("TrackerTab", () => {
  it("アプリ未登録なら OAuth ボタンは押せず、理由を出す", async () => {
    apiMock.mockImplementation((p: string) => {
      if (p === "api/git-oauth") return Promise.resolve({ jira: { configured: false } });
      return Promise.resolve(conns({ connected: false }));
    });
    await render();
    const btn = [...host.querySelectorAll("button")].find((b) => b.textContent === t("tracker.jira_connect_oauth"));
    expect(btn).toBeTruthy();
    expect(btn!.disabled).toBe(true);
    expect(text()).toContain(t("tracker.jira_oauth_unconfigured"));
    // ★ トークン経路は残っている（アプリ未登録のテナントの唯一の入口）。
    expect(buttons()).toContain(t("tracker.jira_use_token"));
  });

  it("アプリが登録済みなら OAuth ボタンが押せる", async () => {
    apiMock.mockImplementation((p: string) => {
      if (p === "api/git-oauth") return Promise.resolve({ jira: { configured: true } });
      return Promise.resolve(conns({ connected: false }));
    });
    await render();
    const btn = [...host.querySelectorAll("button")].find((b) => b.textContent === t("tracker.jira_connect_oauth"));
    expect(btn!.disabled).toBe(false);
    expect(text()).not.toContain(t("tracker.jira_oauth_unconfigured"));
  });

  it("API トークンを選ぶと 3 項目が出る（メールも資格情報）", async () => {
    apiMock.mockImplementation((p: string) =>
      Promise.resolve(p === "api/git-oauth" ? { jira: { configured: true } } : conns({ connected: false })),
    );
    await render();
    await act(async () => {
      [...host.querySelectorAll("button")].find((b) => b.textContent === t("tracker.jira_use_token"))!.click();
    });
    expect(host.querySelectorAll("input.cinput").length).toBe(3);
  });

  it("★ 接続済みで複数サイトなら選択できる（1 件なら出さない）", async () => {
    const jira = {
      connected: true,
      authKind: "oauth",
      account: "山田 太郎",
      site: "https://one.atlassian.net",
      cloudId: "cid-1",
      sites: [
        { cloudId: "cid-1", url: "https://one.atlassian.net", name: "One" },
        { cloudId: "cid-2", url: "https://two.atlassian.net", name: "Two" },
      ],
    };
    apiMock.mockImplementation((p: string) =>
      Promise.resolve(p === "api/git-oauth" ? { jira: { configured: true } } : conns(jira)),
    );
    await render();
    expect(text()).toContain("山田 太郎");
    expect(text()).toContain(t("tracker.jira_via_oauth"));
    const sel = host.querySelector("select");
    expect(sel).toBeTruthy();
    expect(sel!.options.length).toBe(2);

    apiJSONMock.mockResolvedValue({ connected: true });
    await act(async () => {
      sel!.value = "cid-2";
      sel!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(apiJSONMock).toHaveBeenCalledWith("api/connections/jira/site", "PUT", { cloudId: "cid-2" });
  });

  it("サイトが 1 件なら選択は出さない", async () => {
    apiMock.mockImplementation((p: string) =>
      Promise.resolve(
        p === "api/git-oauth"
          ? { jira: { configured: true } }
          : conns({ connected: true, authKind: "oauth", account: "x", site: "https://one.atlassian.net", cloudId: "cid-1", sites: [{ cloudId: "cid-1", url: "https://one.atlassian.net" }] }),
      ),
    );
    await render();
    expect(host.querySelector("select")).toBeNull();
  });

  it("ワークスペースが止まっていれば、まず起動を促す（資格情報はコンテナの中）", async () => {
    useWorkspaceStore.setState({ state: "stopped" });
    apiMock.mockResolvedValue({ jira: { configured: true } });
    await render();
    expect(text()).toContain(t("ops.ws_required_title"));
  });
});
