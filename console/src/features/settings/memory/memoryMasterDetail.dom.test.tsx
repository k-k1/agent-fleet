// The Memories master-detail (#1985, #2324): rows with author / uses / pin, search, filter chips, the
// detail pane with body, forget and unforget through the existing revert route (commit in the query for the audit), and the save form
// (#1997) with its secret refusal. Withheld memories are a count, never a name.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { MemoriesMasterDetail, resetMemoriesSession } from "./memoryMasterDetail.tsx";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { useSettingsUI } from "../store.ts";
import { memoryTarget } from "./memorySearch.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let readKinds: string[] = [];
const onChanged = vi.fn();
const onOpenChanges = vi.fn();
const { t } = await import("../../../lib/i18n/index.ts");

const project = { id: "app-0123456789ab", root: "/r/app", vcs: "git", display: "app" };
const entries = [
  {
    name: "rules",
    scope: "user",
    description: "house rules",
    type: "feedback",
    updated: "2026-10-01T00:00:00Z",
    pinned: true,
    uses: 7,
    revision: 3,
    authorKind: "claude",
    authorSession: "claude-main",
  },
  {
    name: "build-cmd",
    scope: "project",
    project,
    description: "how to build",
    type: "project",
    updated: "2026-10-02T00:00:00Z",
    pinned: false,
    uses: 2,
    revision: 1,
    authorKind: "member",
    authorSession: "console",
  },
];
const changes = [
  {
    commit: "livecommit1",
    at: "2026-10-02T00:00:00Z",
    op: "create",
    scope: "project",
    project,
    name: "build-cmd",
    authorKind: "member",
    authorSession: "console",
    latest: true,
    live: true,
    revertible: true,
  },
  {
    commit: "livecommit0",
    at: "2026-10-01T00:00:00Z",
    op: "create",
    scope: "user",
    name: "rules",
    authorKind: "claude",
    authorSession: "claude-main",
    latest: true,
    live: true,
    revertible: true,
  },
  {
    commit: "abcdef1234",
    at: "2026-10-03T00:00:00Z",
    op: "forget",
    scope: "project",
    project,
    name: "old-note",
    authorKind: "member",
    authorSession: "console",
    latest: true,
    live: false,
    revertible: true,
  },
];

const flush = () =>
  act(async () => {
    for (let i = 0; i < 4; i++) await Promise.resolve();
  });
const confirmButton = () => document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:last-child");
const click = async (b: Element | null | undefined) => {
  expect(b).toBeTruthy();
  await act(async () => (b as HTMLButtonElement).click());
  await flush();
};
const text = (label: string) =>
  Array.from(document.body.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === label);
// The memory list proper; the forgotten list below it reuses the row class.
const listRows = () => Array.from(document.body.querySelectorAll(".mem-br-list")[0].querySelectorAll(".mem-br-row"));
const calls0 = (prefix: string) => api.mock.calls.filter((c) => String(c[0]).startsWith(prefix));
const btn = (key: Parameters<typeof t>[0]) => text(t(key));
// Pick the i-th row of the live list (its select button, not an action).
const pickRow = (i: number) => click(listRows()[i].querySelector(".mem-md-pick"));
const chip = (key: Parameters<typeof t>[0]) => click(btn(key));
const select = () => document.body.querySelector<HTMLSelectElement>(".mem-br-bar select")!;
const remount = async () => {
  await act(async () => root!.unmount());
  root = createRoot(host!);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <MemoriesMasterDetail reload={0} onChanged={onChanged} onOpenChanges={onOpenChanges} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
};

async function setValue(el: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement, v: string) {
  const proto = Object.getPrototypeOf(el);
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  });
}

