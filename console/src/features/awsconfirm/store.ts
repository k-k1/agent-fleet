// "Confirm before use" requests (ADR 0126 decision 5). The notification only says "look": the
// outbox is writable by every agent, so what a card shows (profile, account, role, session)
// always comes from GET /api/aws-confirm, which the Agent joins with Settings and drops when
// Settings no longer matches what the request was filed for.
import { create } from "zustand";
import { api, getTenant } from "../../core/api/client.ts";

/** One profile a request asks about, as Settings defines it now. */
export interface AwsConfirmTarget {
  profile: string;
  label: string;
  accountId: string;
  roleName: string;
  region?: string;
  /** A chain: the profile it assumes its role through. */
  source?: string;
}

/** One pending request: an af-aws-exec run in `session` waits for the member's answer. */
export interface AwsConfirmRequest {
  id: string;
  session: string;
  targets: AwsConfirmTarget[];
  commands?: string[];
  firstAt: string;
  lastAt: string;
  expiresInSec: number;
}

interface AwsConfirmState {
  requests: AwsConfirmRequest[];
  /** Requests answered from this tab, whose card stays off until the list drops them. */
  answering: Record<string, true>;
  /** Sessions whose mirror is on screen in this tab: their requests show as a card there, not a toast. */
  mirrors: Record<string, number>;
  refresh(): Promise<void>;
  reset(): void;
  /** Answers a request; returns the Agent's error code, or "" on success. */
  answer(r: AwsConfirmRequest, verb: "approve" | "deny"): Promise<string>;
  mirrorShown(session: string): () => void;
}

export const useAwsConfirmStore = create<AwsConfirmState>((set, get) => ({
  requests: [],
  answering: {},
  mirrors: {},
  async refresh() {
    const tenant = getTenant();
    let d: { requests?: AwsConfirmRequest[]; error?: unknown } | null = null;
    try {
      d = await api("api/aws-confirm");
    } catch {
      return; // a dropped connection proves nothing; the next poll asks again
    }
    if (!d || d.error || !Array.isArray(d.requests) || getTenant() !== tenant) return;
    const ids = new Set(d.requests.map((r) => r.id));
    set((s) => ({
      requests: d!.requests!,
      answering: Object.fromEntries(Object.keys(s.answering).filter((id) => ids.has(id)).map((id) => [id, true as const])),
    }));
  },
  reset() {
    set({ requests: [], answering: {} });
  },
  async answer(r, verb) {
    set((s) => ({ answering: { ...s.answering, [r.id]: true } }));
    // The hints are for the CP's audit only; the Agent decides from the id alone.
    const q = new URLSearchParams({ profile: r.targets.map((t) => t.profile).join(","), session: r.session });
    let code = "";
    try {
      const d = await api(`api/aws-confirm/${encodeURIComponent(r.id)}/${verb}?${q}`, { method: "POST" });
      if (d?.error) code = String(d.error.code || "failed");
    } catch {
      code = "failed";
    }
    if (code) {
      set((s) => {
        const { [r.id]: _, ...rest } = s.answering;
        return { answering: rest };
      });
    }
    await get().refresh();
    return code;
  },
  mirrorShown(session) {
    set((s) => ({ mirrors: { ...s.mirrors, [session]: (s.mirrors[session] ?? 0) + 1 } }));
    return () =>
      set((s) => {
        const n = (s.mirrors[session] ?? 1) - 1;
        const { [session]: _, ...rest } = s.mirrors;
        return { mirrors: n > 0 ? { ...rest, [session]: n } : rest };
      });
  },
}));
