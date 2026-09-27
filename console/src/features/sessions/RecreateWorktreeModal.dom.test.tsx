// What the recreate dialog sends: the candidate it was shown (never a SHA — the Agent
// resolves again), a new branch only when the shown branch is checked out elsewhere, and the
// restores of the sessions left ticked. The api client is the only thing swapped out.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
const raw = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return {
    ...real,
    api: (...a: unknown[]) => api(...a),
    apiJSON: (...a: unknown[]) => apiJSON(...a),
    raw: (...a: unknown[]) => raw(...a),
  };
});

const { RecreateWorktreeModal } = await import("./RecreateWorktreeModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { recreatableGroup } = await import("./ArchivedModal.tsx");
type Session = import("../../types/session.ts").Session;

const DIR = "/home/dev/repos/app@feat-z";
const sess = (name: string, over: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  dir: DIR,
  resumable: false,
  ...over,
});

let root: Root | null = null;
let host: HTMLDivElement;
let changed = 0;
let closed = 0;

const render = async (sessions: Session[]) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <RecreateWorktreeModal
          dir={DIR}
          sessions={sessions}
          onClose={() => (closed += 1)}
          onChanged={() => (changed += 1)}
        />
      </ToastProvider>,
    );
  });
};

const submit = async () => {
  const btn = document.querySelector<HTMLButtonElement>(".ui-modal-foot button[type=submit]")!;
  await act(async () => btn.click());
};

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  raw.mockReset();
  raw.mockResolvedValue({ ok: true });
  changed = 0;
  closed = 0;
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
});

describe("削除された worktree の作り直し", () => {
  it("表示した候補を source と branch で送り、選んだセッションだけ復帰する", async () => {
    api.mockResolvedValue({
      name: "app@feat-z",
      path: DIR,
      parent: "app",
      candidates: [{ source: "trash", branch: "feat-z", sha: "0123456789abcdef" }],
    });
    apiJSON.mockResolvedValue({ name: "app@feat-z", path: DIR, branch: "feat-z", source: "trash" });
    await render([
      sess("new", { branch: "feat-z", createdAt: "2026-09-02T00:00:00Z" }),
      sess("old", { branch: "temp/other", createdAt: "2026-08-01T00:00:00Z" }),
    ]);
    expect(api).toHaveBeenCalledWith("api/repos/app%40feat-z/recreate");
    expect(document.body.textContent).toContain("01234567");

    await submit();
    expect(apiJSON).toHaveBeenCalledWith("api/repos/app%40feat-z/recreate", "POST", { source: "trash", branch: "feat-z" });
    expect(changed).toBe(1);

    // The older generation's start branch differs from the recreated one: flagged.
    const warns = [...document.querySelectorAll(".rwt-warn")];
    expect(warns).toHaveLength(1);
    expect(warns[0].closest("li")?.textContent).toContain("temp/other");

    const boxes = [...document.querySelectorAll<HTMLInputElement>(".rwt-session input[type=checkbox]")];
    expect(boxes.map((b) => b.checked)).toEqual([true, true]);
    await act(async () => boxes[1].click()); // leave the older one on the shelf
    await submit();
    expect(raw.mock.calls.map((c) => c[0])).toEqual(["api/sessions/new/restore"]);
    expect(closed).toBe(1);
  });

  it("別の作業コピーが持つブランチは新しいブランチ名を付けて送る", async () => {
    api.mockResolvedValue({
      name: "app@feat-z",
      path: DIR,
      parent: "app",
      candidates: [{ source: "local", branch: "feat-z", sha: "abc", in_use: "app@holder" }],
    });
    apiJSON.mockResolvedValue({ branch: "feat-z-2" });
    await render([sess("s")]);
    const input = document.querySelector<HTMLInputElement>(".ui-field input")!;
    expect(input.value).toBe("feat-z-2");
    expect(document.body.textContent).toContain("app@holder");
    await submit();
    expect(apiJSON).toHaveBeenCalledWith("api/repos/app%40feat-z/recreate", "POST", {
      source: "local",
      branch: "feat-z",
      new_branch: "feat-z-2",
    });
  });

  it("Agent の置き場所と違うパスは作り直さない", async () => {
    api.mockResolvedValue({
      name: "app@feat-z",
      path: "/elsewhere/repos/app@feat-z",
      parent: "app",
      candidates: [{ source: "local", branch: "feat-z", sha: "abc" }],
    });
    await render([sess("s")]);
    const btn = document.querySelector<HTMLButtonElement>(".ui-modal-foot button[type=submit]")!;
    expect(btn.disabled).toBe(true);
    expect(document.querySelector(".rwt-error")).not.toBeNull();
  });

  it("作り直しの拒否は err カタログの文で出し、先へ進まない", async () => {
    api.mockResolvedValue({
      name: "app@feat-z",
      path: DIR,
      parent: "app",
      candidates: [{ source: "new", branch: "feat-z", ref: "main" }],
    });
    apiJSON.mockResolvedValue({ error: { code: "recreate_stale", message: "x" } });
    await render([sess("s")]);
    await submit();
    expect(changed).toBe(0);
    expect(document.querySelector(".rwt-session")).toBeNull();
    // What was shown no longer resolves: the candidates are fetched again to choose from.
    expect(api).toHaveBeenCalledTimes(2);
  });

  it("アーカイブの見出しに作り直しを出すのは、消えた worktree フォルダの群だけ", () => {
    expect(recreatableGroup(DIR, [sess("a"), sess("b")])).toBe(true);
    // One session still resumable: the folder is there.
    expect(recreatableGroup(DIR, [sess("a"), sess("b", { resumable: true })])).toBe(false);
    // Not a worktree folder, no folder at all, or nothing in it.
    expect(recreatableGroup("/home/dev/repos/app", [sess("a")])).toBe(false);
    expect(recreatableGroup("", [sess("a")])).toBe(false);
    expect(recreatableGroup(DIR, [])).toBe(false);
  });
});
