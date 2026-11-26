// The Sessions view's Spaces section (ADR 0142 decision 9): absent while no session runs in a
// space, grouped by space once one does, and named from GET /api/spaces.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

const LIST = { root: "/home/dev/spaces", spaces: [{ folder: "q3-abcdef", name: "Q3 report", dir: "/home/dev/spaces/q3-abcdef" }] };
let spacesAnswer: () => unknown = () => LIST;
const api = vi.fn(async (url: string): Promise<unknown> => (url.startsWith("api/spaces") ? spacesAnswer() : {}));
let tenant = "";
let patchAnswer: (folder: string, body: { name: string }) => unknown = () => ({});
let deleteAnswer: (folder: string) => unknown = () => ({ deleted: "x" });
const apiJSON = vi.fn(async (path: string, method: string, body?: unknown): Promise<unknown> => {
  const m = /^api\/spaces\/([^/]+)$/.exec(path);
  if (m && method === "DELETE") return deleteAnswer(decodeURIComponent(m[1]));
  return m && method === "PATCH" ? patchAnswer(decodeURIComponent(m[1]), body as { name: string }) : {};
});

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: (path: string, method: string, body?: unknown) => apiJSON(path, method, body),
  errText: (e: { message?: string } | string) => (typeof e === "string" ? e : (e?.message ?? "error")),
  api: (url: string) => api(url),
  isTransientErr: (d: { error?: { status?: number } }) => (d?.error?.status ?? 0) >= 500,
  getTenant: () => tenant,
  getUser: () => "",
}));

const { SpacesSection } = await import("./SpacesSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useSpacesStore } = await import("../spaces/store.ts");
const { useProjectFilter } = await import("./filter.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const flush = () =>
  act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });

// The section asks from a timer (0 ms the first time): let it run.
const tick = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 5));
  });

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <SpacesSection />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
  if (!vi.isFakeTimers()) await tick();
}

const sess = (name: string, extra: Partial<Session> = {}): Session => ({ name, kind: "claude", alive: true, ...extra });
const spaceCalls = () => api.mock.calls.filter(([u]) => u === "api/spaces").length;
const inSpace = (name: string, folder: string) => sess(name, { repo: folder, dir: "/home/dev/spaces/" + folder, space: folder });
const heads = () => [...host.querySelectorAll(".space-group-name")].map((e) => e.textContent);

