// When a session leaves the list, its tabs go with it (features/sessions/paneReconcile.ts).
//
// The two failure directions are asymmetric: not closing leaves a dead tab that reads "No
// session" forever, which is the annoyance this fixes; closing too eagerly takes away a tab
// the user is working in — for a session that still exists — and that is the one this file
// pins down, because every case that could cause it (an empty list, a list older than the
// session, a tenant switch) looks exactly like a session that was archived.
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { Layout } from "../../layout/types.ts";
import type { Session } from "../../types/session.ts";

// As in files/sessionRefresh.test.ts: the stores drag in the api client (localStorage,
// document.baseURI, fetch), so the globals are stubbed before importing.
const values = new Map<string, string>();
const storage = {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
};
vi.stubGlobal("localStorage", storage);
vi.stubGlobal("sessionStorage", storage);
vi.stubGlobal("document", { baseURI: "http://localhost/", hidden: false });
vi.stubGlobal("window", {
  fetch: vi.fn(),
  setTimeout: (...a: Parameters<typeof setTimeout>) => setTimeout(...a),
  clearTimeout: (...a: Parameters<typeof clearTimeout>) => clearTimeout(...a),
  addEventListener: () => {},
  removeEventListener: () => {},
});

let useLayoutStore: typeof import("../../layout/store.ts")["useLayoutStore"];
let useSessionsStore: typeof import("./store.ts")["useSessionsStore"];
let useTenantStore: typeof import("../../core/store/tenant.ts")["useTenantStore"];
let registerToastSink: typeof import("../../ui/toast.ts")["registerToastSink"];
let wireSessionPaneReconcile: typeof import("./paneReconcile.ts")["wireSessionPaneReconcile"];

beforeAll(async () => {
  ({ useLayoutStore } = await import("../../layout/store.ts"));
  ({ useSessionsStore } = await import("./store.ts"));
  ({ useTenantStore } = await import("../../core/store/tenant.ts"));
  ({ registerToastSink } = await import("../../ui/toast.ts"));
  ({ wireSessionPaneReconcile } = await import("./paneReconcile.ts"));
});

const s = (name: string, over: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  alive: true,
  label: name + "-title",
  ...over,
});

/** One cell holding a terminal tab per session name, plus a session-less file tab that no
 *  reconciliation may ever touch. */
const layoutWith = (...sessions: string[]): Layout => ({
  version: 3,
  mode: "tabs",
  cols: [
    {
      id: "c0",
      rowRatio: 0.5,
      cells: [
        {
          id: "g0",
          selectedViewId: "v0",
          views: [
            ...sessions.map((name, i) => ({
              id: "v" + i,
              session: name,
              content: { kind: "terminal" as const, chat: false },
              wrap: null,
            })),
            {
              id: "vf",
              session: null,
              content: { kind: "file" as const, filePath: "README.md" },
              wrap: null,
            },
          ],
        },
      ],
    },
  ],
  colRatios: [1],
  activeCellId: "g0",
});

const openSessions = (): (string | null)[] =>
  useLayoutStore
    .getState()
    .layout.cols[0].cells[0].views.filter((v) => v.content.kind === "terminal")
    .map((v) => v.session);

const tabIds = (): string[] =>
  useLayoutStore.getState().layout.cols[0].cells[0].views.map((v) => v.id);

const publish = (list: Session[]) => useSessionsStore.setState({ sessions: list });

