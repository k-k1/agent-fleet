// A launch whose session was created but whose draft did not fully arrive (#5077) must say so:
// the caller deletes the memo and clears the draft on ok, so ok alone would lose what never
// reached the session.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn();
const pasteImage = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, apiJSON: (...a: unknown[]) => apiJSON(...a), pasteImage: (...a: unknown[]) => pasteImage(...a) };
});
const stash = vi.fn();
vi.mock("../memo/launchStash.ts", () => ({ stashLaunchDraft: (...a: unknown[]) => stash(...a) }));
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
const file = (name: string) => new File(["x"], name, { type: "image/png" });

beforeEach(async () => {
  apiJSON.mockReset();
  pasteImage.mockReset();
  stash.mockReset();
  stash.mockResolvedValue({ ok: true });
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

const launch = async (images: File[]) => {
  let r!: Awaited<ReturnType<NonNullable<typeof start>>>;
  await act(async () => {
    r = await start!(
      { dir: "/repos/x", repo: "x" },
      {
        kind: "claude", driver: "", model: "", effort: "", startMode: "normal",
        prompt: "look at these", title: "", images, subdir: "", base: "", newBranch: "", worktree: false,
      },
    );
  });
  return r;
};

const created = (path: string, body?: unknown) =>
  path === "api/sessions" ? Promise.resolve({ name: "s1" }) : Promise.resolve(body ?? {});

describe("partial draft delivery", () => {
  it("is complete when every file and the prompt arrive", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValue({ status: 200, path: "/p/a.png" });
    const r = await launch([file("a.png")]);
    expect(r).toEqual({ ok: true, name: "s1" });
    expect(toasts()).not.toContain(t("rp.draft_kept"));
  });

  it("retains a refused attachment for retry to the created session", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValueOnce({ status: 200, path: "/p/a.png" }).mockResolvedValueOnce({ status: 413, error: { code: "paste_too_large" } });
    const r = await launch([file("a.png"), file("b.png")]);
    expect(r.ok).toBe(true);
    expect(r.incomplete).toEqual({ files: ["b.png"], prompt: true, saved: false });
    expect(stash).not.toHaveBeenCalled();
    expect(apiJSON).toHaveBeenCalledTimes(1);
    const review = document.querySelector<HTMLButtonElement>(".ui-toast-msg button")!;
    await act(async () => review.click());
    let dialog = document.querySelector<HTMLFormElement>("form[role=dialog]")!;
    expect(dialog.querySelector("textarea")?.value).toBe("look at these");
    expect(dialog.textContent).toContain("b.png");
    const submit = async () => {
      await act(async () => dialog.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    };

    pasteImage.mockResolvedValueOnce({ status: 413, error: { code: "paste_too_large" } });
    await submit();
    expect(dialog.textContent).toContain("b.png");
    expect(apiJSON).toHaveBeenCalledTimes(1);

    await act(async () => dialog.querySelector<HTMLButtonElement>("header button")!.click());
    expect(dialog.isConnected).toBe(false);
    await act(async () => review.click());
    dialog = document.querySelector<HTMLFormElement>("form[role=dialog]")!;
    expect(dialog.textContent).toContain("b.png");
    await act(async () => dialog.querySelectorAll<HTMLButtonElement>("li button")[1].click());
    const picker = dialog.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(picker, "files", { value: [file("smaller.png")] });
    await act(async () => picker.dispatchEvent(new Event("change", { bubbles: true })));

    pasteImage.mockResolvedValueOnce({ status: 200, path: "/p/smaller.png" });
    await submit();
    expect(pasteImage.mock.calls.map(([session, f]) => [session, f.name])).toEqual([
      ["s1", "a.png"], ["s1", "b.png"], ["s1", "b.png"], ["s1", "smaller.png"],
    ]);
    expect(apiJSON).toHaveBeenCalledTimes(2);
    expect(apiJSON).toHaveBeenLastCalledWith("api/sessions/s1/input", "POST", {
      prompt: expect.stringMatching(/look at these[\s\S]*\/p\/a\.png[\s\S]*\/p\/smaller\.png/),
      when_ready: true,
    });
    expect(document.querySelector("form[role=dialog]")).toBeNull();
    expect(review.isConnected).toBe(false);
  });

  it("reports the file whose upload failed on the network", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockRejectedValue(new Error("offline"));
    const r = await launch([file("a.png")]);
    expect(r.incomplete).toEqual({ files: ["a.png"], prompt: true, saved: false });
  });

  it("reports a first prompt the Agent refused", async () => {
    apiJSON.mockImplementation((p: string) => (p === "api/sessions" ? Promise.resolve({ name: "s1" }) : Promise.resolve({ error: { code: "busy", message: "no" } })));
    pasteImage.mockResolvedValue({ status: 200, path: "/p/a.png" });
    const r = await launch([file("a.png")]);
    expect(r.ok).toBe(true);
    expect(r.incomplete).toEqual({ files: [], prompt: true, saved: true });
  });

  it("says so when the draft could not be saved as a memo either", async () => {
    stash.mockResolvedValue({ ok: false, reason: "failed" });
    pasteImage.mockResolvedValue({ status: 200, path: "/p/a.png" });
    apiJSON.mockImplementation((p: string) => created(p, { error: { code: "busy" } }));
    const r = await launch([file("a.png")]);
    expect(r.incomplete?.saved).toBe(false);
    expect(toasts()).toContain(t("rp.draft_kept"));
  });

  it("falls back to saving the text alone when the files cannot be saved", async () => {
    stash.mockReset();
    stash.mockResolvedValueOnce({ ok: false, reason: "too_large" }).mockResolvedValueOnce({ ok: true });
    apiJSON.mockImplementation((p: string) => created(p, { error: { code: "busy" } }));
    pasteImage.mockResolvedValue({ status: 200, path: "/p/big.png" });
    const r = await launch([file("big.png")]);
    expect(stash).toHaveBeenNthCalledWith(2, "x", "look at these", []);
    expect(r.incomplete?.saved).toBe(true);
    expect(toasts()).toContain(t("rp.draft_saved_text"));
    expect(toasts()).not.toContain(t("rp.draft_saved"));
  });

  it("reports a first prompt whose request itself failed", async () => {
    apiJSON.mockImplementation((p: string) => (p === "api/sessions" ? Promise.resolve({ name: "s1" }) : Promise.reject(new Error("x"))));
    pasteImage.mockResolvedValue({ status: 200, path: "/p/a.png" });
    const r = await launch([file("a.png")]);
    expect(r.incomplete?.prompt).toBe(true);
  });
});
