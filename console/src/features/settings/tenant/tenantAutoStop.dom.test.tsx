// A workspace the start deadline stopped must not read as plain "stopped" to its tenant admin
// (#1384): the member's notification never reaches them, and the CP log is not theirs to read.
// The roster flags the row and the member detail says why, worded exactly as the member's
// notification words it.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(() => Promise.resolve({})),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
  getTenant: () => "",
  getUser: () => "",
  setTenant: () => {},
  setUser: () => {},
  isTransientErr: () => false,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { MembersPanel } from "./tenantMembers.tsx";
import { MemberView } from "./tenantMemberDetail.tsx";
import { notificationWording } from "../../notifications/wording.ts";
import { setLocale } from "../../../lib/i18n/index.ts";
import { en } from "../../../lib/i18n/locales/en.ts";

const PHASE = "blocked: no container instance met all of its requirements";
const STOPPED = {
  user_key: "a",
  email: "a@x.com",
  role: "member",
  state: "stopped",
  status: "active",
  auto_stop: { kind: "start-deadline", phase: PHASE, limit_minutes: 30, stopped_at: "2026-10-01T10:00:00Z" },
};
const PLAIN = { user_key: "b", email: "b@x.com", role: "member", state: "stopped", status: "active" };
const memberBody = () =>
  notificationWording({ kind: "start-deadline", displayName: "", payload: { limitMinutes: 30, phase: PHASE } }).body;

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function render(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(node);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => setLocale("en"));

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
  setLocale("ja");
});

describe("a start the Control Plane stopped", () => {
  it("is flagged on the roster row, with the member's own wording as its title", async () => {
    api.mockImplementation((p: string) =>
      p === "api/admin/workspace-sizing" ? Promise.resolve({}) : Promise.resolve({ members: [STOPPED, PLAIN] }),
    );
    await render(<MembersPanel slug="acme" isSuper={false} onOpenMember={() => {}} />);
    const rows = document.querySelectorAll(".member-row");
    const chip = rows[0].querySelector(".mr-idle.hold");
    expect(chip?.textContent).toBe(en["noti.kind_start_deadline"]);
    // Same function, same words: the admin and the member must not read one stop two ways.
    expect(chip?.getAttribute("title")).toBe(memberBody());
    expect(rows[1].querySelector(".mr-idle")).toBeNull();
  });

  it("names the limit, the last phase and the time in the member detail", async () => {
    api.mockImplementation((p: string) =>
      Promise.resolve(p.endsWith("/stats") ? { running: false, auto_stop: STOPPED.auto_stop } : { sessions: [] }),
    );
    await render(
      <MemberView slug="acme" member={STOPPED} isSuper={false} onChanged={() => {}} onRemoved={() => {}} />,
    );
    const text = (document.body.textContent || "").replace(/\s+/g, " ");
    expect(text).toContain(en["noti.kind_start_deadline"]);
    expect(text).toContain(`It was still starting after 30 min. Last step: ${en["wsstart.blocked"]} — ${PHASE}`);
    expect(text).toContain(en["admin.auto_stop_at"].split("{at}")[1].trim());
  });

  // The detail is opened from a roster snapshot that still carries auto_stop. Once the member
  // restarts, the poll is what counts: the header says Running and the old reason must go,
  // and a launch still starting has no reason either (the CP omits it then).
  for (const [name, stats] of [
    ["running", { running: true }],
    ["starting", { running: false, starting: true }],
  ] as const) {
    it(`drops the snapshot's reason once the poll says ${name}`, async () => {
      api.mockImplementation((p: string) => Promise.resolve(p.endsWith("/stats") ? stats : { sessions: [] }));
      await render(
        <MemberView slug="acme" member={STOPPED} isSuper={false} onChanged={() => {}} onRemoved={() => {}} />,
      );
      expect(document.body.textContent).not.toContain(en["noti.kind_start_deadline"]);
    });
  }

  it("says nothing extra for a member without the record", async () => {
    api.mockResolvedValue({ running: false, sessions: [] });
    await render(<MemberView slug="acme" member={PLAIN} isSuper={false} onChanged={() => {}} onRemoved={() => {}} />);
    expect(document.body.textContent).not.toContain(en["noti.kind_start_deadline"]);
  });
});
