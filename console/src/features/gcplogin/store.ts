// af-gcloud-exec's Console login requests (ADR 0107 decision 3). The notification only says
// "look": the outbox is writable by every agent, so what the toast and the modal show —
// profile, project, account, who asks — always comes from GET /api/gcp-login, which the
// Agent joins with Settings. An id the Agent does not list shows nothing, and nothing here
// ever holds an attempt id: only the modal's own press does (useGcpLoginAttempt).
import { create } from "zustand";
import { api } from "../../core/api/client.ts";

export interface GcpLoginWaiter {
  session?: string;
  command?: string;
}

export interface GcpLoginRequest {
  id: string;
  profile: string;
  label: string;
  project: string;
  /** The account Settings holds the login to; "" when the login picks it. */
  account: string;
  /** Filed because Google rejected the stored credential: the login starts afresh. */
  relogin: boolean;
  waiters: GcpLoginWaiter[];
  firstAt: string;
  lastAt: string;
}

interface GcpLoginState {
  requests: GcpLoginRequest[];
  /** Requests whose toast the member closed in this tab; the next reload shows them again. */
  hidden: Record<string, true>;
  /** The request whose login modal is open, if any. */
  modal: string | null;
  refresh(): Promise<void>;
  hide(id: string): void;
  open(id: string): void;
  close(): void;
}

function asRequest(raw: unknown): GcpLoginRequest | null {
  const r = raw as Record<string, unknown>;
  if (typeof r?.id !== "string" || !r.id || typeof r.profile !== "string" || !r.profile) return null;
  return {
    id: r.id,
    profile: r.profile,
    label: String(r.label ?? ""),
    project: String(r.project ?? ""),
    account: String(r.account ?? ""),
    relogin: r.relogin === true,
    waiters: Array.isArray(r.waiters) ? (r.waiters as GcpLoginWaiter[]) : [],
    firstAt: String(r.firstAt ?? ""),
    lastAt: String(r.lastAt ?? ""),
  };
}

export const useGcpLoginStore = create<GcpLoginState>((set) => ({
  requests: [],
  hidden: {},
  modal: null,
  async refresh() {
    let d: { requests?: unknown[]; error?: unknown } | null = null;
    try {
      d = await api("api/gcp-login");
    } catch {
      return; // a dropped connection proves nothing; the next poll asks again
    }
    if (!d || d.error || !Array.isArray(d.requests)) return;
    set({ requests: d.requests.map(asRequest).filter((r): r is GcpLoginRequest => r !== null) });
  },
  hide(id) {
    set((s) => ({ hidden: { ...s.hidden, [id]: true } }));
  },
  open(id) {
    set({ modal: id });
  },
  close() {
    set({ modal: null });
  },
}));

/** Who asks, as one line: the sessions and commands of the waiters, never the account. */
export function gcpWaitersLine(r: GcpLoginRequest): string {
  const parts = r.waiters.map((w) => [w.session, w.command].filter(Boolean).join(" · ")).filter(Boolean);
  return Array.from(new Set(parts)).join(", ");
}
