// Rendering tests for the work item rail (docs/log/80 P0). Four points:
//   1. A stopped Workspace still shows the cached rows, with the last-fetched stamp and the
//      stopped note — if this screen does not work the feature does not exist (ADR 0061
//      decision 1).
//   2. A started row carries a badge, which stops a second person picking up the same ticket
//      before they launch.
//   3. No buttons on the row (§80.20): pressing a row opens the detail modal, where the controls
//      live.
//   4. The detail modal's start hands the existing launch stack its seed (prompt, title,
//      branch).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../lib/i18n/index.ts";

const workItemList = vi.fn();
const workItemQueryCreate = vi.fn();
const bitbucketRepoList = vi.fn();
vi.mock("./api.ts", () => ({
  workItemList: (...a: unknown[]) => workItemList(...a),
  workItemRefresh: vi.fn(async () => ({ items: [], queries: [] })),
  workItemQueryCreate: (...a: unknown[]) => workItemQueryCreate(...a),
  workItemQueryUpdate: vi.fn(),
  workItemQueryDelete: vi.fn(),
  workItemSessionCreate: vi.fn(),
  workItemSessionDelete: vi.fn(),
  bitbucketRepoList: (...a: unknown[]) => bitbucketRepoList(...a),
}));

const { WorkItemsSection } = await import("./WorkItemsSection.tsx");
const { useWorkItemStore } = await import("./store.ts");
const { useLaunchSeed, useLaunchTarget, useReposStore } = await import("../repos/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");

const item = (over: Record<string, unknown> = {}) => ({
  id: "1",
  queryId: "q1",
  provider: "github",
  kind: "issue",
  key: "acme/web#45",
  title: "ログイン後に一覧が空になる",
  state: "open",
  url: "https://github.com/acme/web/issues/45",
  assignee: "taro",
  labels: ["bug"],
  repo: "acme/web",
  updatedAt: "2026-08-26T00:00:00Z",
  ...over,
});

const query = { id: "q1", provider: "github", label: "自分の未完了", query: "assignee:@me", repoHint: "", enabled: true, position: 0, fetchedAt: "2026-08-26T09:00:00Z", lastError: "" };

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <WorkItemsSection />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const text = () => host.textContent || "";
const rows = () => host.querySelectorAll(".wi-row").length;
/** Pressing a row opens the detail modal (the path that replaced the row button in §80.20). */
const openRow = async (n = 0) => {
  await act(async () => {
    host.querySelectorAll<HTMLElement>(".wi-row")[n].click();
  });
};
/** The rightmost footer button of the detail modal is start. */
const detailStart = () =>
  [...document.querySelectorAll<HTMLButtonElement>(".wi-dmodal .ui-modal-foot button")].pop()!;

// Typing into a controlled input. `el.value = x` alone leaves React's value tracker thinking
// nothing changed, so onChange never fires — which yields the worst possible outcome, a green
// test over a filter that does nothing. Go through the prototype setter instead.
const typeInto = (el: HTMLInputElement, value: string) => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
};

// Same reason for select: go through the prototype setter (React's value tracker).
const selectInto = (el: HTMLSelectElement, value: string) => {
  Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("change", { bubbles: true }));
};

// Only the three shell elements may be direct children of ui-modal. Anything else here means
// content has fallen outside the container that carries the padding.
const strayChildren = (panel: Element) =>
  [...panel.children]
    .filter((el) => !el.matches(".ui-modal-head, .ui-modal-body, .ui-modal-foot"))
    .map((el) => el.className);

