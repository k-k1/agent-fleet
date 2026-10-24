// The session list store's "resume never dies" contract. Every route to resuming a stopped
// session depends on that row being in the list, so blanking the list on a transient fetch
// failure takes the resume button with it (it happened in the 502 window right after a
// container restart). That a failed resume POST is not swallowed is pinned here too.
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// The store imports the api client (which binds window.fetch and reads document.baseURI), so
// stub the globals before importing it (the same style as workspace.test.ts).
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("document", { baseURI: "http://localhost/", hidden: false });
const fetchMock = vi.fn<() => Promise<Response>>();
vi.stubGlobal("window", { fetch: fetchMock });
vi.stubGlobal("fetch", fetchMock);

const toastMock = vi.fn();
const dismissMock = vi.fn();
vi.mock("../../ui/toast.ts", () => ({
  toast: (...a: unknown[]) => toastMock(...a),
  dismissToast: (...a: unknown[]) => dismissMock(...a),
}));

let useSessionsStore: typeof import("./store.ts")["useSessionsStore"];
let tMaybe: typeof import("../../lib/i18n/index.ts")["tMaybe"];
beforeAll(async () => {
  ({ useSessionsStore } = await import("./store.ts"));
  ({ tMaybe } = await import("../../lib/i18n/index.ts"));
});

const jsonResponse = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });

const row = (name: string, alive: boolean) => ({ name, kind: "shell", alive, resumable: true });

