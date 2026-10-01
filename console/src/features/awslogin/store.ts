// af-aws-exec's Console login requests (ADR 0102). The notification only says "look": the
// outbox is writable by every agent, so what the toast and the modal show — profile,
// account, role, who asks — always comes from GET /api/aws-login, which the Agent joins
// with Settings. An id the Agent does not list shows nothing.
import { create } from "zustand";
import { api } from "../../core/api/client.ts";

export interface AwsLoginWaiter {
  session?: string;
  command?: string;
}

export interface AwsLoginRequest {
  id: string;
  profile: string;
  label: string;
  accountId: string;
  roleName: string;
  waiters: AwsLoginWaiter[];
  firstAt: string;
  lastAt: string;
}

/** A Settings profile whose cached login ends soon, as GET /api/aws-login/profiles lists it (#1029). */
export interface AwsProfileExpiry {
  name: string;
  label: string;
  accountId: string;
  roleName: string;
  /** The end the Agent knows, RFC 3339 UTC. The Agent decides "expiring" on its own clock. */
  expiresAt: string;
}

/** One warning per profile and end: a re-login moves the end, so it can warn again. */
export const expiryKey = (p: AwsProfileExpiry) => p.name + "|" + p.expiresAt;

interface AwsLoginState {
  requests: AwsLoginRequest[];
  /** Requests whose toast the member closed in this tab; the next reload shows them again. */
  hidden: Record<string, true>;
  /** The request whose login modal is open, if any. */
  modal: string | null;
  refresh(): Promise<void>;
  hide(id: string): void;
  open(id: string): void;
  close(): void;
  /** Profiles the Agent lists as expiring. */
  expiring: AwsProfileExpiry[];
  /** Expiry warnings (expiryKey) the member closed in this tab. */
  hiddenExpiry: Record<string, true>;
  /** The profile whose login modal an expiry warning opened, if any. */
  profileModal: string | null;
  refreshExpiry(): Promise<void>;
  hideExpiry(key: string): void;
  openProfile(name: string): void;
  closeProfile(): void;
}

export const useAwsLoginStore = create<AwsLoginState>((set) => ({
  requests: [],
  hidden: {},
  modal: null,
  async refresh() {
    let d: { requests?: AwsLoginRequest[]; error?: unknown } | null = null;
    try {
      d = await api("api/aws-login");
    } catch {
      return; // a dropped connection proves nothing; the next poll asks again
    }
    if (!d || d.error || !Array.isArray(d.requests)) return;
    set({ requests: d.requests });
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
  expiring: [],
  hiddenExpiry: {},
  profileModal: null,
  async refreshExpiry() {
    let d: { profiles?: unknown[]; error?: unknown } | null = null;
    try {
      d = await api("api/aws-login/profiles");
    } catch {
      return;
    }
    if (!d || d.error || !Array.isArray(d.profiles)) return;
    const expiring: AwsProfileExpiry[] = [];
    for (const raw of d.profiles) {
      const p = raw as Record<string, unknown>;
      if (p?.expiring !== true || typeof p.name !== "string" || !p.name || typeof p.expiresAt !== "string") continue;
      expiring.push({
        name: p.name,
        label: String(p.label ?? ""),
        accountId: String(p.accountId ?? ""),
        roleName: String(p.roleName ?? ""),
        expiresAt: p.expiresAt,
      });
    }
    set({ expiring });
  },
  hideExpiry(key) {
    set((s) => ({ hiddenExpiry: { ...s.hiddenExpiry, [key]: true } }));
  },
  openProfile(name) {
    set({ profileModal: name });
  },
  closeProfile() {
    set({ profileModal: null });
  },
}));

/** Who asks, as one line: the sessions and commands of the waiters, never the account. */
export function waitersLine(r: AwsLoginRequest): string {
  const parts = r.waiters.map((w) => [w.session, w.command].filter(Boolean).join(" · ")).filter(Boolean);
  return Array.from(new Set(parts)).join(", ");
}
