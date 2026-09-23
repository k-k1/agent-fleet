// The cleanup modal's cache section — the cache of sessions that are gone for good.
//
// What is pinned here is what a screenshot cannot show: that the cache rows stay out of the
// worktree tree, that the one-shot sends a delete only for rows the Agent offered an action
// for (a scan it could not trust comes back as keep and must not be deleted), and that the
// confirm says this delete has no trash behind it — the only one in the modal that does not.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let candidates: unknown[] = [];
let writes: { url: string; method: string }[] = [];

const fetchMock = vi.fn(async (url: string, opts?: RequestInit) => {
  const u = String(url);
  const method = String(opts?.method || "GET");
  if (method !== "GET") writes.push({ url: u, method });
  const body = u.includes("sessions/cleanup")
    ? { candidates }
    : u.includes("cleanup/archives")
      ? { archives: [] }
      : {};
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    headers: { get: () => null },
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response;
});
vi.stubGlobal("fetch", fetchMock);

const { CleanupModal } = await import("./CleanupModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { setLocale } = await import("../../lib/i18n/index.ts");

let host: HTMLDivElement;
let root: Root;
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

const render = async () => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(
      <ToastProvider>
        <ConfirmProvider>
          <CleanupModal />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  // Two fetches (survey + trash) settle.
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
};

const click = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await new Promise((r) => setTimeout(r, 0));
  });
};

const buttonByText = (text: string) =>
  [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(text));

const cacheRow = (id: string, over: Record<string, unknown> = {}) => ({
  type: "cache",
  action: "delete_cache",
  id,
  safety: "safe",
  reason_key: "clean.reason.cache_orphan",
  reason: "…",
  bytes: 25 * 1024 * 1024,
  files: 274,
  dirs: 117,
  ...over,
});

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  setLocale("ja");
  writes = [];
  fetchMock.mockClear();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  document.body.innerHTML = "";
});

describe("CleanupModal cache section", () => {
  it("lists cache rows on their own, named and sized, outside the worktree stage", async () => {
    candidates = [
      cacheRow("pasted"),
      { type: "branch", action: "delete_branch", id: "app", repo: "app", branch: "temp/x", safety: "safe", reason: "…" },
    ];
    await render();
    const stages = [...document.querySelectorAll(".clean-stage")];
    const cacheStage = stages.find((s) => s.textContent?.includes("削除済みセッションのキャッシュ"))!;
    expect(cacheStage.textContent).toContain("貼り付け・添付したファイル");
    expect(cacheStage.textContent).toContain("117 件・25 MB");
    const stage2 = stages.find((s) => s.textContent?.includes("作業コピー・ブランチを削除"))!;
    expect(stage2.textContent).not.toContain("貼り付け");
  });

  it("deletes only the rows the Agent offered, after a confirm that says it cannot be undone", async () => {
    candidates = [
      cacheRow("pasted"),
      // An unprovable scan: keep, no action — never sent.
      cacheRow("codex-view-image", {
        action: undefined,
        safety: "keep",
        reason_key: "clean.reason.cache_unsafe",
        bytes: undefined,
        dirs: undefined,
      }),
    ];
    await render();
    await click(buttonByText("まとめて削除"));
    expect(document.querySelector(".ui-confirm")?.textContent).toContain("元に戻すことはできません");
    expect(writes).toEqual([]);
    const buttons = [...document.querySelectorAll<HTMLButtonElement>(".ui-confirm-actions button")];
    // The keep row has no action, so it would never reach fetch even if it were targeted —
    // the count on the confirm is what shows it was left out.
    expect(buttons[1].textContent).toContain("1 件");
    await click(buttons[1]);
    expect(writes).toEqual([{ url: expect.stringContaining("api/cleanup/cache/pasted"), method: "DELETE" }]);
  });

  it("adds the no-trash warning when a selection mixes cache with trash-backed deletes", async () => {
    candidates = [
      cacheRow("pasted"),
      { type: "branch", action: "delete_branch", id: "app", repo: "app", branch: "temp/x", safety: "safe", reason: "…" },
    ];
    await render();
    await click(buttonByText("安全なものを全選択"));
    await click(buttonByText("選択したものを片付ける"));
    const text = document.querySelector(".ui-confirm")?.textContent || "";
    expect(text).toContain("ごみ箱へ退避");
    expect(text).toContain("キャッシュの削除はごみ箱を経由せず");
  });

  it("marks a row whose scan stopped at its budget as partial", async () => {
    candidates = [cacheRow("pasted", { truncated: true, reason_key: "clean.reason.cache_partial" })];
    await render();
    const row = document.querySelector(".clean-type-cache")!.closest(".clean-row")!;
    expect(row.textContent).toContain("（一部）");
    expect(row.textContent).toContain("一部だけ");
  });
});
