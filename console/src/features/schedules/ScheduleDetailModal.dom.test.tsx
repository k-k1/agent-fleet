// The schedule editor's AGENT PICKER, which had no test at all while it was a hand-kept array
// of six inside the component — and that is how it lost agy, a kind whose scheduled runs ship
// and which could therefore not be chosen in the Console at all (ADR 0095 P2-13's closing note,
// fixed in P2-14).
//
// A unit test over `scheduledKinds` alone would not have caught it either: the list can be right
// while the component ignores it. So this renders the real modal and reads the real <select>.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("./api.ts", () => ({ scheduleUpdate: vi.fn(), scheduleApprove: vi.fn() }));

// The live kind catalogue (GET /agents/kinds): every kind allowed unless a test narrows it.
const ALL = ["claude", "codex", "opencode", "copilot", "cursor", "kiro", "agy", "muse", "lcpp", "shell", "ssm"];
let liveKinds: Record<string, unknown>[] = ALL.map((kind) => ({ kind }));
let ssmProfiles: unknown[] = [];
vi.mock("../../core/api/client.ts", () => ({
  api: async (url: string) => {
    if (url.includes("agents/kinds")) return { kinds: liveKinds, access: { policyConfirmed: true } };
    if (url.includes("ssm/profiles")) return ssmProfiles;
    return {};
  },
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { ScheduleDetailModal } = await import("./ScheduleDetailModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { scheduleUpdate, scheduleApprove } = await import("./api.ts");
const { t } = await import("../../lib/i18n/index.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { getSettings, setSettings } = await import("../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const base = { id: "sch_1", spec_kind: "cron", spec: "0 9 * * *", prompt: "hi", enabled: true };

const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};

const onCloseSpy = vi.fn();
const onStaleSpy = vi.fn();

async function render(agentKind?: string, extra: Record<string, unknown> = {}): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ScheduleDetailModal
          s={{ ...base, agent_kind: agentKind, ...extra } as never}
          onClose={onCloseSpy}
          onStale={onStaleSpy}
          onSaved={() => {}}
        />
      </ToastProvider>,
    );
  });
  await flush();
}

// The modal renders into a portal, so the options are read off the document rather than off the
// host element the root was created on.
const agentOptions = (): string[] =>
  Array.from(document.querySelectorAll("#sched-agent option")).map((o) => o.textContent || "");