describe("wireSessionPaneReconcile", () => {
  let un: (() => void) | null = null;
  let toasts: string[] = [];

  beforeEach(() => {
    toasts = [];
    registerToastSink((m) => toasts.push(String(m)));
    useSessionsStore.setState({ sessions: [] });
    useLayoutStore.setState({ layout: layoutWith("alpha", "beta"), hydrated: true });
    useTenantStore.setState({ tenant: "acme", identityRev: 0 });
  });
  afterEach(() => {
    un?.();
    un = null;
    registerToastSink(null);
  });

  it("closes the tab of a session that left the list, and names it", () => {
    un = wireSessionPaneReconcile();
    publish([s("alpha"), s("beta")]);
    expect(openSessions()).toEqual(["alpha", "beta"]);

    publish([s("beta")]); // alpha archived from another tab / device / the cleanup run
    expect(openSessions()).toEqual(["beta"]);
    expect(toasts).toHaveLength(1);
    expect(toasts[0]).toContain("alpha-title"); // its title, never the runtime slug
  });

  it("leaves tabs that carry no session (a file, a document) alone", () => {
    un = wireSessionPaneReconcile();
    publish([s("alpha"), s("beta")]);
    publish([]);
    publish([s("beta")]);
    expect(tabIds()).toContain("vf");
  });

  it("counts the batch in one toast when a tidy-up takes several at once", () => {
    un = wireSessionPaneReconcile();
    publish([s("alpha"), s("beta"), s("gamma")]);
    publish([s("gamma")]);
    expect(openSessions()).toEqual([]);
    expect(toasts).toHaveLength(1);
    expect(toasts[0]).toContain("2");
  });

  it("says nothing when the vanished session had no tab open", () => {
    un = wireSessionPaneReconcile();
    publish([s("alpha"), s("beta"), s("gamma")]);
    publish([s("alpha"), s("beta")]);
    expect(openSessions()).toEqual(["alpha", "beta"]);
    expect(toasts).toEqual([]);
  });

  // An empty list is what the CP answers when its DB mirror cannot be read, and what the
  // push path applies for a malformed frame. Closing every tab on one would be the worst
  // possible reading of a transient failure.
  it("never closes on an empty list, and does not let it erase what it knows", () => {
    un = wireSessionPaneReconcile();
    publish([s("alpha"), s("beta")]);
    publish([]);
    expect(openSessions()).toEqual(["alpha", "beta"]);
    // The empty list was not recorded either: beta leaving the NEXT list is still an edge.
    publish([s("alpha")]);
    expect(openSessions()).toEqual(["alpha"]);
  });

  // The dangerous case: a session created here, whose pane exists before any list carries
  // it (the create response beat the poll that was already in flight).
  it("keeps a tab whose session no list has ever carried", () => {
    un = wireSessionPaneReconcile();
    publish([s("alpha"), s("beta")]);
    useLayoutStore.setState({ layout: layoutWith("alpha", "beta", "fresh") });
    publish([s("alpha"), s("beta")]); // the older list, still without the new session
    expect(openSessions()).toEqual(["alpha", "beta", "fresh"]);
    expect(toasts).toEqual([]);
  });

  // The boot pass: a tab restored from the saved layout is evidence the session existed
  // when the layout was written, so the first trusted list may judge it.
  it("closes a restored tab whose session was archived while the Console was closed", () => {
    un = wireSessionPaneReconcile();
    publish([s("beta")]);
    expect(openSessions()).toEqual(["beta"]);
    expect(toasts).toHaveLength(1);
  });

  it("waits for the restored layout when a list arrives first", () => {
    useLayoutStore.setState({ layout: layoutWith(), hydrated: false });
    un = wireSessionPaneReconcile();
    publish([s("beta")]);
    // The saved layout lands afterwards, still holding the archived session's tab.
    useLayoutStore.setState({ layout: layoutWith("alpha", "beta"), hydrated: true });
    expect(openSessions()).toEqual(["beta"]);
  });

  // A tenant switch replaces both the list and the layout, so the previous tenant's names
  // say nothing about this one — and a name that appears in both must not be read as the
  // sessions of tenant A having been archived.
  it("does not read another tenant's list as this tenant's sessions being gone", () => {
    un = wireSessionPaneReconcile();
    publish([s("alpha"), s("beta")]);
    useTenantStore.setState({ tenant: "other" });
    useLayoutStore.setState({ layout: layoutWith("gamma") });
    publish([s("gamma"), s("delta")]);
    expect(openSessions()).toEqual(["gamma"]);
    expect(toasts).toEqual([]);
  });

  it("stops closing once unsubscribed", () => {
    un = wireSessionPaneReconcile();
    publish([s("alpha"), s("beta")]);
    un();
    un = null;
    publish([s("beta")]);
    expect(openSessions()).toEqual(["alpha", "beta"]);
  });
});
