// The move dialog (ADR 0115, P2a). What is pinned: the picker offers only what the Agent's
// preview calls eligible and says why the rest are missing (a same-kind source is pointed at fork
// where its kind can fork); the facts decisions 12 and 13 require are shown before the confirm
// (dirty working copy, running children, queued input, peer messages, file size, the managed
// cursor warning); confirm posts the chosen kind; a create that may have started a target turns
// into retry / cancel; and a moved source offers its target, the first prompt's resend and
// "resume here anyway".
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Call = { path: string; method: string; body?: unknown };
const calls: Call[] = [];
// Answers by "METHOD path"; a function answers per call. Anything unrouted is an error body,
// which every reader in the dialog treats as "no data".
let routes: Record<string, unknown> = {};
const answer = (method: string, path: string, body?: unknown) => {
  calls.push({ path, method, body });
  const r = routes[`${method} ${path}`];
  if (typeof r === "function") return (r as () => unknown)();
  return r ?? { error: { code: "unrouted" } };
};
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  api: async (path: string, opts?: RequestInit) => answer((opts?.method ?? "GET").toUpperCase(), path),
  apiJSON: async (path: string, method: string, body?: unknown) => answer(method, path, body),
}));
vi.mock("../../ui/ModelPicker.tsx", () => ({ ModelPicker: () => <span className="model-picker-stub" /> }));
const opened: string[] = [];
vi.mock("./open.ts", () => ({ openSessionByName: (n: string) => opened.push(n) }));

const { MoveSessionModal } = await import("./MoveSessionModal.tsx");
const { parseKindCatalog, setKindCatalog } = await import("../../agents/kindCatalog.ts");
const { useSessionsStore } = await import("./store.ts");
const { setCachedConns } = await import("../repos/connsCache.ts");
const { t } = await import("../../lib/i18n/index.ts");
type Session = import("../../types/session.ts").Session;
type MovePreview = import("./move.ts").MovePreview;

let root: Root | null = null;
let host: HTMLDivElement;
const src: Session = { name: "s1", kind: "claude", alive: false, state: "", title: "作業", repo: "app" };

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => await Promise.resolve());
};
const render = async (s: Session = src, fromLimit = false) => {
  await act(async () => root!.render(<MoveSessionModal session={s} fromLimit={fromLimit} onClose={() => {}} />));
  await flush();
};
const click = async (el: Element | null) => {
  expect(el, "element to click").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};
const text = () => document.body.textContent ?? "";
const kindButtons = () => [...document.querySelectorAll<HTMLElement>(".ui-seg.big .seg-btn")].map((b) => b.dataset.kind);
const confirmBtn = () => document.querySelector<HTMLButtonElement>(".ui-modal-foot button[type=submit]");

const preview = (over: Partial<MovePreview> = {}): MovePreview => ({
  source: "s1",
  kind: "claude",
  state: "idle",
  canFork: true,
  targets: [
    { kind: "claude", eligible: false, reason: "same_kind" },
    { kind: "codex", eligible: true },
    { kind: "opencode", eligible: true },
    { kind: "cursor", eligible: false, reason: "not_measured" },
    { kind: "lcpp", eligible: false, reason: "cannot_read_file" },
  ],
  git: { available: true, dirty: false },
  sizeBytes: 2048,
  ...over,
});
const PREVIEW = "GET api/sessions/s1/move/preview";