beforeEach(() => {
  liveKinds = ALL.map((kind) => ({ kind }));
  ssmProfiles = [];
  useWorkspaceStore.setState({ state: "running" } as never);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("ScheduleDetailModal agent picker", () => {
  it("offers agy, the kind the hand-kept list had lost", async () => {
    await render("claude");
    expect(agentOptions()).toContain("agy");
  });

  it("offers the kinds whose scheduled runs are verified, and no terminal kinds", async () => {
    await render("claude");
    const opts = agentOptions();
    for (const k of ["claude", "codex", "opencode", "copilot", "cursor", "kiro", "agy", "muse"]) {
      expect(opts).toContain(k);
    }
    // shell / ssm run what you send verbatim and have no conversation to schedule into; lcpp is
    // an agent kind whose scheduled runs nobody has watched work end to end, which is why the
    // guide's capability table leaves that row blank.
    for (const k of ["shell", "ssm", "lcpp"]) {
      expect(opts).not.toContain(k);
    }
  });

  it("keeps an existing schedule's own kind selectable even when the picker would not offer it", async () => {
    // The rule that stops an edit of one field from silently rewriting another: a schedule
    // already pointed at lcpp must not be re-pointed at claude just because the member opened
    // the modal to change its label.
    await render("lcpp");
    const opts = agentOptions();
    expect(opts[0]).toBe("lcpp");
    expect(opts.filter((k) => k === "lcpp")).toHaveLength(1);
    expect((document.querySelector("#sched-agent") as HTMLSelectElement).value).toBe("lcpp");
  });
});

// #1560: the delivery targets and the silent sentinel. Only what changed is patched, and a report
// with no target cannot be saved.
describe("ScheduleDetailModal delivery", () => {
  const update = scheduleUpdate as unknown as ReturnType<typeof vi.fn>;

  async function renderWith(extra: Record<string, unknown>): Promise<void> {
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ScheduleDetailModal s={{ ...base, ...extra } as never} onClose={() => {}} onSaved={() => {}} />
        </ToastProvider>,
      );
    });
    await flush();
  }
  const targetBox = (label: string): HTMLInputElement => {
    const row = Array.from(document.querySelectorAll(".sched-target")).find((l) => l.textContent === label);
    return row!.querySelector("input") as HTMLInputElement;
  };
  const saveButton = (): HTMLButtonElement =>
    Array.from(document.querySelectorAll("button[type=submit]"))[0] as HTMLButtonElement;

  beforeEach(() => {
    update.mockReset();
    update.mockResolvedValue({ ...base, id: "sch_1" });
  });

  it("hides the targets while reporting is off, but offers the sentinel", async () => {
    await renderWith({ report: false });
    expect(document.querySelector(".sched-targets")).toBeNull();
    expect(document.body.textContent).toContain("[SILENT]");
  });

  it("patches the added target and the sentinel, nothing else", async () => {
    await renderWith({ report: true, deliver_to: ["operator"] });
    expect(targetBox(t("sched.target_operator")).checked).toBe(true);
    await act(async () => {
      targetBox("Slack").click();
    });
    const silent = Array.from(document.querySelectorAll("label")).find((l) => l.textContent?.includes(t("sched.f_silent")));
    await act(async () => {
      (silent!.querySelector("input") as HTMLInputElement).click();
    });
    await act(async () => {
      saveButton().click();
    });
    expect(update).toHaveBeenCalledWith("sch_1", { deliver_to: ["operator", "slack"], silent: true });
  });

  const showBox = (): HTMLInputElement | undefined => {
    const row = Array.from(document.querySelectorAll("label")).find((l) =>
      l.textContent?.includes(t("sched.f_show_in_sessions")),
    );
    return row?.querySelector("input") as HTMLInputElement | undefined;
  };

  it("shows the Sessions-view setting off by default and patches only it when ticked", async () => {
    await renderWith({});
    expect(showBox()!.checked).toBe(false);
    await act(async () => {
      showBox()!.click();
    });
    await act(async () => {
      saveButton().click();
    });
    expect(update).toHaveBeenCalledWith("sch_1", { show_in_sessions: true });
  });

  it("reads the stored Sessions-view setting and patches the clear", async () => {
    await renderWith({ show_in_sessions: true });
    expect(showBox()!.checked).toBe(true);
    await act(async () => {
      showBox()!.click();
    });
    await act(async () => {
      saveButton().click();
    });
    expect(update).toHaveBeenCalledWith("sch_1", { show_in_sessions: false });
  });

  it("leaves a stored Sessions-view setting out of an unrelated save, toggled back or not", async () => {
    await renderWith({ show_in_sessions: true });
    // Toggle off and on again: back to the stored value, so it must not be patched.
    await act(async () => {
      showBox()!.click();
    });
    await act(async () => {
      showBox()!.click();
    });
    const label = document.querySelector("#sched-label") as HTMLInputElement;
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(label, "renamed");
      label.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      saveButton().click();
    });
    expect(update).toHaveBeenCalledWith("sch_1", { spec_label: "renamed" });
  });

  it("disables the Sessions-view setting on a reuse schedule and says why", async () => {
    await renderWith({ session_mode: "reuse" });
    expect(showBox()!.checked).toBe(false);
    expect(showBox()!.disabled).toBe(true);
  });

  it("enables the Sessions-view setting on a new-session schedule", async () => {
    await renderWith({ session_mode: "new" });
    expect(showBox()!.disabled).toBe(false);
  });

  it("keeps a stored Sessions-view true on a reuse schedule out of an unrelated save, and lets it be cleared", async () => {
    await renderWith({ session_mode: "reuse", show_in_sessions: true });
    expect(showBox()!.checked).toBe(true);
    expect(showBox()!.disabled).toBe(false);
    const label = document.querySelector("#sched-label") as HTMLInputElement;
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(label, "renamed");
      label.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      saveButton().click();
    });
    expect(update).toHaveBeenCalledWith("sch_1", { spec_label: "renamed" });
  });

  it("offers no Sessions-view setting for an assistant-mode schedule", async () => {
    await renderWith({ session_mode: "assistant" });
    expect(showBox()).toBeUndefined();
  });

  it("refuses to save a report with no target", async () => {
    await renderWith({ report: true, deliver_to: ["operator"] });
    await act(async () => {
      targetBox(t("sched.target_operator")).click();
    });
    expect(saveButton().disabled).toBe(true);
    expect(document.body.textContent).toContain(t("sched.f_deliver_to_empty"));
  });
});