beforeEach(() => {
  localStorage.clear();
  api.mockClear();
  apiJSON.mockClear();
  patchAnswer = () => ({});
  deleteAnswer = () => ({ deleted: "x" });
  spacesAnswer = () => LIST;
  tenant = "";
  useSpacesStore.getState().clear();
  useProjectFilter.setState({ q: "" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("Spaces section", () => {
  it("is absent while no session runs in a space, and asks for nothing", async () => {
    useSessionsStore.setState({ sessions: [sess("a", { repo: "app" }), sess("b")] });
    await render();
    expect(host.querySelector(".ui-section")).toBeNull();
    expect(api).not.toHaveBeenCalledWith("api/spaces");
  });

  it("groups space sessions under the space's display name", async () => {
    useSessionsStore.setState({
      sessions: [
        sess("s1", { repo: "q3-abcdef", dir: "/home/dev/spaces/q3-abcdef", space: "q3-abcdef" }),
        sess("s2", { repo: "q3-abcdef", dir: "/home/dev/spaces/q3-abcdef", space: "q3-abcdef" }),
        sess("s3", { repo: "unnamed-zz", dir: "/home/dev/spaces/unnamed-zz", space: "unnamed-zz" }),
        sess("r1", { repo: "app" }),
      ],
    });
    await render();
    expect(api).toHaveBeenCalledWith("api/spaces");
    // A space the list does not name shows its folder.
    expect(heads().sort()).toEqual(["Q3 report", "unnamed-zz"]);
    expect(host.querySelectorAll("[role=group] [role=treeitem]").length).toBe(3);
  });

  it("asks again after a transient failure, and shows the names once it answers", async () => {
    vi.useFakeTimers();
    try {
      spacesAnswer = () => ({ error: { code: "http_502", status: 502 } });
      useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef")] });
      await render();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(heads()).toEqual(["q3-abcdef"]);
      expect(spaceCalls()).toBe(1);
      spacesAnswer = () => LIST;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_000);
      });
      await flush();
      expect(spaceCalls()).toBe(2);
      expect(heads()).toEqual(["Q3 report"]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("names nothing from another workspace's list while the new one is asked for", async () => {
    useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef")] });
    await render();
    expect(heads()).toEqual(["Q3 report"]);
    const before = spaceCalls();
    // The other tenant's Agent knows the same folder by another name, and answers late.
    let answer: (v: unknown) => void = () => {};
    spacesAnswer = () => new Promise((r) => (answer = r));
    tenant = "other";
    await act(async () => {
      useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef"), inSpace("s2", "q3-abcdef")] });
    });
    await tick();
    expect(spaceCalls()).toBeGreaterThan(before);
    // Asked, not answered: the old tenant's name must not stand in for the folder.
    expect(heads()).toEqual(["q3-abcdef"]);
    await act(async () => {
      answer({ ...LIST, spaces: [{ ...LIST.spaces[0], name: "Other tenant's" }] });
    });
    await flush();
    expect(heads()).toEqual(["Other tenant's"]);
  });

  it("does not carry another workspace's failures into the wait", async () => {
    // Two failures for the first tenant would make the next ask wait 4 s.
    useSpacesStore.setState({ failures: 2, failOwner: "\n" });
    tenant = "other";
    useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef")] });
    await render();
    expect(spaceCalls()).toBe(1);
    expect(heads()).toEqual(["Q3 report"]);
  });

  it("matches the search box against the space's name", async () => {
    useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef"), inSpace("s2", "zz-abcdef")] });
    await render();
    await act(async () => {
      useProjectFilter.setState({ q: "q3 rep" });
    });
    expect(heads()).toEqual(["Q3 report"]);
    expect(host.querySelectorAll("[role=group] [role=treeitem]").length).toBe(1);
  });

  it("names the tree, and arrow keys move between a space and its sessions", async () => {
    useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef"), inSpace("s2", "q3-abcdef")] });
    await render();
    const tree = host.querySelector<HTMLElement>("[role=tree]")!;
    expect(tree.getAttribute("aria-label")).toBeTruthy();
    const rows = [...tree.querySelectorAll<HTMLElement>("[data-rail-row]")];
    // The space row first, then its two sessions.
    expect(rows.length).toBe(3);
    expect(rows[0].getAttribute("role")).toBe("treeitem");
    expect(rows[0].textContent).toContain("Q3 report");
    const key = (el: HTMLElement, k: string) =>
      act(() => {
        el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true }));
      });
    rows[0].focus();
    await key(rows[0], "ArrowDown");
    expect(document.activeElement).toBe(rows[1]);
    await key(rows[1], "ArrowDown");
    expect(document.activeElement).toBe(rows[2]);
    await key(rows[2], "ArrowLeft");
    expect(document.activeElement).toBe(rows[0]);
  });

  it("has each space row own the group of its sessions", async () => {
    useSessionsStore.setState({
      sessions: [inSpace("s1", "q3-abcdef"), inSpace("s2", "q3-abcdef"), inSpace("s3", "notes-ghijkl")],
    });
    await render();
    const parents = [...host.querySelectorAll<HTMLElement>("[role=tree] [aria-expanded]")];
    expect(parents.length).toBe(2);
    const owned = parents.map((p) => {
      const id = p.getAttribute("aria-owns") ?? "";
      const group = id ? document.getElementById(id) : null;
      expect(group?.getAttribute("role")).toBe("group");
      return [...group!.querySelectorAll("[role=treeitem]")].length;
    });
    // Two ids, one per space, each naming that space's own sessions.
    expect(new Set(parents.map((p) => p.getAttribute("aria-owns"))).size).toBe(2);
    expect(owned.sort()).toEqual([1, 2]);
  });

  // ADR 0142 decision 10: the Sessions view lists no shell, so a space that only has shells keeps
  // its row for the count chip that leads to them.
  describe("a space with shells", () => {
    const shellIn = (name: string, folder: string): Session => sess(name, { kind: "shell", repo: folder, dir: "/home/dev/spaces/" + folder, space: folder });

    it("keeps the row of a space that only has shells, with the chip, and lists no shell", async () => {
      useSessionsStore.setState({ sessions: [shellIn("sh1", "q3-abcdef"), shellIn("sh2", "q3-abcdef")] });
      await render();
      expect(heads()).toEqual(["Q3 report"]);
      expect(host.querySelector(".term-chip")?.textContent).toBe("2");
      expect(host.querySelectorAll(".sess-row").length).toBe(0);
      // The chip leads to the group of that space.
      const { useTerminalReveal } = await import("../terminals/store.ts");
      act(() => host.querySelector<HTMLButtonElement>(".term-chip")!.click());
      expect(useTerminalReveal.getState().key).toBe("shell:space:q3-abcdef");
    });

    it("puts the chip on a space that also has agent sessions, and finds the space by its name only", async () => {
      useSessionsStore.setState({ sessions: [inSpace("a1", "q3-abcdef"), shellIn("sh1", "q3-abcdef"), shellIn("sh2", "notes-ghijkl")] });
      await render();
      expect(heads().sort()).toEqual(["Q3 report", "notes-ghijkl"]);
      expect(host.querySelectorAll(".term-chip").length).toBe(2);
      expect(host.querySelectorAll(".sess-row").length).toBe(1);
      // The search box matches the shown name, not a shell's own title.
      await act(async () => useProjectFilter.setState({ q: "notes" }));
      expect(heads()).toEqual(["notes-ghijkl"]);
      await act(async () => useProjectFilter.setState({ q: "shell title" }));
      expect(host.querySelector(".ui-section")).toBeNull();
    });
  });

  describe("rename", () => {
    const open = async () => {
      useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef")] });
      await render();
      const btn = host.querySelector<HTMLButtonElement>(".space-group-rename");
      expect(btn).not.toBeNull();
      // A real click focuses the button first; jsdom's click() does not.
      await act(async () => {
        btn!.focus();
        btn!.click();
      });
    };
    const dialog = () => document.querySelector<HTMLElement>(".ui-modal, [role=dialog]");
    const input = () => document.querySelector<HTMLInputElement>("input[type=text]")!;
    const type = async (v: string) => {
      await act(async () => {
        // React tracks the value setter: go through the prototype's, as a user's typing does.
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input(), v);
        input().dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    const submit = async () => {
      await act(async () => {
        input().form!.requestSubmit();
      });
      await flush();
    };

    it("opens on the space's own name and sends the new one for that folder only", async () => {
      patchAnswer = (folder, body) => {
        // The Agent's list names the space the new way from now on.
        spacesAnswer = () => ({ root: LIST.root, spaces: [{ ...LIST.spaces[0], folder, name: body.name }] });
        return { space: { ...LIST.spaces[0], folder, name: body.name } };
      };
      await open();
      expect(dialog()).not.toBeNull();
      expect(input().value).toBe("Q3 report");
      await type("  Q4 plan  ");
      await submit();
      expect(apiJSON).toHaveBeenCalledWith("api/spaces/q3-abcdef", "PATCH", { name: "Q4 plan" });
      expect(dialog()).toBeNull();
      expect(heads()).toEqual(["Q4 plan"]);
    });

    it("sends an empty name as it is, so the folder shows again", async () => {
      patchAnswer = (folder, body) => {
        spacesAnswer = () => ({ root: LIST.root, spaces: [{ ...LIST.spaces[0], folder, name: body.name }] });
        return { space: { ...LIST.spaces[0], folder, name: body.name } };
      };
      await open();
      await type("");
      await submit();
      expect(apiJSON).toHaveBeenCalledWith("api/spaces/q3-abcdef", "PATCH", { name: "" });
      expect(heads()).toEqual(["q3-abcdef"]);
    });

    it("keeps the dialog and the old name when the Agent refuses", async () => {
      patchAnswer = () => ({ error: { code: "bad_name", message: "name is too long" } });
      await open();
      await type("x");
      await submit();
      expect(dialog()).not.toBeNull();
      expect(input().value).toBe("x");
      expect(heads()).toEqual(["Q3 report"]);
    });

    it("keeps the pencil off until the list names the space, so an unknown name is never saved as empty", async () => {
      let answer: (v: unknown) => void = () => {};
      spacesAnswer = () => new Promise((r) => (answer = r));
      useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef")] });
      await render();
      const btn = () => host.querySelector<HTMLButtonElement>(".space-group-rename")!;
      expect(btn().disabled).toBe(true);
      await act(async () => btn().click());
      expect(dialog()).toBeNull();
      await act(async () => answer(LIST));
      await flush();
      expect(btn().disabled).toBe(false);
      await act(async () => btn().click());
      expect(input().value).toBe("Q3 report");
    });

    it("closes a dialog left open across a workspace switch, and sends nothing to the new one", async () => {
      await open();
      expect(dialog()).not.toBeNull();
      await type("Renamed");
      tenant = "other";
      await act(async () => {
        useSpacesStore.getState().clear();
      });
      await flush();
      expect(dialog()).toBeNull();
      expect(apiJSON).not.toHaveBeenCalled();
    });

    it("drops the answer of a rename that was in flight when the workspace changed", async () => {
      let answer: (v: unknown) => void = () => {};
      patchAnswer = () => new Promise((r) => (answer = r));
      await open();
      await type("Renamed");
      const listCalls = spaceCalls();
      await act(async () => {
        input().form!.requestSubmit();
      });
      tenant = "other";
      await act(async () => {
        useSpacesStore.getState().clear();
        answer({ error: { code: "space_not_found", message: "no such space" } });
      });
      await flush();
      // No refusal about the old workspace's folder is shown in the new one, and its list is not read for it.
      expect(document.body.textContent).not.toContain("This space no longer exists");
      expect(document.body.textContent).not.toContain("no such space");
      expect(spaceCalls() - listCalls).toBeLessThanOrEqual(1);
    });

    it("counts characters, not UTF-16 units: 100 astral characters fit, 101 do not", async () => {
      patchAnswer = (folder, body) => ({ space: { ...LIST.spaces[0], folder, name: body.name } });
      await open();
      await type("😀".repeat(100));
      const save = () => [...document.querySelectorAll<HTMLButtonElement>("button[type=submit]")].at(-1)!;
      expect(save().disabled).toBe(false);
      await submit();
      expect(apiJSON).toHaveBeenCalledWith("api/spaces/q3-abcdef", "PATCH", { name: "😀".repeat(100) });
    });

    it("refuses 101 characters before sending", async () => {
      await open();
      await type("a".repeat(101));
      const save = [...document.querySelectorAll<HTMLButtonElement>("button[type=submit]")].at(-1)!;
      expect(save.disabled).toBe(true);
      await submit();
      expect(apiJSON).not.toHaveBeenCalled();
    });

    it("words the Agent's refusals in the dialog and returns the focus to the pencil on Escape", async () => {
      patchAnswer = () => ({ error: { code: "space_not_found", message: "no such space" } });
      await open();
      await type("x");
      await submit();
      expect(document.body.textContent).toContain("This space no longer exists");
      await act(async () => {
        document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      });
      expect(dialog()).toBeNull();
      expect(document.activeElement).toBe(host.querySelector(".space-group-rename"));
    });

    it("does not bring the dialog back when the first workspace is on screen again", async () => {
      await open();
      expect(dialog()).not.toBeNull();
      tenant = "other";
      await act(async () => {
        useSpacesStore.getState().clear();
      });
      await flush();
      expect(dialog()).toBeNull();
      // Back in the first workspace, whose list is still on its way: nobody pressed the pencil.
      spacesAnswer = () => new Promise(() => {});
      tenant = "";
      await act(async () => {
        useSpacesStore.getState().clear();
      });
      await flush();
      expect(dialog()).toBeNull();
      expect(apiJSON).not.toHaveBeenCalled();
    });

    it("says nothing about a network failure of a request made for another workspace", async () => {
      let fail: (e: Error) => void = () => {};
      patchAnswer = () => new Promise((_, rej) => (fail = rej));
      await open();
      await type("Renamed");
      await act(async () => {
        input().form!.requestSubmit();
      });
      tenant = "other";
      await act(async () => {
        useSpacesStore.getState().clear();
        fail(new Error("offline"));
      });
      await flush();
      expect(document.body.textContent).not.toContain("network error");
      expect(dialog()).toBeNull();
    });

    it("tells a network failure in this workspace, and shows the Agent's own text under a heading for other failures", async () => {
      patchAnswer = () => Promise.reject(new Error("offline"));
      await open();
      await type("x");
      await submit();
      expect(document.body.textContent).toContain("network error");
      expect(dialog()).not.toBeNull();
      patchAnswer = () => ({ error: { code: "rename_failed", message: "permission denied" } });
      await submit();
      expect(document.body.textContent).toContain("Couldn't rename the space. permission denied");
    });
  });
  describe("delete", () => {
    const shell = (name: string, folder: string): Session => ({ ...inSpace(name, folder), kind: "shell", alive: false });
    const open = async (sessions: Session[]) => {
      useSessionsStore.setState({ sessions });
      await render();
      const btn = host.querySelector<HTMLButtonElement>(".space-group-delete");
      expect(btn).not.toBeNull();
      await act(async () => {
        btn!.focus();
        btn!.click();
      });
    };
    const dialog = () => document.querySelector<HTMLElement>(".ui-modal, [role=dialog]");
    const confirmBtn = () => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Move to trash")!;
    const press = async () => {
      await act(async () => confirmBtn().click());
      await flush();
    };

    it("says what goes with the space, and offers no press while one of its sessions runs", async () => {
      await open([inSpace("a1", "q3-abcdef"), { ...inSpace("a2", "q3-abcdef"), alive: false }, shell("sh1", "q3-abcdef")]);
      const text = dialog()!.textContent!;
      expect(text).toContain("Q3 report");
      expect(text).toContain("~/spaces/q3-abcdef");
      expect(text).toContain("2 AI session(s)");
      expect(text).toContain("1 shell / SSM session(s)");
      // A running session blocks it: the press would only be refused.
      expect(confirmBtn().disabled).toBe(true);
      expect(apiJSON).not.toHaveBeenCalled();
    });

    it("sends the delete for that folder once nothing runs, and drops the space from the list", async () => {
      deleteAnswer = () => ({ deleted: "q3-abcdef", archive: "a1" });
      await open([{ ...inSpace("a1", "q3-abcdef"), alive: false }, shell("sh1", "q3-abcdef")]);
      expect(confirmBtn().disabled).toBe(false);
      spacesAnswer = () => ({ root: LIST.root, spaces: [] });
      await press();
      expect(apiJSON).toHaveBeenCalledTimes(1);
      // The names it showed go with it, so the Agent settles nothing beyond them.
      expect(apiJSON).toHaveBeenCalledWith(`api/spaces/q3-abcdef?expect=${encodeURIComponent("a1,sh1")}`, "DELETE", undefined);
      expect(dialog()).toBeNull();
      expect(useSpacesStore.getState().spaces).toEqual([]);
      expect(document.body.textContent).toContain("Moved the space \"Q3 report\" to the trash.");
    });

    it("names the sessions that are running or locked, and offers no press while any is", async () => {
      await open([
        { ...inSpace("run1", "q3-abcdef"), title: "Runner" },
        { ...inSpace("lock1", "q3-abcdef"), alive: false, locked: true, title: "Keeper" },
      ]);
      const text = dialog()!.textContent!;
      expect(text).toContain("Runner");
      expect(text).toContain("Keeper");
      expect(confirmBtn().disabled).toBe(true);
    });

    it("counts a session started in a subfolder, which the Agent settles too", async () => {
      await open([
        { ...inSpace("a1", "q3-abcdef"), alive: false },
        // Not named by `space` (the folder is not directly under ~/spaces), but at or under the space.
        sess("sub1", { kind: "shell", alive: false, dir: "/home/dev/spaces/q3-abcdef/notes", repo: "notes" }),
        sess("other", { kind: "shell", alive: false, dir: "/home/dev/spaces/q3-abcdef-other" }),
      ]);
      expect(dialog()!.textContent).toContain("1 AI session(s)");
      expect(dialog()!.textContent).toContain("1 shell / SSM session(s)");
    });

    it("lifts the block when the running session is stopped elsewhere", async () => {
      await open([inSpace("a1", "q3-abcdef"), { ...inSpace("a2", "q3-abcdef"), alive: false }]);
      expect(confirmBtn().disabled).toBe(true);
      await act(async () => {
        useSessionsStore.setState({
          sessions: [
            { ...inSpace("a1", "q3-abcdef"), alive: false },
            { ...inSpace("a2", "q3-abcdef"), alive: false },
          ],
        });
      });
      expect(confirmBtn().disabled).toBe(false);
      expect(dialog()!.textContent).toContain("2 AI session(s)");
    });

    it("waits for a second look when the sessions in the space change while the dialog is open", async () => {
      await open([{ ...inSpace("a1", "q3-abcdef"), alive: false }]);
      expect(confirmBtn().disabled).toBe(false);
      // A shell opened elsewhere and already stopped: the Agent would trash it too.
      await act(async () => {
        useSessionsStore.setState({ sessions: [{ ...inSpace("a1", "q3-abcdef"), alive: false }, shell("sh9", "q3-abcdef")] });
      });
      expect(confirmBtn().disabled).toBe(true);
      expect(dialog()!.textContent).toContain("The sessions in this space changed");
      expect(dialog()!.textContent).toContain("1 AI session(s)");
      await act(async () => confirmBtn().click());
      expect(apiJSON).not.toHaveBeenCalled();
      // Looking again shows the new list and allows the press.
      const review = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Show the current list")!;
      await act(async () => review.click());
      expect(dialog()!.textContent).toContain("1 shell / SSM session(s)");
      expect(confirmBtn().disabled).toBe(false);
    });

    it("offers no press while the space's location is unknown, in either order", async () => {
      // The list names the space but not where it is: a subfolder session could be missed.
      spacesAnswer = () => ({ root: LIST.root, spaces: [{ folder: "q3-abcdef", name: "Q3 report" }] });
      await open([{ ...inSpace("a1", "q3-abcdef"), alive: false }]);
      expect(confirmBtn().disabled).toBe(true);
      expect(dialog()!.textContent).toContain("Couldn't read where this space is");
      // Reading it again brings the location, and the press.
      spacesAnswer = () => LIST;
      const reload = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Read it again")!;
      await act(async () => reload.click());
      await flush();
      expect(confirmBtn().disabled).toBe(false);
      // And the location lost after the dialog opened blocks it again rather than accepting a short list.
      await act(async () => {
        useSpacesStore.setState({ spaces: [{ folder: "q3-abcdef", name: "Q3 report", dir: "" }] });
      });
      expect(confirmBtn().disabled).toBe(true);
      expect(apiJSON).not.toHaveBeenCalled();
    });

    it("keeps the dialog and words the Agent's refusal in the space's own terms", async () => {
      deleteAnswer = () => ({ error: { code: "space_sessions_running", message: "1 session(s) are running in this space" } });
      await open([{ ...inSpace("a1", "q3-abcdef"), alive: false }]);
      await press();
      expect(dialog()).not.toBeNull();
      expect(document.body.textContent).toContain("Sessions are running in this space. Stop them first.");
      expect(document.body.textContent).not.toContain("working copy");
      deleteAnswer = () => ({ error: { code: "space_trash_failed", message: "x" } });
      await press();
      expect(document.body.textContent).toContain("Couldn't move the space to the trash, so it was not deleted");
      expect(useSpacesStore.getState().spaces.map((x) => x.folder)).toContain("q3-abcdef");
    });

    it("tells a network failure, and shows the Agent's own text for an unknown code", async () => {
      deleteAnswer = () => Promise.reject(new Error("offline"));
      await open([{ ...inSpace("a1", "q3-abcdef"), alive: false }]);
      await press();
      expect(document.body.textContent).toContain("network error");
      deleteAnswer = () => ({ error: { code: "weird", message: "disk on fire" } });
      await press();
      expect(document.body.textContent).toContain("Couldn't delete the space. disk on fire");
      expect(dialog()).not.toBeNull();
    });

    it("closes a dialog left open across a workspace switch, and sends nothing to the new one", async () => {
      await open([{ ...inSpace("a1", "q3-abcdef"), alive: false }]);
      expect(dialog()).not.toBeNull();
      tenant = "other";
      await act(async () => {
        useSpacesStore.getState().clear();
      });
      await flush();
      expect(dialog()).toBeNull();
      expect(apiJSON).not.toHaveBeenCalled();
    });

    it("says nothing about an answer that arrives after a workspace switch", async () => {
      let answer: (v: unknown) => void = () => {};
      deleteAnswer = () => new Promise((r) => (answer = r));
      await open([{ ...inSpace("a1", "q3-abcdef"), alive: false }]);
      await act(async () => confirmBtn().click());
      tenant = "other";
      await act(async () => {
        useSpacesStore.getState().clear();
        answer({ error: { code: "space_not_found", message: "no such space" } });
      });
      await flush();
      expect(document.body.textContent).not.toContain("This space no longer exists");
      expect(dialog()).toBeNull();
    });
  });
});