beforeEach(() => {
  calls.length = 0;
  opened.length = 0;
  routes = { [PREVIEW]: preview() };
  // Null = the connection check has not answered: the picker then takes the Agent's word alone.
  setCachedConns(null);
  useSessionsStore.setState({ sessions: [], refresh: async () => {} } as never);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("MoveSessionModal: before the move", () => {
  it("says it is reading while the preview is on its way", async () => {
    routes[PREVIEW] = () => new Promise(() => {});
    await render();
    expect(text()).toContain(t("move.loading"));
    expect(confirmBtn()).toBeNull();
  });

  it("offers the eligible kinds and lists the others with their reason", async () => {
    await render();
    expect(kindButtons()).toEqual(["codex", "opencode"]);
    const missing = [...document.querySelectorAll<HTMLElement>(".move-not-offered li")];
    expect(missing.map((li) => li.dataset.kind)).toEqual(["claude", "cursor", "lcpp"]);
    expect(missing[1].textContent).toContain(t("move.reason.not_measured"));
    expect(missing[2].textContent).toContain(t("move.reason.cannot_read_file"));
  });

  it("keeps the Agent's order, and marks nothing, when not opened from a limit banner", async () => {
    routes["GET api/codex/usage"] = { ok: true, fiveHour: { pct: 10 } };
    await render();
    expect(kindButtons()).toEqual(["codex", "opencode"]);
    expect(document.querySelector(".move-quota-left")).toBeNull();
  });

  it("from a limit banner puts the kinds with quota left first, marked, and defaults to the first", async () => {
    routes[PREVIEW] = preview({
      targets: [
        { kind: "claude", eligible: false, reason: "same_kind" },
        { kind: "codex", eligible: true },
        { kind: "opencode", eligible: true },
        { kind: "muse", eligible: true },
      ],
    });
    routes["GET api/codex/usage"] = { ok: true, fiveHour: { pct: 100 } };
    routes["GET api/muse/usage"] = { ok: true, fiveHour: { pct: 30 } };
    await render(src, true);
    // muse has quota; opencode has no usage source (selectable, unmarked); codex is at its cap.
    expect(kindButtons()).toEqual(["muse", "opencode", "codex"]);
    const marked = [...document.querySelectorAll<HTMLElement>(".seg-btn.quota-left")].map((b) => b.dataset.kind);
    expect(marked).toEqual(["muse"]);
    expect(document.querySelector(".seg-btn.active")?.getAttribute("data-kind")).toBe("muse");
  });

  it("does not move the user's choice when usage reorders the picks", async () => {
    routes["GET api/codex/usage"] = { ok: true, fiveHour: { pct: 100 } };
    routes["GET api/muse/usage"] = { ok: true, fiveHour: { pct: 30 } };
    routes[PREVIEW] = preview({
      targets: [
        { kind: "codex", eligible: true },
        { kind: "muse", eligible: true },
      ],
    });
    await render(src, true);
    await click(document.querySelector('.seg-btn[data-kind="codex"]'));
    expect(document.querySelector(".seg-btn.active")?.getAttribute("data-kind")).toBe("codex");
  });

  it("points a same-kind source at fork when its kind can fork, and says another kind is needed when not", async () => {
    await render();
    expect(document.querySelector('.move-not-offered li[data-kind="claude"]')?.textContent).toContain(t("move.reason.same_kind_fork"));
    act(() => root!.unmount());
    root = createRoot(host);
    routes[PREVIEW] = preview({
      kind: "cursor",
      canFork: false,
      targets: [
        { kind: "claude", eligible: true },
        { kind: "cursor", eligible: false, reason: "same_kind" },
      ],
    });
    await render({ ...src, kind: "cursor" });
    const li = document.querySelector('.move-not-offered li[data-kind="cursor"]')?.textContent ?? "";
    expect(li).toContain(t("move.reason.same_kind"));
    expect(li).not.toContain(t("move.reason.same_kind_fork"));
  });

  it("drops an eligible kind that is not connected and says so", async () => {
    setCachedConns({ codex: { connected: false }, opencode: { connected: true } } as never);
    await render();
    expect(kindButtons()).toEqual(["opencode"]);
    expect(document.querySelector('.move-not-offered li[data-kind="codex"]')?.textContent).toContain(t("move.reason.not_connected"));
  });

  it("shows a dirty working copy as it is, and says the move will not tidy it", async () => {
    routes[PREVIEW] = preview({ git: { available: true, dirty: true, status: " M main.go\n?? notes.txt", diffStat: "1 file changed" } });
    await render();
    expect(document.querySelector(".move-git-dirty")?.textContent).toContain(t("move.git_dirty"));
    expect(document.querySelector(".move-pre")?.textContent).toContain(" M main.go");
    expect(document.querySelector(".move-git-clean")).toBeNull();
  });

  it("warns that running children keep their owner, and lists queued input, peer messages and the size", async () => {
    routes[PREVIEW] = preview({
      children: [{ name: "kid1", state: "working", running: true }],
      proposals: ["hp-1"],
      queued: [{ text: "次はテストを直して" }],
      peers: [{ from: "peerA", intent: "question", message: "進み具合は？" }],
      warnings: ["children_running"],
      sizeBytes: 300 * 1024,
      overSizeTarget: true,
    });
    await render();
    expect(document.querySelector(".move-warn-children")?.textContent).toContain(t("move.warn.children_running"));
    expect(document.querySelector(".move-children")?.textContent).toContain("kid1");
    expect(document.querySelector(".move-children")?.textContent).toContain(t("move.proposal_item", { name: "hp-1" }));
    expect(document.querySelector(".move-queued")?.textContent).toContain("次はテストを直して");
    expect(document.querySelector(".move-peers")?.textContent).toContain("進み具合は？");
    expect(document.querySelector(".move-size")?.textContent).toContain("300.0 KiB");
    expect(document.querySelector(".move-size")?.textContent).toContain(t("move.size_over"));
  });

  it("re-reads the driver facts when the kind catalog lands while the dialog is open", async () => {
    routes[PREVIEW] = preview();
    await render();
    const toggle = () => [...document.querySelectorAll(".seg-btn")].find((b) => b.textContent?.includes(t("launch.driver_managed")));
    expect(toggle()?.classList.contains("active")).toBe(true); // the first offered target defaults to managed
    await act(async () => setKindCatalog(parseKindCatalog({ kinds: ["codex", "opencode"].map((kind) => ({ kind, managedOnly: false, managedDefault: false, mcpClient: "yes" })) })));
    expect(toggle()).toBeUndefined(); // the catalog says the offered kinds have no managed default
    await act(async () => setKindCatalog(null));
  });

  it("warns before moving a Managed cursor source that its mirror empties", async () => {
    routes[PREVIEW] = preview({ kind: "cursor", warnings: ["managed_cursor_mirror_empty"] });
    await render({ ...src, kind: "cursor" });
    expect(document.querySelector(".move-warn-cursor")?.textContent).toContain(t("move.warn.managed_cursor_mirror_empty"));
  });

  it("disables the confirm while the Agent refuses the source, and says why", async () => {
    routes[PREVIEW] = preview({ refusal: { code: "move_source_state", message: "working", state: "working" } });
    await render();
    expect(confirmBtn()?.disabled).toBe(true);
    expect(document.querySelector(".move-refusal")?.textContent).toContain(t("err.move_source_state"));
  });
});

describe("MoveSessionModal: the move", () => {
  it("posts the chosen kind and shows the target it created", async () => {
    routes["POST api/sessions/s1/move"] = {
      source: "s1",
      target: { name: "t9", kind: "opencode", title: "作業", alive: true },
      file: "/home/dev/.af-work/t9/handover-s1.md",
      sizeBytes: 2048,
      sourceStopped: true,
    };
    await render();
    await click(document.querySelector('.seg-btn[data-kind="opencode"]'));
    await click(confirmBtn());
    const post = calls.find((c) => c.method === "POST");
    expect(post?.path).toBe("api/sessions/s1/move");
    expect((post?.body as { kind: string }).kind).toBe("opencode");
    expect(document.querySelector(".move-done")?.textContent).toContain("/home/dev/.af-work/t9/handover-s1.md");
    await click(document.querySelector(".move-open-target"));
    expect(opened).toEqual(["t9"]);
  });

  it("shows the move in flight where it stays visible, and locks the dialog meanwhile", async () => {
    routes["POST api/sessions/s1/move"] = () => new Promise(() => {});
    await render();
    await click(confirmBtn());
    expect(document.querySelector(".ui-modal-foot .move-progress")?.textContent).toContain(t("move.busy_move"));
    expect(confirmBtn()?.disabled).toBe(true);
    expect(document.querySelector<HTMLButtonElement>(".ui-modal-foot .ui-btn-ghost")?.disabled).toBe(true);
  });

  it("turns a create that may have started a target into retry and cancel", async () => {
    let moved = false;
    routes["POST api/sessions/s1/move"] = () => {
      moved = true;
      return { error: { code: "move_create_uncertain", message: "timeout", target: "t9", file: "/f.md" } };
    };
    routes[PREVIEW] = () =>
      moved ? preview({ moving: "t9", pending: { target: "t9", file: "/f.md", state: "uncertain", error: "timeout", targetExists: false } }) : preview();
    routes["POST api/sessions/s1/move/retry"] = { source: "s1", target: { name: "t9", kind: "codex" }, file: "/f.md", sourceStopped: true };
    await render();
    await click(confirmBtn());
    expect(document.querySelector(".move-failure")?.textContent).toContain(t("err.move_create_uncertain"));
    expect(document.querySelector(".move-pending")?.textContent).toContain(t("move.pending_intro", { target: "t9" }));
    const cancel = document.querySelector<HTMLButtonElement>(".move-cancel");
    expect(cancel?.disabled).toBe(false);
    await click(document.querySelector(".move-retry"));
    expect(calls.some((c) => c.method === "POST" && c.path === "api/sessions/s1/move/retry")).toBe(true);
    expect(document.querySelector(".move-done")).toBeTruthy();
  });

  it("does not offer cancel once the target exists, nor retry for an orphan", async () => {
    routes[PREVIEW] = preview({ moving: "t9", pending: { target: "t9", targetExists: true } });
    await render();
    expect(document.querySelector<HTMLButtonElement>(".move-cancel")?.disabled).toBe(true);
    expect(document.querySelector<HTMLButtonElement>(".move-retry")?.disabled).toBe(false);
    act(() => root!.unmount());
    root = createRoot(host);
    routes[PREVIEW] = preview({ moving: "t9", pending: { target: "t9", targetExists: false, orphan: true } });
    routes["POST api/sessions/s1/move/cancel"] = { cancelled: "s1", target: "t9" };
    await render();
    expect(document.querySelector<HTMLButtonElement>(".move-retry")?.disabled).toBe(true);
    await click(document.querySelector(".move-cancel"));
    expect(calls.some((c) => c.method === "POST" && c.path === "api/sessions/s1/move/cancel")).toBe(true);
  });
});

describe("MoveSessionModal: a moved source", () => {
  it("names the target, resends its first prompt on the user's word, and reopens the source", async () => {
    routes[PREVIEW] = preview({
      movedTo: "t9",
      delivery: { target: "t9", file: "/f.md", initialPromptState: "unknown", deliverable: true },
    });
    routes["POST api/sessions/t9/move/deliver"] = { target: "t9", initialPromptState: "delivered", mayDuplicate: true };
    routes["POST api/sessions/s1/move/reopen"] = { reopened: "s1" };
    await render();
    expect(document.querySelector(".move-moved-to")?.textContent).toContain(t("move.moved_to", { target: "t9" }));
    expect(document.querySelector(".move-delivery")?.textContent).toContain(t("move.delivery_unknown", { target: "t9" }));
    expect(confirmBtn()).toBeNull();
    await click(document.querySelector(".move-deliver"));
    // The resend goes to the TARGET's route, never the source's.
    expect(calls.some((c) => c.method === "POST" && c.path === "api/sessions/t9/move/deliver")).toBe(true);
    expect(text()).toContain(t("move.deliver_sent_maybe_twice"));
    await click(document.querySelector(".move-reopen-btn"));
    expect(calls.some((c) => c.method === "POST" && c.path === "api/sessions/s1/move/reopen")).toBe(true);
  });
});

// States that end without the user: the dialog has to read the preview again, or it shows a
// state that is long gone with its buttons disabled.
describe("MoveSessionModal: states that move on by themselves", () => {
  afterEach(() => vi.useRealTimers());
  const tick = async () => {
    await act(async () => await vi.advanceTimersByTimeAsync(3000));
    await flush();
  };

  it("enables retry and cancel once a move running elsewhere has stopped", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let reads = 0;
    routes[PREVIEW] = () =>
      ++reads === 1
        ? preview({ moving: "t9", pending: { target: "t9", targetExists: false, busy: true } })
        : preview({ moving: "t9", pending: { target: "t9", targetExists: false, state: "uncertain" } });
    await render();
    expect(document.querySelector<HTMLButtonElement>(".move-retry")?.disabled).toBe(true);
    expect(document.querySelector<HTMLButtonElement>(".move-cancel")?.disabled).toBe(true);
    await tick();
    expect(reads).toBe(2);
    expect(document.querySelector<HTMLButtonElement>(".move-retry")?.disabled).toBe(false);
    expect(document.querySelector<HTMLButtonElement>(".move-cancel")?.disabled).toBe(false);
    // Settled: no more reads.
    await tick();
    expect(reads).toBe(2);
  });

  it("follows a Terminal resend that answered pending and then failed in the background", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const done = { source: "s1", target: { name: "t9", kind: "codex" }, file: "/f.md", sourceStopped: true,
      delivery: { target: "t9", file: "/f.md", initialPromptState: "failed", deliverable: true } };
    routes["POST api/sessions/s1/move"] = done;
    let stage = "before";
    routes[PREVIEW] = () =>
      stage === "before"
        ? preview()
        : preview({ movedTo: "t9", delivery: { target: "t9", file: "/f.md", initialPromptState: stage, deliverable: stage !== "pending" } });
    routes["POST api/sessions/t9/move/deliver"] = () => {
      stage = "pending";
      return { target: "t9", initialPromptState: "pending" };
    };
    await render();
    await click(confirmBtn());
    await click(document.querySelector(".move-done .move-deliver"));
    // A 202 says only that typing started: not "resent", and the row stays.
    expect(text()).toContain(t("move.deliver_started"));
    expect(text()).not.toContain(t("move.deliver_sent"));
    expect(document.querySelector(".move-done .move-delivery")?.textContent).toContain(t("move.delivery_pending", { target: "t9" }));
    expect(document.querySelector(".move-done .move-deliver")).toBeNull();
    stage = "failed";
    await tick();
    expect(document.querySelector(".move-done .move-delivery")?.textContent).toContain(t("move.delivery_failed", { target: "t9" }));
    expect(document.querySelector(".move-done .move-deliver")).toBeTruthy();
  });

  it("keeps the view and the re-reads through one failed read", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let reads = 0;
    routes[PREVIEW] = () => {
      reads++;
      if (reads === 1) return preview({ moving: "t9", pending: { target: "t9", targetExists: false, busy: true } });
      if (reads === 2) return { error: { code: "http_502", message: "workspace agent unreachable", status: 502 } };
      return preview({ moving: "t9", pending: { target: "t9", targetExists: false, state: "uncertain" } });
    };
    await render();
    await tick();
    // The blip is said, the pending view stays, and the next read still comes.
    expect(document.querySelector(".move-stale")).toBeTruthy();
    expect(document.querySelector(".move-pending")).toBeTruthy();
    await tick();
    expect(reads).toBe(3);
    expect(document.querySelector(".move-stale")).toBeNull();
    expect(document.querySelector<HTMLButtonElement>(".move-retry")?.disabled).toBe(false);
  });

  it("keeps following a pending delivery through one failed read", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let reads = 0;
    const moved = (st: string) =>
      preview({ movedTo: "t9", delivery: { target: "t9", file: "/f.md", initialPromptState: st, deliverable: st !== "pending" } });
    routes[PREVIEW] = () => {
      reads++;
      if (reads === 1) return moved("pending");
      if (reads === 2) throw new Error("network");
      return moved("failed");
    };
    await render();
    expect(document.querySelector(".move-deliver")).toBeNull();
    await tick();
    expect(document.querySelector(".move-moved")).toBeTruthy();
    await tick();
    expect(document.querySelector(".move-delivery")?.textContent).toContain(t("move.delivery_failed", { target: "t9" }));
    expect(document.querySelector(".move-deliver")).toBeTruthy();
  });

  it("keeps reading after a failed read that followed the action changing the phase", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let stage = "fresh";
    let afterMove = 0;
    routes["POST api/sessions/s1/move"] = () => {
      stage = "moved";
      return { error: { code: "move_create_uncertain", message: "timeout", target: "t9", file: "/f.md" } };
    };
    routes[PREVIEW] = () => {
      if (stage === "fresh") return preview();
      // The read right after the move fails once; the next one shows the pending move.
      if (++afterMove === 1) return { error: { code: "http_502", message: "unreachable", status: 502 } };
      return preview({ moving: "t9", pending: { target: "t9", targetExists: false, state: "uncertain" } });
    };
    await render();
    await click(confirmBtn());
    // Still the pre-move form, marked stale: the kept preview is from before the move.
    expect(document.querySelector(".move-stale")).toBeTruthy();
    expect(document.querySelector(".move-pending")).toBeNull();
    await tick();
    expect(afterMove).toBe(2);
    expect(document.querySelector(".move-stale")).toBeNull();
    expect(document.querySelector(".move-pending")).toBeTruthy();
    expect(document.querySelector<HTMLButtonElement>(".move-retry")?.disabled).toBe(false);
  });
});