beforeEach(() => {
  workItemList.mockReset();
  workItemQueryCreate.mockReset();
  workItemQueryCreate.mockResolvedValue({ id: "new" });
  bitbucketRepoList.mockReset();
  bitbucketRepoList.mockResolvedValue({ repos: [] });
  useWorkItemStore.getState().reset();
  useLaunchSeed.getState().clear();
  useLaunchTarget.getState().clear();
  useReposStore.setState({ repos: [] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("WorkItemsSection", () => {
  it("shows cached rows while stopped, with the last-fetched stamp and the stopped note", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "2026-08-26T09:00:00Z", running: false });
    await render();
    expect(rows()).toBe(1);
    expect(text()).toContain("ログイン後に一覧が空になる");
    expect(text()).toContain(t("wi.stopped_note"));
    // Never show a possibly stale list without saying when it was fetched.
    expect(host.querySelector(".wi-stamp")?.textContent || "").not.toBe("");
  });

  it("survives a row with null labels (this blanked the whole Console)", async () => {
    // The CP emitted a Go nil slice as JSON null; item.labels.slice(0, 2) in the row threw a
    // TypeError, and with no ErrorBoundary in the app the whole Console disappeared. The producer
    // was fixed, but this pins down that the rail still renders when an older CP sends null.
    workItemList.mockResolvedValue({
      items: [{ ...item(), labels: null }, { ...item(), id: "2", key: "acme/web#46", labels: ["bug"] }],
      queries: [query],
      sessions: [],
      fetchedAt: "2026-08-26T09:00:00Z",
      running: true,
    });
    await render();
    expect(rows()).toBe(2);
    expect(text()).toContain("ログイン後に一覧が空になる");
    expect(text()).toContain("bug");
  });

  it("shows a failed fetch as distinct from empty and keeps the previous rows", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    expect(rows()).toBe(1);
    workItemList.mockResolvedValue({ error: { code: "boom", message: "nope" } });
    await act(async () => {
      await useWorkItemStore.getState().refresh();
    });
    expect(host.querySelector(".wi-err")).not.toBeNull();
    expect(rows()).toBe(1); // the rows stay
    expect(text()).not.toContain(t("wi.empty"));
  });

  it("offers the add path rather than \"empty\" when there is no query at all", async () => {
    workItemList.mockResolvedValue({ items: [], queries: [], sessions: [], fetchedAt: "", running: true });
    await render();
    expect(text()).toContain(t("wi.no_queries"));
    expect(text()).not.toContain(t("wi.empty"));
  });

  it("names the query in a per-query failure", async () => {
    workItemList.mockResolvedValue({
      items: [item()],
      queries: [{ ...query, lastError: "github rejected the token" }],
      sessions: [],
      fetchedAt: "2026-08-26T09:00:00Z",
      running: true,
    });
    await render();
    expect(text()).toContain(t("wi.query_failed", { label: "自分の未完了" }));
    expect(rows()).toBe(1); // the other rows stay visible through a failure
  });

  it("puts a badge on a started row", async () => {
    workItemList.mockResolvedValue({
      items: [item(), item({ id: "2", key: "acme/web#46", title: "まだ誰も見ていない" })],
      queries: [query],
      sessions: [{ id: "l1", provider: "github", itemKey: "acme/web#45", sessionName: "sk7f3q9", repo: "web", branch: "", createdAt: "" }],
      fetchedAt: "2026-08-26T09:00:00Z",
      running: true,
    });
    await render();
    const badges = host.querySelectorAll(".wi-started");
    expect(badges.length).toBe(1);
    expect(badges[0].getAttribute("title")).toContain("sk7f3q9");
  });

  // --- §80.20: no buttons on the row; pressing it opens the detail modal, where controls live ---

  it("puts no start button on the row (41 rows no longer mean 41 buttons)", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({ items: jiraRows(5), queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    expect(host.querySelectorAll(".wi-start").length).toBe(0);
    expect(host.querySelectorAll(".wi-report").length).toBe(0);
    // Only the external link and, when started, the badge stay on the row; the row itself is
    // pressable.
    expect(host.querySelectorAll(".wi-link").length).toBe(5);
    expect(host.querySelector(".wi-row")?.getAttribute("role")).toBe("button");
  });

  it("opens the detail modal on a row press, listing exactly what the CP holds and fetching no body", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await openRow();
    const modal = document.querySelector(".wi-dmodal")!;
    const shown = modal.textContent || "";
    expect(shown).toContain("ログイン後に一覧が空になる");
    expect(shown).toContain("acme/web#45");
    expect(shown).toContain(t("wi.state_open"));
    expect(shown).toContain("@taro");
    expect(shown).toContain("bug");
    expect(modal.querySelector<HTMLAnchorElement>(".wi-dlink")?.href).toBe("https://github.com/acme/web/issues/45");
  });

  it("does not open the detail modal from the row's link or started badge (nested controls)", async () => {
    workItemList.mockResolvedValue({
      items: [item()],
      queries: [query],
      sessions: [{ id: "l1", provider: "github", itemKey: "acme/web#45", sessionName: "sk7f3q9", repo: "web", branch: "", createdAt: "" }],
      fetchedAt: "",
      running: true,
    });
    await render();
    await act(async () => {
      host.querySelector<HTMLElement>(".wi-link")!.click();
    });
    expect(document.querySelector(".wi-dmodal")).toBeNull();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".wi-started")!.click();
    });
    expect(document.querySelector(".wi-dmodal")).toBeNull();
  });

  it("asks where to launch first, because a ticket knows nothing about working copies", async () => {
    useReposStore.setState({
      repos: [
        { name: "web", path: "/home/dev/repos/web" },
        { name: "web@wip-abc", path: "/home/dev/repos/web@wip-abc", worktree: true, parent: "web", branch: "feature/issue-9" },
      ],
    });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await openRow();
    // Opening alone launches nothing and seeds nothing yet, since it can still be cancelled.
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(useLaunchSeed.getState().workItem).toBeNull();
    const selects = document.querySelectorAll<HTMLSelectElement>(".wi-sfield select");
    expect(selects.length).toBe(2);
    // The where options are: a new worktree, the base in place, and the existing worktree.
    expect(selects[1].options.length).toBe(3);
    expect(document.body.textContent || "").toContain("feature/issue-9");
  });

  it("hands the launch stack the base plus a seed when a new worktree is chosen", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await openRow();
    await act(async () => {
      detailStart().click();
    });
    const seed = useLaunchSeed.getState();
    expect(seed.prompt).toContain("acme/web#45");
    expect(seed.prompt).toContain("gh issue view 45");
    expect(seed.prompt).not.toContain(">"); // the body is not pasted by default
    expect(seed.title).toContain("#45");
    expect(seed.workItem).toEqual({ provider: "github", key: "acme/web#45", branch: "feature/issue-45" });
    expect(useLaunchTarget.getState().target?.name).toBe("web");
    expect(useLaunchTarget.getState().inPlace).toBe(false);
  });

  it("passes inPlace for an existing working copy, so the launch dialog does not revert to a new worktree", async () => {
    useReposStore.setState({
      repos: [
        { name: "web", path: "/home/dev/repos/web" },
        { name: "web@wip-abc", path: "/home/dev/repos/web@wip-abc", worktree: true, parent: "web", branch: "feature/issue-9" },
      ],
    });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await openRow();
    const where = document.querySelectorAll<HTMLSelectElement>(".wi-sfield select")[1];
    await act(async () => {
      where.value = "web@wip-abc";
      where.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      detailStart().click();
    });
    expect(useLaunchTarget.getState().target?.name).toBe("web@wip-abc");
    expect(useLaunchTarget.getState().inPlace).toBe(true);
  });

  // Guards against the padding regression. ui-modal itself has no padding (the heading and footer
  // carry their own), so content placed directly in it sticks to the frame — which is what the
  // start hub actually did. Asserting on structure catches it without looking at the rendering.
  it("puts modal content in the shared ui-modal-body / ui-modal-foot, matching the other dialogs", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();

    await openRow();
    const detail = document.querySelector(".wi-dmodal")!;
    expect(detail.querySelector(":scope > .ui-modal-body .wi-sfield")).not.toBeNull();
    expect(detail.querySelector(":scope > .ui-modal-foot button")).not.toBeNull();
    expect(strayChildren(detail)).toEqual([]);

    // The detail modal closes on Esc or cancel; without closing it first, opening the gear would
    // stack two modals.
    await act(async () => {
      [...document.querySelectorAll<HTMLButtonElement>(".wi-dmodal .ui-modal-foot button")][0].click();
    });

    // The saved queries modal (the gear) must have the same shape.
    const gear = [...host.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.getAttribute("aria-label") === t("wi.queries"),
    )!;
    await act(async () => gear.click());
    const queries = document.querySelector(".wi-qmodal")!;
    expect(queries.querySelector(":scope > .ui-modal-body .wi-qform")).not.toBeNull();
    expect(strayChildren(queries)).toEqual([]);
  });

  // --- docs/log/80 §80.18: information design reworked on real data (41 Jira rows, one assignee) ---

  const jiraRows = (n: number) =>
    Array.from({ length: n }, (_, i) =>
      item({
        id: "j" + i,
        queryId: "q1",
        provider: "jira",
        key: `G3M-${100 + i}`,
        title: `課題 ${i}`,
        assignee: "Rin Aoyagi",
        repo: "",
        labels: [],
        // Use an unambiguously old date. Nothing is rendered within 24 hours, so a value near
        // the run date would make the .wi-when count depend on the day the suite runs.
        updatedAt: new Date(Date.UTC(2025, 11, 1, 0, i)).toISOString(),
      }),
    );

  it("draws 10 rows by default even for 41 items, keeps the badge at the full count and names the remainder", async () => {
    workItemList.mockResolvedValue({ items: jiraRows(41), queries: [query], sessions: [], fetchedAt: "2026-08-26T09:00:00Z", running: true });
    await render();
    expect(rows()).toBe(10);
    // Folded, not hidden: the section badge still reads 41.
    expect(host.querySelector(".ui-section-count")?.textContent).toBe("41");
    const more = host.querySelector<HTMLButtonElement>(".wi-more")!;
    expect(more.textContent).toBe(t("wi.show_more", { n: 31 }));
    await act(async () => more.click());
    expect(rows()).toBe(41);
    // Collapsing takes it back, so it does not stay expanded.
    await act(async () => host.querySelector<HTMLButtonElement>(".wi-more")!.click());
    expect(rows()).toBe(10);
  });

  // docs/log/80 §80.20: reported from the real rail — the same JQL saved twice turned 41 items
  // into 82 rows.
  it("keeps one row when two queries return the same ticket, and the badge does not count the duplicate", async () => {
    const q2 = { ...query, id: "q2", provider: "jira" };
    const dup = jiraRows(41).map((r) => ({ ...r, id: r.id + "-dup", queryId: "q2" }));
    workItemList.mockResolvedValue({
      items: [...jiraRows(41), ...dup],
      queries: [query, q2],
      sessions: [],
      fetchedAt: "2026-08-26T09:00:00Z",
      running: true,
    });
    await render();
    expect(host.querySelector(".ui-section-count")?.textContent).toBe("41");
    await act(async () => host.querySelector<HTMLButtonElement>(".wi-more")!.click());
    expect(rows()).toBe(41);
  });

  it("drops an assignee shared by every row, and draws no second line when the meta is empty", async () => {
    workItemList.mockResolvedValue({ items: jiraRows(41), queries: [query], sessions: [], fetchedAt: "2026-08-26T09:00:00Z", running: true });
    await render();
    expect(text()).not.toContain("@Rin Aoyagi");
    expect(host.querySelectorAll(".wi-meta").length).toBe(0);
    // Only the rendering was dropped: the assignee is still in the row's tooltip.
    expect(host.querySelector(".wi-title")?.getAttribute("title")).toContain("Rin Aoyagi");
    // In place of the freed height, the reason for the sort order appears (relative update time).
    expect(host.querySelectorAll(".wi-when").length).toBe(10);
  });

  it("shows the assignee when it varies (a team query)", async () => {
    const mixed = jiraRows(41).map((r, i) => (i === 3 ? { ...r, assignee: "Sora Ueda" } : r));
    workItemList.mockResolvedValue({ items: mixed, queries: [query], sessions: [], fetchedAt: "2026-08-26T09:00:00Z", running: true });
    await render();
    expect(text()).toContain("@Rin Aoyagi");
  });

  it("filters the rail by reducing rows only, filtering before folding", async () => {
    const mixed = [...jiraRows(40), item({ id: "gh", key: "acme/web#45", title: "ログイン後に一覧が空になる" })];
    workItemList.mockResolvedValue({ items: mixed, queries: [query], sessions: [], fetchedAt: "2026-08-26T09:00:00Z", running: true });
    await render();
    const input = host.querySelector<HTMLInputElement>(".wi-filter input")!;
    await act(async () => typeInto(input, "ログイン"));
    expect(rows()).toBe(1);
    expect(host.querySelector(".wi-more")).toBeNull(); // one remaining row needs no fold
    await act(async () => typeInto(input, "存在しない語"));
    expect(rows()).toBe(0);
    expect(text()).toContain(t("wi.filter_empty"));
  });

  it("shows no filter box on a rail that is not crowded", async () => {
    workItemList.mockResolvedValue({ items: jiraRows(4), queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    expect(host.querySelector(".wi-filter")).toBeNull();
    expect(host.querySelector(".wi-more")).toBeNull();
    expect(rows()).toBe(4);
  });

  it("defers to the start hub only when there is no working copy at all", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    await openRow();
    // With nothing to choose, no options are offered and start hands straight over to the clone
    // path.
    expect(document.querySelector(".wi-sfield")).toBeNull();
    await act(async () => {
      detailStart().click();
    });
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(useLaunchSeed.getState().workItem?.key).toBe("acme/web#45");
  });

  it("offers report in the detail modal only once started, swapping to the draft modal on press", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({
      items: [item(), item({ id: "2", key: "acme/web#46", title: "まだ誰も見ていない" })],
      queries: [query],
      sessions: [{ id: "l1", provider: "github", itemKey: "acme/web#45", sessionName: "sk7f3q9", repo: "web", branch: "feature/issue-45", createdAt: "" }],
      fetchedAt: "",
      running: true,
    });
    await render();
    // A row that was never started shows neither the report button nor the started section.
    await openRow(1);
    expect(document.querySelector(".wi-dstarted")).toBeNull();
    await act(async () => {
      [...document.querySelectorAll<HTMLButtonElement>(".wi-dmodal .ui-modal-foot button")][0].click();
    });

    await openRow(0);
    const started = document.querySelector(".wi-dstarted")!;
    expect(started.textContent).toContain("sk7f3q9");
    expect(started.textContent).toContain("feature/issue-45");
    const report = [...started.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      (b.textContent || "").includes(t("wi.report_title")),
    )!;
    await act(async () => report.click());
    // Never stack two: the detail modal closes and only the report one remains.
    expect(document.querySelector(".wi-dmodal")).toBeNull();
    expect(document.querySelector(".wi-rmodal")).not.toBeNull();
  });

  it("offers no report on a bitbucket item, since the control would always be refused once pressed", async () => {
    useReposStore.setState({ repos: [{ name: "web", path: "/home/dev/repos/web" }] });
    workItemList.mockResolvedValue({
      items: [item({ provider: "bitbucket", kind: "pr", key: "acme/web#45" })],
      queries: [query],
      sessions: [{ id: "l1", provider: "bitbucket", itemKey: "acme/web#45", sessionName: "sk7f3q9", repo: "web", branch: "", createdAt: "" }],
      fetchedAt: "",
      running: true,
    });
    await render();
    await openRow();
    expect(document.querySelector(".wi-dstarted")).not.toBeNull();
    expect(document.querySelector(".wi-dmodal")?.textContent).not.toContain(t("wi.report_title"));
  });

  // --- The saved query's source: three choices, so a segmented control instead of a select ---

  const openQueries = async () => {
    const gear = [...host.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.getAttribute("aria-label") === t("wi.queries"),
    )!;
    await act(async () => gear.click());
    return document.querySelector(".wi-qmodal")!;
  };

  it("renders the source as three buttons, not a select (nothing to collapse, and options were unreadable in the dark theme)", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    await render();
    const modal = await openQueries();

    const segs = [...modal.querySelectorAll<HTMLButtonElement>(".wi-qform .ui-seg .seg-btn")];
    expect(segs.map((b) => b.textContent)).toEqual(["GitHub", "Jira", "Bitbucket"]);
    expect(segs.map((b) => b.getAttribute("aria-pressed"))).toEqual(["true", "false", "false"]);
    // The only select left is the default working copy; the source select is gone.
    expect(modal.querySelectorAll(".wi-qform select").length).toBe(1);

    await act(async () => segs[1].click());
    expect(segs.map((b) => b.getAttribute("aria-pressed"))).toEqual(["false", "true", "false"]);
    // The field swaps to the pressed source's default query (JQL), so the select's side effect
    // still works.
    const expr = [...modal.querySelectorAll<HTMLInputElement>(".wi-qform input")].pop()!;
    expect(expr.value).toContain("currentUser()");
  });

  // --- §80.23: a Bitbucket query is assembled, never typed ---

  /** Select Bitbucket and advance until the connection's repository list has arrived. */
  const openBitbucket = async () => {
    await render();
    const modal = await openQueries();
    const provs = [...modal.querySelectorAll<HTMLButtonElement>(".wi-qform .ui-seg .seg-btn")];
    await act(async () => provs[2].click());
    await act(async () => {
      await Promise.resolve();
    });
    return modal;
  };

  it("assembles Bitbucket from the connection list, so nobody types af's invented format", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    bitbucketRepoList.mockResolvedValue({ repos: [{ full_name: "acme/web" }, { full_name: "acme/api" }] });
    const modal = await openBitbucket();

    // No free-text query field: the one text input is the label, and what to fetch is the three
    // checkboxes.
    expect(modal.querySelectorAll<HTMLInputElement>('.wi-qform input[type="text"], .wi-qform input:not([type])').length).toBe(1);
    expect(modal.querySelectorAll(".wi-qform .ui-seg").length).toBe(1); // the source only
    expect(modal.querySelectorAll('.wi-qchecks input[type="checkbox"]').length).toBe(3);

    // Choosing a target makes the string that will be saved visible as it is assembled.
    const target = modal.querySelectorAll<HTMLSelectElement>(".wi-qform select")[0];
    expect([...target.options].map((o) => o.value)).toEqual(["", "acme/api", "acme/web"]);
    await act(async () => selectInto(target, "acme/web"));
    expect(modal.querySelector(".wi-qfield code.wi-qquery")?.textContent).toBe('acme/web reviewers.uuid="@me"');

    // Adding saves that exact string; nobody typed the UUID or the leading target.
    await act(async () => [...modal.querySelectorAll<HTMLButtonElement>(".wi-qform button")].pop()!.click());
    expect(workItemQueryCreate).toHaveBeenCalledWith(
      expect.objectContaining({ provider: "bitbucket", query: 'acme/web reviewers.uuid="@me"' }),
    );
  });

  it("does not ask for a workspace-scoped target (my own PRs) when there is one candidate", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    bitbucketRepoList.mockResolvedValue({ repos: [{ full_name: "acme/web" }, { full_name: "acme/api" }] });
    const modal = await openBitbucket();

    const checks = [...modal.querySelectorAll<HTMLInputElement>('.wi-qchecks input[type="checkbox"]')];
    await act(async () => checks[0].click()); // clear waiting-on-my-review
    await act(async () => checks[2].click()); // select my own PRs
    // There is only one workspace, so it is settled without being chosen.
    const target = modal.querySelectorAll<HTMLSelectElement>(".wi-qform select")[0];
    expect([...target.options].map((o) => o.value)).toEqual(["", "acme"]);
    expect(target.value).toBe("acme");
    expect(modal.querySelector(".wi-qfield code.wi-qquery")?.textContent).toBe("acme");
  });

  it("accepts several intents at once (the three are not exclusive), adding two queries in one go", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    bitbucketRepoList.mockResolvedValue({ repos: [{ full_name: "acme/web" }] });
    const modal = await openBitbucket();

    const checks = [...modal.querySelectorAll<HTMLInputElement>('.wi-qchecks input[type="checkbox"]')];
    await act(async () => checks[2].click()); // waiting-on-my-review plus my own PRs
    // The target stays a repository, because a repository-scoped intent is still selected; af
    // folds it up to the workspace itself.
    expect([...modal.querySelectorAll(".wi-qfield code.wi-qquery")].map((c) => c.textContent)).toEqual([
      'acme/web reviewers.uuid="@me"',
      "acme",
    ]);
    // Adding several at once ignores the label, to avoid two rows with the same name.
    expect(modal.querySelector<HTMLInputElement>(".wi-qform input")?.disabled).toBe(true);

    await act(async () => [...modal.querySelectorAll<HTMLButtonElement>(".wi-qform button")].pop()!.click());
    expect(workItemQueryCreate).toHaveBeenCalledTimes(2);
    expect(workItemQueryCreate.mock.calls.map((c) => (c[0] as { query: string }).query)).toEqual([
      'acme/web reviewers.uuid="@me"',
      "acme",
    ]);
  });

  it("falls back to free text when the list cannot be fetched (stopped, not connected), never blocking in settings", async () => {
    workItemList.mockResolvedValue({ items: [item()], queries: [query], sessions: [], fetchedAt: "", running: true });
    bitbucketRepoList.mockResolvedValue({ error: { code: "workspace_stopped" } });
    const modal = await openBitbucket();

    expect(modal.querySelectorAll(".wi-qchecks").length).toBe(0); // no assembly UI
    const expr = [...modal.querySelectorAll<HTMLInputElement>(".wi-qform input")].pop()!;
    expect(expr.placeholder).toContain("workspace/repo");
    expect(modal.textContent).toContain(t("wi.bb_list_failed"));
  });
});