beforeEach(async () => {
  readKinds = [];
  api.mockImplementation(async (url: string) => {
    if (url.startsWith("api/agents/memory/entries/list")) return { entries, withheld: 2 };
    if (url.startsWith("api/agents/memory/entries/member-forgotten")) {
      const u = new URL("http://x/" + url);
      const sc = u.searchParams.get("scope");
      const pid = u.searchParams.get("project");
      return {
        forgotten: changes.filter(
          (c) => c.latest && !c.live && c.revertible && (!sc || (c.scope === sc && (sc === "user" || c.project?.id === pid))),
        ),
      };
    }
    if (url.startsWith("api/agents/memory/entries/member-latest")) {
      const u = new URL("http://x/" + url);
      const c = changes.find((ch) => ch.name === u.searchParams.get("name") && ch.latest);
      return c ?? { error: { code: "memory_not_found", message: "none" } };
    }
    if (url.startsWith("api/agents/memory/entries/member-search"))
      return { hits: [{ ...entries[0], snippets: ["the zebra rule"] }] };
    if (url.startsWith("api/agents/memory/entries/member-read"))
      return { ...entries[0], kinds: readKinds, body: "always run the tests" };
    return {};
  });
  apiJSON.mockResolvedValue({ name: "x" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <MemoriesMasterDetail reload={0} onChanged={onChanged} onOpenChanges={onOpenChanges} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  onChanged.mockReset();
  onOpenChanges.mockReset();
  resetMemoriesSession();
  document.body.innerHTML = "";
});

describe("MemoriesMasterDetail", () => {
  it("opens on all scopes, tagging each row with its scope or project", () => {
    expect(select().value).toBe("all");
    const rows = listRows();
    expect(rows.map((r) => r.querySelector(".mem-af-name")!.textContent)).toEqual(["rules", "build-cmd"]);
    expect(rows[0].querySelector(".mem-br-where")!.textContent).toMatch(/User-wide|ユーザー共通/);
    expect(rows[1].querySelector(".mem-br-where")!.textContent).toBe("app");
  });

  it("lists the selected scope with author, uses and pin, and only a count for withheld ones", async () => {
    await setValue(select(), "user");
    const rows = listRows();
    expect(rows).toHaveLength(1);
    expect(rows[0].querySelector(".mem-br-where")).toBeNull();
    expect(rows[0].textContent).toContain("rules");
    expect(rows[0].textContent).toContain("claude · claude-main");
    expect(rows[0].textContent).toContain("7");
    expect(rows[0].textContent).toMatch(/pinned|ピン留め/);
    expect(document.body.textContent).toMatch(/2 /);
  });

  it("searches name, description, type and author", async () => {
    const input = document.body.querySelector<HTMLInputElement>('input[type="search"]')!;
    await setValue(input, "no-such");
    expect(listRows()).toHaveLength(0);
    await setValue(input, "HOUSE");
    expect(listRows()).toHaveLength(1);
    // The filter runs over every scope: a project-scoped memory matches too.
    await setValue(input, "how to build");
    expect(listRows()[0].querySelector(".mem-af-name")!.textContent).toBe("build-cmd");
    await setValue(input, "claude-main");
    expect(listRows()).toHaveLength(1);
  });

  it("asks the body search after a pause and shows the matching lines of a memory the list fields miss", async () => {
    const input = document.body.querySelector<HTMLInputElement>('input[type="search"]')!;
    const searchCalls = () => api.mock.calls.filter((c) => String(c[0]).startsWith("api/agents/memory/entries/member-search"));
    await setValue(input, "zeb");
    await setValue(input, "zebra");
    // Nothing is asked while the user is still typing, and the list fields match nothing yet.
    expect(searchCalls()).toHaveLength(0);
    expect(listRows()).toHaveLength(0);
    await act(async () => {
      await new Promise((r) => setTimeout(r, 400));
    });
    await flush();
    expect(searchCalls()).toHaveLength(1);
    expect(searchCalls()[0][0]).toBe("api/agents/memory/entries/member-search?limit=50&q=zebra");
    const rows = listRows();
    expect(rows).toHaveLength(1);
    expect(rows[0].textContent).toContain("rules");
    expect(rows[0].textContent).toContain("the zebra rule");
    // Clearing the box brings the plain list back and drops the snippets.
    await setValue(input, "");
    expect(listRows()[0].textContent).not.toContain("the zebra rule");
  });

  it("orders body-only hits by the server's rank, after the instant field matches", async () => {
    const more = [
      ...entries,
      { ...entries[0], name: "second", description: "other", authorKind: "codex", authorSession: "c1" },
      { ...entries[0], name: "third", description: "other", authorKind: "codex", authorSession: "c2" },
    ];
    const base = api.getMockImplementation()!;
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries: more };
      if (url.startsWith("api/agents/memory/entries/member-search"))
        return { hits: [{ ...more[3], snippets: ["zebra 3"] }, { ...more[2], snippets: ["zebra 2"] }, { ...more[0], snippets: ["zebra 1"] }] };
      return base(url);
    });
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <MemoriesMasterDetail reload={0} onChanged={onChanged} onOpenChanges={onOpenChanges} />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    await flush();
    await setValue(document.body.querySelector<HTMLInputElement>('input[type="search"]')!, "zebra");
    await act(async () => {
      await new Promise((r) => setTimeout(r, 400));
    });
    await flush();
    const names = listRows().map((r) => r.querySelector(".mem-af-name")!.textContent);
    // listed order is rules, second, third; the server ranked third, second, rules
    expect(names).toEqual(["third", "second", "rules"]);
  });

  it("returns project-scoped body hits for a query under the default selection, without a scope param", async () => {
    const base = api.getMockImplementation()!;
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/member-search"))
        return { hits: [{ ...entries[1], snippets: ["compile with zebra"] }] };
      return base(url);
    });
    await setValue(document.body.querySelector<HTMLInputElement>('input[type="search"]')!, "zebra");
    await act(async () => {
      await new Promise((r) => setTimeout(r, 400));
    });
    await flush();
    const calls = api.mock.calls.filter((c) => String(c[0]).startsWith("api/agents/memory/entries/member-search"));
    expect(calls).toHaveLength(1);
    expect(calls[0][0]).not.toContain("scope=");
    const rows = listRows();
    expect(rows).toHaveLength(1);
    expect(rows[0].textContent).toContain("build-cmd");
    expect(rows[0].textContent).toContain("compile with zebra");
    expect(rows[0].querySelector(".mem-br-where")!.textContent).toBe("app");
  });

  it("scopes the body search to the user scope when it is chosen", async () => {
    await setValue(select(), "user");
    await setValue(document.body.querySelector<HTMLInputElement>('input[type="search"]')!, "zebra");
    await act(async () => {
      await new Promise((r) => setTimeout(r, 400));
    });
    expect(api).toHaveBeenCalledWith("api/agents/memory/entries/member-search?scope=user&limit=50&q=zebra");
  });

  it("scopes the body search to the selected project", async () => {
    await setValue(select(), "project:" + project.id);
    await setValue(document.body.querySelector<HTMLInputElement>('input[type="search"]')!, "zebra");
    await act(async () => {
      await new Promise((r) => setTimeout(r, 400));
    });
    expect(api).toHaveBeenCalledWith(
      `api/agents/memory/entries/member-search?scope=project&project=${project.id}&limit=50&q=zebra`,
    );
  });

  it("shows a body from the member-read route", async () => {
    await pickRow(0);
    expect(api).toHaveBeenCalledWith(
      "api/agents/memory/entries/member-read?scope=user&project=&name=rules",
    );
    expect(document.body.querySelector(".mem-br-body")!.textContent).toBe("always run the tests");
  });

  it("forgets from the list through the revert route on the memory's newest change", async () => {
    await setValue(select(), "project:" + project.id);
    expect(listRows()[0].textContent).toContain("build-cmd");
    await pickRow(0);
    await click(btn("mem.af_forget"));
    await click(confirmButton());
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/revert?commit=livecommit1", "POST", {
      commit: "livecommit1",
      forget: true,
      ack: false,
      expectRevision: 1,
    });
  });

  it("forgets a memory whose newest change is outside the loaded changes, resolving it on demand", async () => {
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries };
      if (url.startsWith("api/agents/memory/entries/member-latest"))
        return { ...changes[1], commit: "oldcommit9" };
      return {};
    });
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <MemoriesMasterDetail reload={0} onChanged={onChanged} onOpenChanges={onOpenChanges} />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    await flush();
    await pickRow(0);
    const forgetBtn = btn("mem.af_forget")!;
    expect(forgetBtn.disabled).toBe(false);
    await click(forgetBtn);
    expect(api).toHaveBeenCalledWith("api/agents/memory/entries/member-latest?scope=user&project=&name=rules");
    await click(confirmButton());
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/revert?commit=oldcommit9", "POST", {
      commit: "oldcommit9",
      forget: true,
      ack: false,
      expectRevision: 3,
    });
  });

  it("does not forget when no change can be resolved", async () => {
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries };
      if (url.startsWith("api/agents/memory/entries/member-latest"))
        return { error: { code: "memory_not_found", message: "no published change" } };
      return {};
    });
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <MemoriesMasterDetail reload={0} onChanged={onChanged} onOpenChanges={onOpenChanges} />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    await flush();
    await pickRow(0);
    await click(btn("mem.af_forget"));
    expect(confirmButton()).toBeNull();
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("unforgets a forgotten memory through the revert route", async () => {
    await setValue(select(), "project:" + project.id);
    await chip("mem.md_f_forgotten");
    expect(document.body.textContent).toContain("old-note");
    const restore = Array.from(document.body.querySelectorAll<HTMLButtonElement>(".mem-br-row button")).at(-1);
    expect(restore?.textContent).toBe(t("mem.br_unforget"));
    expect(restore?.textContent).toMatch(/^(Unforget|忘却を取り消す)$/);
    await click(restore);
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/revert?commit=abcdef1234", "POST", {
      commit: "abcdef1234",
      forget: false,
      ack: false,
    });
  });

  it("says Unforget in the toasts, and names the Changes view's Revert this change when the text trips the scan", async () => {
    await setValue(select(), "project:" + project.id);
    await chip("mem.md_f_forgotten");
    const unforget = () => Array.from(document.body.querySelectorAll<HTMLButtonElement>(".mem-br-row button")).at(-1);
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_secret_detected", message: "x" } });
    await click(unforget());
    expect(document.body.textContent).toContain(t("mem.br_unforget_secret"));
    expect(t("mem.br_unforget_secret")).toContain(t("mem.af_revert"));
    apiJSON.mockResolvedValueOnce({});
    await click(unforget());
    expect(document.body.textContent).toContain(t("mem.br_unforgotten", { name: "old-note" }));
  });

  it("opens the purge dialog for a forgotten memory and asks for its preview, calling no write", async () => {
    await setValue(select(), "project:" + project.id);
    await chip("mem.md_f_forgotten");
    const purge = Array.from(document.body.querySelectorAll<HTMLButtonElement>(".mem-br-row button")).at(-2);
    expect(purge?.textContent).toMatch(/Purge history|履歴から消去/);
    await click(purge);
    expect(api).toHaveBeenCalledWith(expect.stringContaining("api/agents/memory/entries/purge-preview?scope=project"));
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("saves an edit at the revision it read, and shows secret findings without saving", async () => {
    await pickRow(0);
    await click(text("Edit") ?? text("編集"));
    const body = document.body.querySelector<HTMLTextAreaElement>("textarea")!;
    await setValue(body, "new text");
    apiJSON.mockResolvedValueOnce({
      error: { code: "memory_secret_detected", message: "x" },
      findings: [{ path: "body", line: 1, rule: "aws-key", hint: "AKIA…" }],
    });
    await click(document.body.querySelector<HTMLButtonElement>('button[type="submit"]'));
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/member-save", "POST", {
      scope: "user",
      project: "",
      name: "rules",
      description: "house rules",
      type: "feedback",
      kinds: [],
      body: "new text",
      revision: 3,
    });
    expect(document.body.querySelector(".mem-findings")!.textContent).toContain("aws-key");
    expect(document.body.querySelector("form")).toBeTruthy();
  });

  it("creates a memory in the selected project at revision 0, with the project id in the query", async () => {
    await setValue(select(), "project:" + project.id);
    await click(text("New memory") ?? text("新しいメモリ"));
    const inputs = document.body.querySelectorAll<HTMLInputElement>("form input");
    await setValue(inputs[0], "fresh");
    await setValue(inputs[1], "a fact");
    await setValue(document.body.querySelector<HTMLTextAreaElement>("textarea")!, "body");
    await click(document.body.querySelector<HTMLButtonElement>('button[type="submit"]'));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/agents/memory/entries/member-save?project=app-0123456789ab",
      "POST",
      expect.objectContaining({ scope: "project", project: project.id, name: "fresh", revision: 0 }),
    );
    expect(document.body.querySelector("form")).toBeNull();
  });

  it("creates a new memory in the user scope while every scope is shown", async () => {
    await click(text("New memory") ?? text("新しいメモリ"));
    await click(document.body.querySelector<HTMLButtonElement>('button[type="submit"]'));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/agents/memory/entries/member-save",
      "POST",
      expect.objectContaining({ scope: "user", project: "", revision: 0 }),
    );
  });

  it("saves an edit to the scope it was opened in, even if the selector moved", async () => {
    await pickRow(0);
    await click(text("Edit") ?? text("編集"));
    await setValue(select(), "project:" + project.id);
    await click(document.body.querySelector<HTMLButtonElement>('button[type="submit"]'));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/agents/memory/entries/member-save",
      "POST",
      expect.objectContaining({ scope: "user", project: "", name: "rules", revision: 3 }),
    );
  });

  it("keeps the kinds of the memory when its body is edited", async () => {
    readKinds = ["claude"];
    await pickRow(0);
    await click(text("Edit") ?? text("編集"));
    await click(document.body.querySelector<HTMLButtonElement>('button[type="submit"]'));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/agents/memory/entries/member-save",
      "POST",
      expect.objectContaining({ kinds: ["claude"] }),
    );
  });

  it("asks for the forgotten memories of the scope on screen, not the recent changes", async () => {
    expect(api).toHaveBeenCalledWith("api/agents/memory/entries/member-forgotten");
    // The only changes read is the per-memory history's own page, never what the forgotten list is built from.
    expect(calls0("api/agents/memory/entries/changes").map((c) => c[0])).toEqual(["api/agents/memory/entries/changes?limit=300"]);
    await chip("mem.md_f_forgotten");
    await setValue(select(), "user");
    expect(api).toHaveBeenCalledWith("api/agents/memory/entries/member-forgotten?scope=user");
    expect(document.body.textContent).not.toContain("old-note");
    await setValue(select(), "project:" + project.id);
    expect(api).toHaveBeenCalledWith(
      `api/agents/memory/entries/member-forgotten?scope=project&project=${encodeURIComponent(project.id)}`,
    );
    expect(document.body.textContent).toContain("old-note");
  });

  it("counts withheld forgotten memories and offers the next page when the list is cut", async () => {
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries };
      if (url.startsWith("api/agents/memory/entries/member-forgotten"))
        return { forgotten: [changes[2]], withheld: 3, truncated: true, next: "cur1" };
      return {};
    });
    await remount();
    await chip("mem.md_f_forgotten");
    const warns = Array.from(document.body.querySelectorAll(".mem-warn")).map((e) => e.textContent ?? "");
    expect(warns.some((w) => w.includes("3"))).toBe(true);
    expect(document.body.textContent).toContain("old-note");
    expect(btn("mem.br_forgotten_more")).toBeTruthy();
  });

  it("pages the forgotten list with the cursor and never repeats a row", async () => {
    const older = { ...changes[2], commit: "oldercommit", name: "older-note" };
    const again = { ...changes[2], commit: "newercommit2" }; // forgotten again since page one
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries };
      if (url === "api/agents/memory/entries/member-forgotten") return { forgotten: [changes[2]], projects: [project], next: "cur 1|x" };
      if (url === "api/agents/memory/entries/member-forgotten?before=cur%201%7Cx")
        return { forgotten: [again, older], projects: [project] };
      return {};
    });
    await remount();
    await chip("mem.md_f_forgotten");
    await click(btn("mem.br_forgotten_more"));
    expect(api).toHaveBeenCalledWith("api/agents/memory/entries/member-forgotten?before=cur%201%7Cx");
    const names = Array.from(document.body.querySelectorAll(".mem-br-list")[0].querySelectorAll(".mem-af-name")).map((e) => e.textContent);
    expect(names).toEqual(["old-note", "older-note"]);
    // The list is complete now: no more button.
    expect(btn("mem.br_forgotten_more")).toBeUndefined();
  });

  it("adds the cursor after the scope parameters when a scoped forgotten list is paged", async () => {
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries };
      if (url === "api/agents/memory/entries/member-forgotten?scope=user") return { forgotten: [], next: "c2" };
      return {};
    });
    await remount();
    await chip("mem.md_f_forgotten");
    await setValue(select(), "user");
    await click(btn("mem.br_forgotten_more"));
    expect(api).toHaveBeenCalledWith("api/agents/memory/entries/member-forgotten?scope=user&before=c2");
  });

  it("requires every word of a multi-word query and says when only some words matched", async () => {
    const input = document.body.querySelector<HTMLInputElement>('input[type="search"]')!;
    // "house" is in rules, "build" in build-cmd: no single row holds both words in its fields.
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries };
      if (url.startsWith("api/agents/memory/entries/member-search"))
        return { hits: [{ ...entries[0], snippets: ["house rules here"] }], fallback: "any" };
      return {};
    });
    await remount();
    const input2 = document.body.querySelector<HTMLInputElement>('input[type="search"]')!;
    await setValue(input2, "house build");
    expect(listRows()).toHaveLength(0);
    await act(async () => {
      await new Promise((r) => setTimeout(r, 400));
    });
    await flush();
    expect(listRows()).toHaveLength(1);
    expect(document.body.textContent).toContain(t("mem.br_search_partial"));
    // An exact match among the fields hides the partial hits and the notice.
    await setValue(input2, "house rules");
    await act(async () => {
      await new Promise((r) => setTimeout(r, 400));
    });
    await flush();
    expect(listRows().map((r) => r.querySelector(".mem-af-name")!.textContent)).toEqual(["rules"]);
    expect(document.body.textContent).not.toContain(t("mem.br_search_partial"));
    void input;
  });

  it("keeps offering a project that holds only forgotten memories, even when its rows were cut or another scope is selected", async () => {
    const userRow = { ...changes[2], scope: "user", project: undefined, name: "u-note", commit: "cu" };
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries: [] };
      if (url.startsWith("api/agents/memory/entries/member-forgotten")) {
        // All scopes: the user row fills the answer, the project's row did not fit but is named.
        if (!url.includes("?")) return { forgotten: [userRow], projects: [project], truncated: true };
        return { forgotten: [userRow], projects: [] };
      }
      return {};
    });
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <MemoriesMasterDetail reload={0} onChanged={onChanged} onOpenChanges={onOpenChanges} />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    await flush();
    const options = () => Array.from(select().options).map((o) => o.value);
    expect(options()).toEqual(["all", "user", "project:" + project.id]);
    await setValue(select(), "user");
    expect(options()).toEqual(["all", "user", "project:" + project.id]);
    await setValue(select(), "project:" + project.id);
    expect(select().value).toBe("project:" + project.id);
  });

  it("says the forgotten list is incomplete when the Agent ran out of checking budget", async () => {
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries };
      if (url.startsWith("api/agents/memory/entries/member-forgotten")) return { forgotten: [], projects: [], withheld: 1, incomplete: true };
      return {};
    });
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <MemoriesMasterDetail reload={0} onChanged={onChanged} onOpenChanges={onOpenChanges} />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    await flush();
    await chip("mem.md_f_forgotten");
    const warns = Array.from(document.body.querySelectorAll(".mem-warn")).map((e) => e.textContent ?? "");
    expect(warns.some((w) => w.includes("minimum") || w.includes("最小値"))).toBe(true);
    expect(warns.some((w) => w.includes("Only the newest") || w.includes("新しい順"))).toBe(false);
  });

  it("drops another scope's forgotten rows at once when the scope changes, before the new list arrives", async () => {
    let release: (v: unknown) => void = () => {};
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries };
      if (url === "api/agents/memory/entries/member-forgotten") return { forgotten: [changes[2]], projects: [project] };
      if (url.startsWith("api/agents/memory/entries/member-forgotten?scope=user"))
        return new Promise((resolve) => {
          release = resolve;
        });
      return {};
    });
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <MemoriesMasterDetail reload={0} onChanged={onChanged} onOpenChanges={onOpenChanges} />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    await flush();
    await chip("mem.md_f_forgotten");
    expect(document.body.textContent).toContain("old-note");
    await setValue(select(), "user");
    expect(api).toHaveBeenCalledWith("api/agents/memory/entries/member-forgotten?scope=user");
    // The user scope's answer is still pending: the project's row and its Unforget are gone already.
    expect(document.body.textContent).not.toContain("old-note");
    expect(text(t("mem.br_unforget"))).toBeUndefined();
    await act(async () => release({ forgotten: [], projects: [project] }));
    await flush();
    expect(document.body.textContent).not.toContain("old-note");
  });
});