describe("sessions store", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    toastMock.mockReset();
    useSessionsStore.setState({ sessions: [] });
  });

  it("keeps the last known list when a refresh fails", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", false)] }));
    await useSessionsStore.getState().refresh();
    expect(useSessionsStore.getState().sessions).toHaveLength(1);

    // The 502 window while the agent comes up. Blanking here removed the row, and
    // with it the only path back into a stopped session.
    fetchMock.mockRejectedValueOnce(new Error("502"));
    await useSessionsStore.getState().refresh();
    expect(useSessionsStore.getState().sessions.map((s) => s.name)).toEqual(["ssko6g5"]);
  });

  // The gap the case above did not cover: a REJECTED fetch is only half of "a refresh
  // failed". When the server answers, api() resolves an error body instead of throwing, so
  // the catch never runs and `d.sessions || []` published an empty list. Both shapes the CP
  // really produces are pinned here — the plain-text 502 it writes while the agent is
  // unreachable, and the JSON 500 it writes when its own store is unwell.
  // Each case seeds a DISTINCT name: applyList only publishes on a changed serialization and
  // that comparison is module state, so reusing one name would make the seeding refresh a
  // no-op and the case would fail before it reached what it is testing.
  it.each([
    ["plain-text 502 from the agent proxy", "sproxy52", () => new Response("workspace agent unreachable", { status: 502 })],
    [
      "JSON 500 from the control plane",
      "sinternal5",
      () =>
        new Response(JSON.stringify({ error: { code: "internal" } }), {
          status: 500,
          headers: { "Content-Type": "application/json" },
        }),
    ],
  ])("keeps the last known list on a %s", async (_label, name, res) => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ sessions: [row(name, false)] }));
    await useSessionsStore.getState().refresh();
    expect(useSessionsStore.getState().sessions).toHaveLength(1);

    fetchMock.mockResolvedValueOnce(res());
    await useSessionsStore.getState().refresh();
    expect(useSessionsStore.getState().sessions.map((s) => s.name)).toEqual([name]);
  });

  it("reports a failed resume instead of swallowing it", async () => {
    fetchMock
      .mockRejectedValueOnce(new Error("502")) // POST …/start
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", false)] })); // trailing refresh

    await expect(useSessionsStore.getState().start("ssko6g5")).resolves.toBe(false);
    expect(toastMock).toHaveBeenCalledTimes(1);
    expect(toastMock.mock.calls[0][1]).toMatchObject({ kind: "error" });
  });

  // api() resolves an HTTP error instead of throwing, so a refused start used to read as a
  // success: the pane attached to a session nobody started. codex answers 409 codex_releasing
  // while it still holds a just-stopped conversation, and that reason must reach the user.
  it("reports a refused resume with the server's reason", async () => {
    fetchMock
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: { code: "codex_releasing", message: "still held" } }), {
          status: 409,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", false)] }));

    await expect(useSessionsStore.getState().start("ssko6g5")).resolves.toBe(false);
    expect(toastMock).toHaveBeenCalledTimes(1);
    expect(toastMock.mock.calls[0][0]).toBe(tMaybe("err.codex_releasing"));
    expect(toastMock.mock.calls[0][1]).toMatchObject({ kind: "error" });
  });

  it("words a failed log restore from the catalogue and keeps the cause", async () => {
    fetchMock
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ error: { code: "transcript_restore_failed", message: "could not restore: disk full" } }),
          { status: 409, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", false)] }));

    await expect(useSessionsStore.getState().start("ssko6g5")).resolves.toBe(false);
    const shown = String(toastMock.mock.calls[0][0]);
    expect(shown).toContain(tMaybe("err.transcript_restore_failed") as string);
    expect(shown).toContain("disk full");
  });

  it("keeps the server's message for an uncatalogued refusal", async () => {
    fetchMock
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: { code: "start_failed", message: "tmux: no server" } }), {
          status: 500,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", false)] }));

    await expect(useSessionsStore.getState().start("ssko6g5")).resolves.toBe(false);
    expect(String(toastMock.mock.calls[0][0])).toContain("tmux: no server");
  });

  // claude deletes transcripts older than cleanupPeriodDays; a session whose log is gone must not
  // quietly start a blank conversation. The agent refuses with 409 transcript_lost, and the
  // toast's button is the explicit way to start fresh (?fresh=1).
  // The toast outlives a tenant switch and names the session by slug only, so it is bound to the
  // (user, tenant) it was raised for: a late 409 shows nothing in another workspace, and the
  // button of a toast left behind does nothing there. Back in the original one it works again.
  it("binds the lost-log toast to the tenant that raised it", async () => {
    const lost = () =>
      new Response(JSON.stringify({ error: { code: "transcript_lost", message: "x" } }), {
        status: 409,
        headers: { "Content-Type": "application/json" },
      });
    const { setTenant } = await import("../../core/api/client.ts");

    // A late answer: the request left for alpha, the user is in beta when it lands.
    setTenant("alpha");
    let release!: () => void;
    fetchMock.mockImplementationOnce(() => new Promise((r) => (release = () => r(lost()))));
    fetchMock.mockResolvedValue(jsonResponse({ sessions: [] }));
    const pending = useSessionsStore.getState().start("ssko6g5");
    setTenant("beta");
    release();
    await pending;
    expect(toastMock).not.toHaveBeenCalled();

    // A toast raised in alpha, its button pressed in beta: no request at all.
    setTenant("alpha");
    fetchMock.mockReset();
    fetchMock.mockResolvedValueOnce(lost()).mockResolvedValue(jsonResponse({ sessions: [] }));
    await useSessionsStore.getState().start("ssko6g5");
    const body = toastMock.mock.calls[0][0] as { props: { children: { props: { onClick: () => void } }[] } };
    setTenant("beta");
    const before = fetchMock.mock.calls.length;
    body.props.children[1].props.onClick();
    await Promise.resolve();
    expect(fetchMock.mock.calls.length).toBe(before);

    // Back in alpha the same button works.
    setTenant("alpha");
    body.props.children[1].props.onClick();
    await vi.waitFor(() => expect(String((fetchMock.mock.calls[before] as unknown[])[0])).toContain("/start?fresh=1"));
    setTenant("");
  });

  it("offers a fresh start when the conversation log was deleted", async () => {
    fetchMock
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: { code: "transcript_lost", message: "log deleted" } }), {
          status: 409,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", false)] }));

    await expect(useSessionsStore.getState().start("ssko6g5")).resolves.toBe(false);
    expect(toastMock).toHaveBeenCalledTimes(1);
    expect(toastMock.mock.calls[0][1]).toMatchObject({ kind: "error", duration: 0 });

    fetchMock
      .mockResolvedValueOnce(jsonResponse({ ok: true }))
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", true)] }));
    const toastBody = toastMock.mock.calls[0][0] as { props: { children: { props: { onClick: () => void } }[] } };
    toastBody.props.children[1].props.onClick();
    await vi.waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(4));
    expect(String((fetchMock.mock.calls[2] as unknown[])[0])).toContain("/start?fresh=1");
  });

  // The accepted POST is authoritative (docs/log/85): the row has to say "stopping after
  // this turn" straight away, or the only feedback for an arm set from the ⋯ menu is a toast
  // that disappears, and the arm the session set on the user's word stays invisible until the
  // next poll lands.
  it("shows a stop-after-turn arm before the next list poll", () => {
    useSessionsStore.setState({ sessions: [{ ...row("ssko6g5", true), kind: "shell" as const }] });
    useSessionsStore.getState().setStopAfterTurn("ssko6g5", "2026-09-07T10:00:00Z");
    expect(useSessionsStore.getState().sessions[0].stopAfterTurnAt).toBe("2026-09-07T10:00:00Z");

    useSessionsStore.getState().setStopAfterTurn("ssko6g5", "");
    expect(useSessionsStore.getState().sessions[0].stopAfterTurnAt).toBe("");
  });

  it("reports a successful resume", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ ok: true })) // POST …/start
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", true)] }));

    await expect(useSessionsStore.getState().start("ssko6g5")).resolves.toBe(true);
    expect(toastMock).not.toHaveBeenCalled();
  });
});