// ADR 0138 D10a and the user's decision 6a on #4169: the kinds a member may point a schedule at
// are the ones the policy allows and they have not switched off, and a route can be named.
describe("ScheduleDetailModal under the credential policy", () => {
  const update = scheduleUpdate as unknown as ReturnType<typeof vi.fn>;
  const routeOptions = () => Array.from(document.querySelectorAll<HTMLOptionElement>("#sched-route option"));
  const saveButton = (): HTMLButtonElement => Array.from(document.querySelectorAll("button[type=submit]"))[0] as HTMLButtonElement;
  beforeEach(() => {
    update.mockReset();
    update.mockResolvedValue({ ...base, id: "sch_1" });
    setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, claude: { ...getSettings().agentLaunchDefaults.claude, route: undefined } } });
  });

  it("offers no kind the catalogue leaves out, and none without a catalogue", async () => {
    liveKinds = [{ kind: "claude" }, { kind: "agy" }, { kind: "codex", usableRoutes: [] }];
    await render("claude");
    // codex is allowed but nothing it runs on is held: every run would be refused.
    expect(agentOptions()).toEqual(["claude", "agy"]);
    useWorkspaceStore.setState({ state: "stopped" } as never);
    await render("claude");
    // The schedule's own kind stays, so an edit of another field cannot rewrite it.
    expect(agentOptions()).toEqual(["claude"]);
  });

  it("offers the allowed routes and the default route, and patches the one chosen", async () => {
    liveKinds = [{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api"] }];
    setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, claude: { ...getSettings().agentLaunchDefaults.claude, route: { route: "anthropic-api" } } } });
    await render("claude", { session_mode: "new" });
    const opts = routeOptions();
    expect(opts.map((o) => o.value)).toEqual(["", "anthropic-api|", "anthropic-subscription|"]);
    expect(opts[0].textContent).toBe(t("sched.route_default_saved", { route: t("cred.route.anthropic-api") }));
    const sel = document.querySelector<HTMLSelectElement>("#sched-route")!;
    await act(async () => {
      sel.value = "anthropic-subscription|";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => saveButton().click());
    expect(update).toHaveBeenCalledWith("sch_1", { route: "anthropic-subscription", route_connection: "" });
  });

  it("keeps a stored route the policy now hides without naming it", async () => {
    liveKinds = [{ kind: "claude", routes: ["anthropic-subscription"] }];
    await render("claude", { session_mode: "new", route: "anthropic-api" });
    const opts = routeOptions();
    expect(opts.map((o) => o.textContent)).toContain(t("sched.route_unavailable"));
    expect(document.body.textContent).not.toContain(t("cred.route.anthropic-api"));
    expect((document.querySelector("#sched-route") as HTMLSelectElement).value).toBe("anthropic-api|");
  });
});


