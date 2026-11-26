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

  it("reports the file whose upload was refused", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValueOnce({ status: 200, path: "/p/a.png" }).mockResolvedValueOnce({ status: 413, error: { code: "paste_too_large" } });
    const r = await launch([file("a.png"), file("b.png")]);
    expect(r.ok).toBe(true);
    expect(r.incomplete).toEqual({ files: ["b.png"], prompt: false });
    expect(toasts()).toContain(t("rp.draft_kept"));
  });

  it("reports the file whose upload failed on the network", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockRejectedValue(new Error("offline"));
    const r = await launch([file("a.png")]);
    expect(r.incomplete).toEqual({ files: ["a.png"], prompt: false });
  });

  it("reports a first prompt the Agent refused", async () => {
    apiJSON.mockImplementation((p: string) => (p === "api/sessions" ? Promise.resolve({ name: "s1" }) : Promise.resolve({ error: { code: "busy", message: "no" } })));
    pasteImage.mockResolvedValue({ status: 200, path: "/p/a.png" });
    const r = await launch([file("a.png")]);
    expect(r.ok).toBe(true);
    expect(r.incomplete).toEqual({ files: [], prompt: true });
  });

  it("reports a first prompt whose request itself failed", async () => {
    apiJSON.mockImplementation((p: string) => (p === "api/sessions" ? Promise.resolve({ name: "s1" }) : Promise.reject(new Error("x"))));
    pasteImage.mockResolvedValue({ status: 200, path: "/p/a.png" });
    const r = await launch([file("a.png")]);
    expect(r.incomplete?.prompt).toBe(true);
  });
});
