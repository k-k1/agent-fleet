// A pane saved before every pane had a studio (ADR 0100 decision 10, revised) moves this
// browser's localStorage draft into a new studio on mount, points itself at it, and drops the
// local copy — once, even when React mounts it twice.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";

const creates: string[] = [];
let createFails = false;

vi.mock("./api.ts", async (orig) => ({
  ...(await orig<typeof import("./api.ts")>()),
  createStudio: async (body: { draft: { prompt?: string } }) => {
    creates.push(body.draft.prompt ?? "");
    await new Promise((r) => setTimeout(r, 5));
    return createFails ? { error: { code: "unavailable", message: "down" } } : { id: `st${creates.length}` };
  },
}));
vi.mock("../../core/store/workspace.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/store/workspace.ts")>()),
  useWorkspaceStore: (sel: (s: { state: string }) => unknown) => sel({ state: "running" }),
}));

const { useLayoutStore } = await import("../../layout/store.ts");
const { ImagegenView } = await import("./ImagegenView.tsx");
const { draftKey, emptyDraft, saveDraft } = await import("./draft.ts");
const { getTenant } = await import("../../core/api/client.ts");

let host: HTMLDivElement;
let root: Root;
const retargets: [string, string | null][] = [];

beforeEach(() => {
  creates.length = 0;
  retargets.length = 0;
  createFails = false;
  localStorage.clear();
  useLayoutStore.setState({
    setPaneTarget: (id: string, t: { content: { studioId?: string | null } }) => retargets.push([id, t.content.studioId ?? null]),
  } as never);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  localStorage.clear();
});

const mount = async () => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () =>
    root.render(
      <StrictMode>
        <ImagegenView paneId="p1" studioId={null} />
      </StrictMode>,
    ),
  );
  await act(async () => new Promise((r) => setTimeout(r, 20)));
};

describe("スタジオなしペインの移行", () => {
  it("ローカルの下書きで 1 つだけ作り、ペインを差し替え、ローカルを消す", async () => {
    saveDraft(draftKey(getTenant()), { ...emptyDraft(), prompt: "old local draft" });
    await mount();
    expect(creates).toEqual(["old local draft"]);
    expect(retargets).toEqual([["p1", "st1"]]);
    expect(localStorage.getItem(draftKey(getTenant()))).toBeNull();
  });

  it("下書きが無ければ空のスタジオ", async () => {
    await mount();
    expect(creates).toEqual([""]);
    expect(retargets).toEqual([["p1", "st1"]]);
  });

  it("作れなければローカルの下書きは残し、やり直しを出す", async () => {
    createFails = true;
    saveDraft(draftKey(getTenant()), { ...emptyDraft(), prompt: "keep me" });
    await mount();
    expect(retargets).toEqual([]);
    expect(localStorage.getItem(draftKey(getTenant()))).toContain("keep me");
    const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === "Try again");
    expect(retry).toBeTruthy();
    createFails = false;
    await act(async () => retry!.click());
    await act(async () => new Promise((r) => setTimeout(r, 20)));
    expect(creates).toEqual(["keep me", "keep me"]);
    expect(retargets).toEqual([["p1", "st2"]]);
  });
});