describe("ScheduleDetailModal approval of a held schedule (#4631)", () => {
  const approveBtn = () =>
    Array.from(document.querySelectorAll("button")).find((b) => b.textContent === t("sched.approve")) as HTMLButtonElement | undefined;

  beforeEach(() => vi.mocked(scheduleApprove).mockReset());

  it("shows no approve action for a schedule nobody holds", async () => {
    await render("claude");
    expect(approveBtn()).toBeUndefined();
    expect(document.querySelector(".sched-held-note")).toBeNull();
  });

  it("approves the generation the modal opened on", async () => {
    vi.mocked(scheduleApprove).mockResolvedValue({ ...base, id: "sch_1" } as never);
    await render("claude", { approval_pending: true, generation: 7 });
    expect(document.querySelector(".sched-held-note")).not.toBeNull();
    await act(async () => approveBtn()!.click());
    await flush();
    expect(scheduleApprove).toHaveBeenCalledWith("sch_1", 7);
  });

  it("tells the member the schedule changed when the CP refuses a stale generation", async () => {
    vi.mocked(scheduleApprove).mockResolvedValue({ error: { code: "schedule_changed", message: "x" } } as never);
    await render("claude", { approval_pending: true, generation: 3 });
    await act(async () => approveBtn()!.click());
    await flush();
    expect(document.body.textContent).toContain(t("sched.approve_changed"));
  });

  it("does not send an approval while the form has unsaved edits", async () => {
    await render("claude", { approval_pending: true, generation: 4 });
    const ta = document.querySelector("#sched-prompt") as HTMLTextAreaElement;
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    await act(async () => {
      set.call(ta, "an edited prompt");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(approveBtn()!.disabled).toBe(true);
    await act(async () => approveBtn()!.click());
    expect(scheduleApprove).not.toHaveBeenCalled();
  });

  it("treats not_pending (approved in another tab) like a changed schedule", async () => {
    vi.mocked(scheduleApprove).mockResolvedValue({ error: { code: "not_pending", message: "x" } } as never);
    await render("claude", { approval_pending: true, generation: 3 });
    await act(async () => approveBtn()!.click());
    await flush();
    expect(document.body.textContent).toContain(t("sched.approve_changed"));
  });

  it("shows the conversation an assistant schedule runs in, explicit target first then the owner", async () => {
    await render("claude", { approval_pending: true, generation: 1, session_mode: "assistant", owner_conv: "aowner1" });
    expect(document.body.textContent).toContain("aowner1");
    act(() => root?.unmount());
    root = createRoot(host);
    await render("claude", { approval_pending: true, generation: 1, session_mode: "assistant", owner_conv: "aowner1", reuse_target: "atarget2" });
    expect(document.body.textContent).toContain("atarget2");
    expect(document.body.textContent).not.toContain("aowner1");
  });

  it("hides the stored next run while the schedule is held", async () => {
    await render("claude", { approval_pending: true, generation: 1, next_run: "2026-01-01T00:00:00Z", next_run_local: "2026-01-01 09:00" });
    expect(document.body.textContent).not.toContain("2026-01-01 09:00");
  });

  it("re-reads the list and closes when the CP says the schedule moved on", async () => {
    onCloseSpy.mockClear();
    onStaleSpy.mockClear();
    vi.mocked(scheduleApprove).mockResolvedValue({ error: { code: "schedule_changed", message: "x" } } as never);
    await render("claude", { approval_pending: true, generation: 3 });
    await act(async () => approveBtn()!.click());
    await flush();
    expect(onStaleSpy).toHaveBeenCalledTimes(1);
    expect(onCloseSpy).toHaveBeenCalled();
  });

  it("names the conversation the completion report goes to only when it can go there", async () => {
    const held = { approval_pending: true, generation: 1, owner_conv: "aownerx", session_mode: "new" };
    await render("claude", { ...held, report: true, deliver_to: ["operator"] });
    expect(document.body.textContent).toContain(t("sched.f_report_conv"));
    expect(document.body.textContent).toContain("aownerx");
    act(() => root?.unmount());
    root = createRoot(host);
    await render("claude", { ...held, report: true, deliver_to: ["notifications"] });
    expect(document.body.textContent).not.toContain(t("sched.f_report_conv"));
    act(() => root?.unmount());
    root = createRoot(host);
    await render("claude", { ...held, report: false });
    expect(document.body.textContent).not.toContain(t("sched.f_report_conv"));
  });
});
