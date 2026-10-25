// The session context menu is built from sections (issue #2189): Run; View & names; Hand over &
// keep; Working sets; Behaviour; Clean up, in that order, one rule between the sections that have
// something to show. Pinned here, over the session kinds and states the other SessionMenu tests
// cover: no section renders empty, no rule is doubled or sits at the top or bottom, the order is
// the same on every surface that mounts the menu, and arrow-key roving steps over the rules.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../gallery/open.ts", () => ({ openGallery: () => {} }));
vi.mock("./open.ts", () => ({
  openSessionFromList: () => true,
  openSessionChat: () => {},
  openSessionChatSplit: () => {},
  openSessionTerminal: () => {},
}));
vi.mock("./useSessionActions.tsx", () => ({
  useSessionActions: () => new Proxy({}, { get: () => async () => {} }),
}));

const { SessionMenu } = await import("./SessionMenu.tsx");
const { SessionLinkMenuHost } = await import("./SessionLinkMenu.tsx");
const { MarkdownView } = await import("../viewer/MarkdownView.tsx");
const { SessionCard } = await import("../overview/SessionCard.tsx");
const { PaneHoverProvider } = await import("../../lib/panehover.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { useSessionsStore } = await import("./store.ts");
const { useReposStore } = await import("../repos/store.ts");
const { setSetting } = await import("../../lib/settings.ts");
type Session = import("../../types/session.ts").Session;
type SessionActions = import("./useSessionActions.tsx").SessionActions;

const actions = new Proxy({}, { get: () => async () => {} }) as SessionActions;

let root: Root;
let host: HTMLDivElement;

const base = (over: Partial<Session> = {}): Session =>
  ({ name: "sk7f3q9", kind: "claude", alive: true, state: "idle", title: "t", ...over }) as Session;

const renderMenu = async (s: Session) => {
  await act(async () => {
    root.render(
      <ToastProvider>
        <SessionMenu s={s} actions={actions} running open place={() => {}} onClose={() => {}} />
      </ToastProvider>,
    );
  });
};
const renderMenuRunning = async (s: Session, running: boolean) => {
  await act(async () => {
    root.render(
      <ToastProvider>
        <SessionMenu s={s} actions={actions} running={running} open place={() => {}} onClose={() => {}} />
      </ToastProvider>,
    );
  });
};

const menuEl = () => document.querySelector<HTMLElement>(".sess-menu")!;
const sectionIds = () => [...menuEl().querySelectorAll<HTMLElement>("[data-menu-section]")].map((e) => e.dataset.menuSection!);
const labels = () => [...menuEl().querySelectorAll<HTMLElement>(".ui-menu-item")].map((e) => e.textContent!.trim());

// The structural rule: the menu's children alternate section, rule, section, … and start and end
// with a section, and every section holds at least one real item.
const expectWellFormed = (what: string) => {
  const kids = [...menuEl().children];
  expect(kids.length, what).toBeGreaterThan(0);
  kids.forEach((k, i) => {
    const isSection = k.hasAttribute("data-menu-section");
    expect(isSection, `${what}: child ${i} should be ${i % 2 === 0 ? "a section" : "a rule"}`).toBe(i % 2 === 0);
    if (isSection) expect(k.querySelectorAll(".ui-menu-item").length, `${what}: empty section`).toBeGreaterThan(0);
    else expect(k.className).toBe("ui-menu-sep");
  });
  expect(kids.length % 2, `${what}: ends with a rule`).toBe(1);
};

// A surface may know less than the bare menu (the workspace's reachability differs), so it can
// show fewer items; what it shows must be those of the full list, in the same order.
const expectSameOrder = (full: { ids: string[]; labels: string[] }) => {
  const subsequence = (sub: string[], all: string[]) => {
    let at = 0;
    return sub.every((x) => (at = all.indexOf(x, at) + 1) > 0);
  };
  expect(subsequence(sectionIds(), full.ids)).toBe(true);
  expect(subsequence(labels(), full.labels)).toBe(true);
  expect(sectionIds().length).toBeGreaterThan(3);
};

const ORDER = ["run", "view", "keep", "wsets", "behaviour", "cleanup"];
const inOrder = (ids: string[]) => ids.every((id, i) => i === 0 || ORDER.indexOf(ids[i - 1]) < ORDER.indexOf(id));

// jsdom lays nothing out, so offsetParent is always null and useMenuRoving would see no visible
// item. Report every attached element as rendered.
const offsetParent = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetParent");
beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, "offsetParent", {
    configurable: true,
    get() {
      return (this as HTMLElement).parentElement;
    },
  });
  localStorage.clear();
  setSetting("workingSets", [] as never);
  useSessionsStore.setState({ sessions: [] });
  useReposStore.setState({ repos: [] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  if (offsetParent) Object.defineProperty(HTMLElement.prototype, "offsetParent", offsetParent);
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

const cases: Array<[string, Partial<Session>, boolean]> = [
  ["alive claude", {}, true],
  ["alive claude, workspace unreachable", {}, false],
  ["stopped claude", { alive: false }, true],
  ["dead claude (folder gone)", { alive: false, resumable: false }, true],
  ["dead claude, workspace unreachable", { alive: false, resumable: false }, false],
  ["stopped ssm", { kind: "ssm", alive: false }, true],
  ["alive shell", { kind: "shell" }, true],
  ["stopped shell", { kind: "shell", alive: false }, true],
  ["dead shell", { kind: "shell", alive: false, resumable: false }, true],
  ["alive codex with remote url", { kind: "codex", remoteUrl: "https://example.test/r" }, true],
  ["locked, keep-awake, stop armed", { locked: true, keepAwakeUntil: new Date(Date.now() + 3_600_000).toISOString(), stopAfterTurnAt: new Date().toISOString() }, true],
  ["worktree session", { worktree: true, repo: "app@wip-sab" }, true],
  ["generated images", { generatedImages: 3, generatedImagesPath: ".cache/g/uuid" }, true],
];

describe("session menu sections", () => {
  for (const withSets of [false, true]) {
    for (const [name, over, running] of cases) {
      it(`${name}${withSets ? " + working sets" : ""}: sections in order, one rule between, none empty`, async () => {
        if (withSets) setSetting("workingSets", [{ id: "w1", name: "Set one", repos: [], sessions: [] }] as never);
        await renderMenuRunning(base(over), running);
        expectWellFormed(name);
        const ids = sectionIds();
        expect(inOrder(ids), `${name}: ${ids.join(",")}`).toBe(true);
        expect(new Set(ids).size).toBe(ids.length);
        // The behaviour and the view sections always have the lock / copy-id items.
        expect(ids).toContain("behaviour");
        expect(ids).toContain("view");
        expect(ids.includes("wsets")).toBe(withSets);
      });
    }
  }

  it("a fully loaded session shows all six sections, with the captions inside their sections", async () => {
    setSetting("workingSets", [{ id: "w1", name: "Set one", repos: [], sessions: [] }] as never);
    await renderMenu(base({ remoteUrl: "https://example.test/r", generatedImages: 1, generatedImagesPath: "p" }));
    expect(sectionIds()).toEqual(ORDER);
    const group = (id: string) => menuEl().querySelector<HTMLElement>(`[data-menu-section=${id}]`)!;
    // The Export caption heads the export rows at the tail of "Hand over & keep"; the working
    // sets section is headed by its own caption.
    expect(group("keep").querySelector(".ui-menu-caption")).not.toBeNull();
    expect(group("wsets").firstElementChild?.className).toBe("ui-menu-caption");
    expect(menuEl().querySelectorAll(".ui-menu-sep")).toHaveLength(5);
  });

  // The issue's membership, pinned by label so moving an item to another section fails here.
  it("puts each item in the section the issue names, in order", async () => {
    setSetting("workingSets", [{ id: "w1", name: "Set one", repos: [], sessions: [] }] as never);
    await renderMenu(
      base({ remoteUrl: "https://example.test/r", generatedImages: 1, generatedImagesPath: "p", worktree: true }),
    );
    const t = (await import("../../lib/i18n/index.ts")).t;
    const inGroup = (id: string) =>
      [...menuEl().querySelectorAll<HTMLElement>(`[data-menu-section=${id}] .ui-menu-item`)].map((e) => e.textContent!.trim());
    expect(inGroup("run")).toEqual([t("srow.stop"), t("srow.open_remote")]);
    expect(inGroup("view")).toEqual([
      t("srow.generated_images", { n: 1 }),
      t("srow.copy_id", { name: "sk7f3q9" }),
      t("srow.rename"),
      t("srow.rename_branch"),
    ]);
    expect(inGroup("keep")).toEqual([
      t("srow.handoff"),
      t("srow.move"),
      t("srow.handoff_member"),
      t("srow.share"),
      t("srow.export_none"),
      t("srow.export_names"),
      t("srow.export_full"),
    ]);
    expect(inGroup("wsets")).toEqual(["Set one"]);
    expect(inGroup("behaviour")).toEqual([
      t("srow.lock"),
      t("srow.keep_awake_on", { hours: 4 }),
      t("srow.stop_after_turn_on"),
      t("srow.budget"),
    ]);
    expect(inGroup("cleanup")).toEqual([t("srow.archive"), t("srow.recreate")]);
  });

  // jsdom computes no layout, so the shrink is pinned at its source: the menu scrolls at
  // max-height as a flex column, and a rule that may shrink collapses to 0px.
  it("keeps the rules from shrinking when the menu scrolls", async () => {
    const { readFileSync } = await import("node:fs");
    const css = readFileSync("src/ui/ui.css", "utf8");
    expect(css).toMatch(/\.ui-menu-sep \{[^}]*flex-shrink: 0;/);
  });

  it("a dead shell keeps only the sections that have items", async () => {
    await renderMenu(base({ kind: "shell", alive: false, resumable: false }));
    expect(sectionIds()).toEqual(["view", "behaviour", "cleanup"]);
    expect(menuEl().querySelectorAll(".ui-menu-sep")).toHaveLength(2);
  });

  it("arrow keys step from section to section without landing on a rule", async () => {
    setSetting("workingSets", [{ id: "w1", name: "Set one", repos: [], sessions: [] }] as never);
    await renderMenu(base());
    const items = [...menuEl().querySelectorAll<HTMLElement>(".ui-menu-item")];
    expect(document.activeElement).toBe(items[0]);
    const seen: Element[] = [];
    for (let i = 1; i < items.length; i++) {
      await act(async () => {
        document.activeElement!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
      });
      seen.push(document.activeElement!);
    }
    // Every press lands on the next item in DOM order, across every section boundary.
    expect(seen).toEqual(items.slice(1));
    expect(seen.every((el) => el.classList.contains("ui-menu-item"))).toBe(true);
    // And it wraps from the last item to the first.
    await act(async () => {
      document.activeElement!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    });
    expect(document.activeElement).toBe(items[0]);
    await act(async () => {
      document.activeElement!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }));
    });
    expect(document.activeElement).toBe(items[items.length - 1]);
  });

  it("opens in the same order on the overview card and on a session link as on the bare menu", async () => {
    const s = base({ name: "sukbq4s", remoteUrl: "https://example.test/r" });
    await renderMenu(s);
    const direct = { ids: sectionIds(), labels: labels() };
    await act(async () => root.render(<></>));

    useSessionsStore.setState({ sessions: [s] });
    await act(async () => {
      root.render(
        <ToastProvider>
          <PaneHoverProvider>
            <SessionCard s={s} opens={[]} beside running waitingAt={0} actions={actions} />
          </PaneHoverProvider>
        </ToastProvider>,
      );
    });
    await act(async () => {
      host.querySelector<HTMLElement>(".ovw-card")!.dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }),
      );
    });
    expectWellFormed("overview card");
    expectSameOrder(direct);
    await act(async () => root.render(<></>));
    document.body.querySelectorAll(".sess-menu").forEach((e) => e.remove());

    await act(async () => {
      root.render(
        <ToastProvider>
          <SessionLinkMenuHost>
            <MarkdownView source="子は sukbq4s で動いている。" />
          </SessionLinkMenuHost>
        </ToastProvider>,
      );
    });
    await act(async () => {
      host.querySelector<HTMLElement>("a.md-session-link")!.dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true, button: 2, clientX: 40, clientY: 50 }),
      );
    });
    expectWellFormed("session link");
    expectSameOrder(direct);
  });
});