// The master-detail itself (#2324): chips, the detail pane, pin, history, overflow purge, edit in
// the pane, the phone flow, keyboard, and the refresh after each write.
describe("MemoriesMasterDetail master-detail", () => {
  const history = [
    { ...changes[1], commit: "h3", op: "update", at: "2026-10-05T00:00:00Z" },
    { ...changes[1], commit: "h2", op: "create", at: "2026-10-01T00:00:00Z" },
    { ...changes[0], commit: "other1" },
  ];
  const calls = (prefix: string) => api.mock.calls.filter((c) => String(c[0]).startsWith(prefix));
  const detail = () => document.body.querySelector<HTMLElement>(".mem-md-detail")!;
  const root_ = () => document.body.querySelector<HTMLElement>(".mem-md")!;
  const withChanges = () => {
    const base = api.getMockImplementation()!;
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/changes")) return { changes: history };
      if (url.startsWith("api/agents/memory/entries/diff")) return { diff: "diff --git a/x b/x\n+added line\n" };
      return base(url);
    });
  };
  it("filters with the chips: All and Pinned list live memories, Forgotten lists the forgotten ones", async () => {
    expect(listRows().map((r) => r.querySelector(".mem-af-name")!.textContent)).toEqual(["rules", "build-cmd"]);
    expect(btn("mem.md_f_all")!.getAttribute("aria-pressed")).toBe("true");
    await chip("mem.md_f_pinned");
    expect(btn("mem.md_f_pinned")!.getAttribute("aria-pressed")).toBe("true");
    expect(listRows().map((r) => r.querySelector(".mem-af-name")!.textContent)).toEqual(["rules"]);
    expect(listRows()[0].querySelector(".mem-md-pinned")).toBeTruthy();
    await chip("mem.md_f_forgotten");
    expect(document.body.querySelector(".mem-br-list")!.textContent).toContain("old-note");
    expect(document.body.querySelector(".mem-br-list")!.textContent).not.toContain("rules");
    await chip("mem.md_f_all");
    expect(document.body.querySelector(".mem-br-list")!.textContent).not.toContain("old-note");
  });

  it("marks a pinned row in the list even under All", () => {
    expect(listRows()[0].querySelector(".mem-md-pinned")).toBeTruthy();
    expect(listRows()[1].querySelector(".mem-md-pinned")).toBeNull();
  });

  it("shows the body and metadata of the picked memory in the detail pane, not below the list", async () => {
    expect(root_().getAttribute("data-pane")).toBe("list");
    await pickRow(0);
    expect(root_().getAttribute("data-pane")).toBe("detail");
    expect(detail().querySelector("h5")!.textContent).toBe("rules");
    expect(detail().querySelector(".mem-br-body")!.textContent).toBe("always run the tests");
    const meta = detail().querySelector(".mem-md-meta")!.textContent!;
    expect(meta).toContain("claude · claude-main");
    expect(meta).toContain("7");
    expect(meta).toContain("feedback");
    expect(document.body.querySelector(".mem-md-list")!.contains(document.body.querySelector(".mem-br-body"))).toBe(false);
    expect(listRows()[0].querySelector(".mem-md-pick")!.getAttribute("aria-current")).toBe("true");
  });

  it("moves focus to the detail heading on pick", async () => {
    await pickRow(0);
    expect(document.activeElement).toBe(detail().querySelector("h5"));
  });

  it("toggles the pin from the detail pane for the right scope and project, and refreshes", async () => {
    await pickRow(1);
    const pin = btn("mem.pin_pin")!;
    expect(pin.getAttribute("aria-pressed")).toBe("false");
    const listCalls = calls("api/agents/memory/entries/list").length;
    await click(pin);
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/pin?project=app-0123456789ab", "POST", {
      scope: "project",
      project: "app-0123456789ab",
      name: "build-cmd",
      pinned: true,
    });
    expect(onChanged).toHaveBeenCalled();
    expect(calls("api/agents/memory/entries/list").length).toBeGreaterThan(listCalls);
  });

  it("unpins a user-scope memory without a project", async () => {
    await pickRow(0);
    expect(btn("mem.pin_unpin")!.getAttribute("aria-pressed")).toBe("true");
    await click(btn("mem.pin_unpin"));
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/pin", "POST", {
      scope: "user",
      project: "",
      name: "rules",
      pinned: false,
    });
  });

  it("lists this memory's own changes and shows an entry's diff with the Changes diff view", async () => {
    withChanges();
    await remount();
    await pickRow(0);
    const items = Array.from(detail().querySelectorAll(".mem-md-hist-list button"));
    // Only "rules" (user scope) entries, not the other memory's.
    expect(items).toHaveLength(2);
    expect(calls("api/agents/memory/entries/changes")[0][0]).toBe("api/agents/memory/entries/changes?limit=300");
    expect(detail().querySelector(".mem-md-history .mem-diff")).toBeNull();
    await click(items[0]);
    expect(api).toHaveBeenCalledWith("api/agents/memory/entries/diff?commit=h3");
    expect(detail().querySelector(".mem-md-history .mem-diff")!.textContent).toContain("added line");
    await click(btn("mem.md_hist_all"));
    expect(onOpenChanges).toHaveBeenCalledTimes(1);
  });

  it("says the history is cut and loads older pages by cursor until it ends", async () => {
    const full = Array.from({ length: 300 }, (_, i) => ({ ...changes[1], commit: "c" + i }));
    const older = [{ ...changes[1], commit: "older0" }];
    const base = api.getMockImplementation()!;
    api.mockImplementation(async (url: string) => {
      if (url === "api/agents/memory/entries/changes?limit=300") return { changes: full, next: "c299" };
      if (url === "api/agents/memory/entries/changes?limit=300&before=c299") return { changes: older };
      return base(url);
    });
    await remount();
    await pickRow(0);
    expect(detail().querySelector(".mem-md-history")!.textContent).toContain("300");
    expect(detail().querySelectorAll(".mem-md-hist-list button")).toHaveLength(300);
    await click(btn("mem.md_hist_more"));
    expect(detail().querySelectorAll(".mem-md-hist-list button")).toHaveLength(301);
    // Nothing older: the cut note and the button go.
    expect(btn("mem.md_hist_more")).toBeUndefined();
    expect(detail().querySelector(".mem-md-history")!.textContent).not.toContain(t("mem.md_hist_capped", { n: 300 }));
  });

  it("purges from the overflow menu through the existing purge dialog", async () => {
    await pickRow(0);
    expect(btn("mem.pg_button")).toBeUndefined();
    await click(detail().querySelector('[aria-haspopup="menu"]'));
    const item = document.body.querySelector<HTMLElement>('[role="menuitem"]')!;
    expect(item.textContent).toBe(t("mem.pg_button"));
    await click(item);
    expect(api).toHaveBeenCalledWith(expect.stringContaining("api/agents/memory/entries/purge-preview?scope=user"));
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("edits in the detail pane and keeps the memory picked after the save, refreshing everything", async () => {
    await pickRow(0);
    await click(btn("mem.br_edit"));
    expect(detail().querySelector("form")).toBeTruthy();
    expect(document.body.querySelector(".mem-md-list form")).toBeNull();
    expect(document.activeElement).toBe(detail().querySelector("h5"));
    await setValue(detail().querySelector("textarea")!, "changed");
    const reads = calls("api/agents/memory/entries/member-read").length;
    const lists = calls("api/agents/memory/entries/list").length;
    await click(detail().querySelector('button[type="submit"]'));
    expect(detail().querySelector("form")).toBeNull();
    expect(detail().querySelector("h5")!.textContent).toBe("rules");
    expect(onChanged).toHaveBeenCalled();
    expect(calls("api/agents/memory/entries/list").length).toBeGreaterThan(lists);
    expect(calls("api/agents/memory/entries/member-read").length).toBeGreaterThan(reads);
  });

  it("opens New in the detail pane and picks the new memory once saved", async () => {
    await click(btn("mem.br_new"));
    expect(root_().getAttribute("data-pane")).toBe("detail");
    expect(detail().querySelector("form")).toBeTruthy();
    const inputs = detail().querySelectorAll<HTMLInputElement>("form input");
    await setValue(inputs[0], "fresh");
    const base = api.getMockImplementation()!;
    // The re-read list does not contain it yet: the pick must survive until it does.
    api.mockImplementation(async (url: string) =>
      url.startsWith("api/agents/memory/entries/member-read") ? { ...entries[0], name: "fresh", body: "b" } : base(url),
    );
    await click(detail().querySelector('button[type="submit"]'));
    expect(detail().querySelector("h5")!.textContent).toBe("fresh");
  });

  it("refreshes the list, the forgotten list and the status after forgetting, and leaves the detail", async () => {
    await pickRow(0);
    const lists = calls("api/agents/memory/entries/list").length;
    const gones = calls("api/agents/memory/entries/member-forgotten").length;
    await click(btn("mem.af_forget"));
    await click(confirmButton());
    expect(apiJSON).toHaveBeenCalled();
    expect(onChanged).toHaveBeenCalled();
    expect(calls("api/agents/memory/entries/list").length).toBeGreaterThan(lists);
    expect(calls("api/agents/memory/entries/member-forgotten").length).toBeGreaterThan(gones);
    expect(root_().getAttribute("data-pane")).toBe("list");
    expect(detail().querySelector(".mem-br-body")).toBeNull();
  });

  it("unforgets from the detail pane of a forgotten memory and refreshes", async () => {
    await setValue(select(), "project:" + project.id);
    await chip("mem.md_f_forgotten");
    await click(document.body.querySelector(".mem-md-pick"));
    expect(detail().querySelector("h5")!.textContent).toBe("old-note");
    const lists = calls("api/agents/memory/entries/list").length;
    expect(detail().querySelector(".mem-br-actions button")!.textContent).toBe(t("mem.br_unforget"));
    await click(detail().querySelector(".mem-br-actions button"));
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/revert?commit=abcdef1234", "POST", {
      commit: "abcdef1234",
      forget: false,
      ack: false,
    });
    expect(onChanged).toHaveBeenCalled();
    expect(calls("api/agents/memory/entries/list").length).toBeGreaterThan(lists);
    expect(root_().getAttribute("data-pane")).toBe("list");
  });

  it("refreshes the list, the detail and the status after a purge", async () => {
    const base = api.getMockImplementation()!;
    api.mockImplementation(async (url: string) =>
      url.startsWith("api/agents/memory/entries/purge-preview")
        ? { scope: "user", name: "rules", live: true, keepsCurrent: true, nothing: false, changes: [], changesTotal: 2, snapshots: 0, refs: [], findings: [], plan: "p1" }
        : base(url),
    );
    await pickRow(0);
    await click(detail().querySelector('[aria-haspopup="menu"]'));
    await click(document.body.querySelector('[role="menuitem"]'));
    await setValue(document.body.querySelector<HTMLInputElement>(".mem-purge input")!, "rules");
    const lists = calls("api/agents/memory/entries/list").length;
    const reads = calls("api/agents/memory/entries/member-read").length;
    await click(Array.from(document.body.querySelectorAll<HTMLButtonElement>(".mem-purge button")).find((b) => b.textContent === t("mem.pg_run")));
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/purge", "POST", expect.objectContaining({ name: "rules", plan: "p1" }));
    expect(onChanged).toHaveBeenCalled();
    expect(calls("api/agents/memory/entries/list").length).toBeGreaterThan(lists);
    expect(calls("api/agents/memory/entries/member-read").length).toBeGreaterThan(reads);
  });

  it("drops the detail when the scope changes away from the picked memory", async () => {
    await pickRow(0);
    expect(root_().getAttribute("data-pane")).toBe("detail");
    await setValue(select(), "project:" + project.id);
    expect(root_().getAttribute("data-pane")).toBe("list");
    expect(detail().querySelector(".mem-br-body")).toBeNull();
  });

  it("drops the detail when the picked memory disappears from the list", async () => {
    await pickRow(0);
    const base = api.getMockImplementation()!;
    api.mockImplementation(async (url: string) =>
      url.startsWith("api/agents/memory/entries/list") ? { entries: [entries[1]] } : base(url),
    );
    await act(async () => root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <MemoriesMasterDetail reload={1} onChanged={onChanged} />
        </ConfirmProvider>
      </ToastProvider>,
    ));
    await flush();
    expect(root_().getAttribute("data-pane")).toBe("list");
  });

  it("goes list -> detail -> back on a phone, and Back focuses the row it came from", async () => {
    await pickRow(1);
    expect(root_().getAttribute("data-pane")).toBe("detail");
    await click(detail().querySelector(".mem-md-back"));
    expect(root_().getAttribute("data-pane")).toBe("list");
    expect(document.activeElement).toBe(listRows()[1].querySelector(".mem-md-pick"));
  });

  it("returns to the list on Escape in the detail pane, without closing the dialog", async () => {
    await pickRow(0);
    const outer = vi.fn();
    document.addEventListener("keydown", outer);
    await act(async () => {
      detail().querySelector("h5")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    document.removeEventListener("keydown", outer);
    expect(root_().getAttribute("data-pane")).toBe("list");
    expect(outer).not.toHaveBeenCalled();
  });

  it("keeps an open edit form on Escape", async () => {
    await pickRow(0);
    await click(btn("mem.br_edit"));
    await act(async () => {
      detail().querySelector("textarea")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(detail().querySelector("form")).toBeTruthy();
  });

  it("remembers the scope, filter and pick across a remount of the view, not beyond a reset", async () => {
    await chip("mem.md_f_pinned");
    await pickRow(0);
    await remount();
    expect(btn("mem.md_f_pinned")!.getAttribute("aria-pressed")).toBe("true");
    expect(root_().getAttribute("data-pane")).toBe("detail");
    expect(detail().querySelector("h5")!.textContent).toBe("rules");
    resetMemoriesSession();
    await remount();
    expect(btn("mem.md_f_all")!.getAttribute("aria-pressed")).toBe("true");
    expect(root_().getAttribute("data-pane")).toBe("list");
  });

  it("drops an opened diff when a purge removes that entry from the history", async () => {
    withChanges();
    const base = api.getMockImplementation()!;
    let purged = false;
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/changes") && purged) return { changes: [history[0]] };
      if (url.startsWith("api/agents/memory/entries/purge-preview"))
        return { scope: "user", name: "rules", live: true, keepsCurrent: true, nothing: false, changes: [], changesTotal: 1, snapshots: 0, refs: [], findings: [], plan: "p1" };
      return base(url);
    });
    apiJSON.mockImplementation(async () => {
      purged = true;
      return {};
    });
    await remount();
    await pickRow(0);
    await click(detail().querySelectorAll(".mem-md-hist-list button")[1]);
    expect(detail().querySelector(".mem-md-history .mem-diff")).toBeTruthy();
    await click(detail().querySelector('[aria-haspopup="menu"]'));
    await click(document.body.querySelector('[role="menuitem"]'));
    await setValue(document.body.querySelector<HTMLInputElement>(".mem-purge input")!, "rules");
    await click(Array.from(document.body.querySelectorAll<HTMLButtonElement>(".mem-purge button")).find((b) => b.textContent === t("mem.pg_run")));
    expect(detail().querySelectorAll(".mem-md-hist-list button")).toHaveLength(1);
    expect(detail().querySelector(".mem-md-history .mem-diff")).toBeNull();
  });

  it("keeps a project scope across a remount, even while the lists are still loading", async () => {
    await setValue(select(), "project:" + project.id);
    const base = api.getMockImplementation()!;
    let release: (v: unknown) => void = () => {};
    api.mockImplementation(async (url: string) =>
      url.startsWith("api/agents/memory/entries/list")
        ? new Promise((r) => {
            release = r;
          })
        : base(url),
    );
    await remount();
    // The project is not a candidate until the list arrives; the scope must not fall back meanwhile.
    await act(async () => release({ entries }));
    await flush();
    expect(select().value).toBe("project:" + project.id);
  });

  it("reads the changes once however many memories are picked, and again after a write", async () => {
    withChanges();
    await remount();
    const n = () => calls("api/agents/memory/entries/changes").length;
    const start = n();
    await pickRow(0);
    await pickRow(1);
    await pickRow(0);
    expect(n()).toBe(start);
    await click(btn("mem.pin_unpin"));
    expect(n()).toBeGreaterThan(start);
  });

  it("drops a pick that the Pinned chip no longer lists after it is unpinned", async () => {
    await chip("mem.md_f_pinned");
    await pickRow(0);
    const base = api.getMockImplementation()!;
    api.mockImplementation(async (url: string) =>
      url.startsWith("api/agents/memory/entries/list")
        ? { entries: [{ ...entries[0], pinned: false }, entries[1]] }
        : base(url),
    );
    await click(btn("mem.pin_unpin"));
    expect(root_().getAttribute("data-pane")).toBe("list");
    expect(listRows()).toHaveLength(0);
  });

  it("clears an unpinned pick when switching to the Pinned chip", async () => {
    await pickRow(1);
    await chip("mem.md_f_pinned");
    expect(root_().getAttribute("data-pane")).toBe("list");
  });
});

// The command palette names one memory through openSettings' target (#2407): the view picks it
// once its list has landed, whatever scope and filter were left on screen, and takes the request.
describe("MemoriesMasterDetail settings target", () => {
  const picked = () => document.body.querySelector("[aria-current=true]");

  it("picks the memory a target names, resetting the scope and filter that would hide it", async () => {
    await setValue(select(), "user");
    await chip("mem.md_f_pinned");
    expect(listRows()).toHaveLength(1);
    await act(async () => useSettingsUI.setState({ settingsTarget: memoryTarget(entries[1]) }));
    await flush();
    expect(select().value).toBe("all");
    expect(picked()?.textContent).toContain("build-cmd");
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
  });

  it("drops a target that is not listed, leaving the view as it was", async () => {
    await act(async () => useSettingsUI.setState({ settingsTarget: memoryTarget({ ...entries[0], name: "gone" }) }));
    await flush();
    expect(picked()).toBeNull();
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
  });

  it("leaves a target that names something else for its owner", async () => {
    await act(async () => useSettingsUI.setState({ settingsTarget: "codex" }));
    await flush();
    expect(useSettingsUI.getState().settingsTarget).toBe("codex");
    useSettingsUI.setState({ settingsTarget: null });
  });
});
