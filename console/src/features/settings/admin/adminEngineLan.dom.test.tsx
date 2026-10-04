// The LAN ComfyUI panel (#957): it says which source is in effect, saves URL and key, keeps the
// key write-only, and has no form where a managed engine-table row holds the role.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { ReactElement } from "react";

const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { ComfyLanPanel, type ComfyLanStatus } from "./adminEngineLan.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(el: ReactElement) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(el);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const ui = () => document.body;
const click = async (el: HTMLElement | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
};
const typeInto = async (el: HTMLInputElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const urlInput = () => ui().querySelector('input[type="url"]') as HTMLInputElement | null;
const keyInput = () => ui().querySelector('input[type="password"]') as HTMLInputElement | null;
const button = (label: string) =>
  Array.from(ui().querySelectorAll("button")).find((b) => b.textContent === label) as
    | HTMLButtonElement
    | undefined;
const text = (id: string) => ui().querySelector(`[data-testid="${id}"]`)?.textContent || "";

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  apiJSON.mockReset();
});

describe("ComfyLanPanel", () => {
  it("shows the environment as the source, then saves a panel URL and key without echoing the key", async () => {
    const status: ComfyLanStatus = {
      available: true,
      source: "env",
      url: "http://192.0.2.10:8188",
      env_url: "http://192.0.2.10:8188",
      env_key_set: true,
      panel_key_set: false,
    };
    apiJSON.mockResolvedValue({
      available: true,
      source: "panel",
      url: "http://192.0.2.20:8188",
      panel_url: "http://192.0.2.20:8188",
      panel_key_set: true,
      env_url: "http://192.0.2.10:8188",
      updated_by: "root1",
      updated_at: "2026-10-04T01:00:00Z",
    });
    const onChanged = vi.fn();
    await mount(<ComfyLanPanel status={status} onChanged={onChanged} />);

    expect(text("comfy-lan-source")).toContain("AF_COMFY_URL");
    expect(text("comfy-lan-source")).toContain("http://192.0.2.10:8188");
    expect(button("接続先を保存")?.disabled).toBe(true);

    await typeInto(urlInput()!, "http://192.0.2.20:8188");
    await typeInto(keyInput()!, "typed-secret");
    await click(button("接続先を保存"));

    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/comfy-lan", "PUT", {
      url: "http://192.0.2.20:8188",
      key: "typed-secret",
    });
    expect(onChanged).toHaveBeenCalled();
    expect(text("comfy-lan-source")).toContain("このパネル");
    expect(text("comfy-lan-key-state")).toContain("キーあり");
    expect(text("comfy-lan-key-state")).toContain("root1");
    // The key is write-only: gone from the field and from the page once saved.
    expect(keyInput()!.value).toBe("");
    expect(host!.innerHTML).not.toContain("typed-secret");
  });

  it("sends no key when the field is left empty, so the stored one is kept", async () => {
    apiJSON.mockResolvedValue({ available: true, source: "panel", panel_url: "http://192.0.2.21:8188" });
    await mount(
      <ComfyLanPanel
        status={{
          available: true,
          source: "panel",
          url: "http://192.0.2.20:8188",
          panel_url: "http://192.0.2.20:8188",
          panel_key_set: true,
        }}
      />,
    );
    expect(urlInput()!.value).toBe("http://192.0.2.20:8188");
    await typeInto(urlInput()!, "http://192.0.2.21:8188");
    await click(button("接続先を保存"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/comfy-lan", "PUT", { url: "http://192.0.2.21:8188" });
  });

  it("clears the key and removes the panel value through their own requests", async () => {
    const saved: ComfyLanStatus = {
      available: true,
      source: "panel",
      url: "http://192.0.2.20:8188",
      panel_url: "http://192.0.2.20:8188",
      panel_key_set: true,
    };
    apiJSON.mockResolvedValue({ ...saved, panel_key_set: false });
    await mount(<ComfyLanPanel status={saved} />);
    await click(button("キーを削除"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/comfy-lan", "PUT", {
      url: "http://192.0.2.20:8188",
      clear_key: true,
    });
    expect(text("comfy-lan-key-state")).toContain("キーなし");
    await click(button("パネルの設定を削除"));
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/comfy-lan", "DELETE", undefined);
  });

  // Removing is not always "no image engine": a table row or a borrowed engine takes the role back.
  it("says what removing the panel value returns images to", async () => {
    const base: ComfyLanStatus = { available: true, source: "panel", panel_url: "http://192.0.2.20:8188" };
    const cases: [ComfyLanStatus, string][] = [
      [{ ...base, fallback_source: "env", fallback_url: "http://192.0.2.10:8188" }, "http://192.0.2.10:8188（AF_COMFY_URL）"],
      [{ ...base, fallback_source: "table", fallback_url: "http://192.0.2.30:8188" }, "エンジン表の外部の行 http://192.0.2.30:8188"],
      [{ ...base, remote_configured: true }, "止まるとは限りません"],
      [base, "画像エンジンは無くなります"],
    ];
    for (const [status, want] of cases) {
      await mount(<ComfyLanPanel status={status} />);
      expect(text("comfy-lan-fallback")).toContain(want);
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("offers no form where a managed engine-table row holds the role", async () => {
    await mount(<ComfyLanPanel status={{ available: false, source: "table", url: "http://image.af.internal:8188" }} />);
    expect(urlInput()).toBeNull();
    expect(keyInput()).toBeNull();
    expect(text("comfy-lan-source")).toContain("エンジン表");
    expect(ui().textContent).toContain("スタックが管理");
  });

  it("names a refused URL in Japanese", async () => {
    apiJSON.mockResolvedValue({
      error: { code: "engine_comfy_url_credentials", message: "the URL carries credentials" },
    });
    await mount(<ComfyLanPanel status={{ available: true, source: "" }} />);
    expect(text("comfy-lan-source")).toContain("設定されていません");
    await typeInto(urlInput()!, "http://u:p@192.0.2.20:8188");
    await click(button("接続先を保存"));
    expect(ui().textContent).toContain("ユーザー名やパスワード");
  });
});
