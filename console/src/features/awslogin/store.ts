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

/** A Settings profile and its login, as GET /api/aws-login/profiles lists it (#1477). */
export interface AwsProfileState {
  name: string;
  label: string;
  accountId: string;
  roleName: string;
  /** "signed_in" | "renew" | "none" — the Agent's reading of the profile's token cache. */
  state: string;
  /** The login's end when it cannot renew, RFC 3339 UTC; "" when no end is known. */
  expiresAt: string;
  expiring: boolean;
}

/** What a profile's login modal needs; a subset of both lists, so either can open it. */
export interface AwsLoginTarget {
  name: string;
  label: string;
  accountId: string;
  roleName: string;
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
  /** Every Settings profile the Agent last listed; null until it has answered once. */
  profiles: AwsProfileState[] | null;
  /** Profiles the Agent lists as expiring. */
  expiring: AwsProfileExpiry[];
  /** Expiry warnings (expiryKey) the member closed in this tab. */
  hiddenExpiry: Record<string, true>;
  /**
   * The profile whose login modal an expiry warning opened, as the Agent listed it then. A
   * snapshot: once open, the modal outlives the warning (the old token may run out while the
   * device code waits), and only closing it clears this.
   */
  profileModal: AwsLoginTarget | null;
  /** Returns the fresh list, or null when the Agent could not be asked (the old list stays). */
  refreshExpiry(): Promise<AwsProfileExpiry[] | null>;
  hideExpiry(key: string): void;
  /** Opens the modal for a profile from the Agent's list (the toast, the WS bar chip). */
  showProfile(p: AwsLoginTarget): void;
  /** Opens the modal for a name from a notification, only if the Agent lists it as expiring. */
  openProfile(name: string): Promise<void>;
  closeProfile(): void;
  /** Profiles a logout is running for; their buttons stay off until it answers. */
  loggingOut: Record<string, true>;
  /** Logs the workspace out of one profile, then re-reads the list. A second call while one runs is refused. */
  logoutProfile(name: string): Promise<AwsLogoutResult>;
}

/**
 * The Agent's answer to a logout. revoked is false when AWS could not be told (the workspace
 * is signed out all the same) or when there was no login to revoke (noToken).
 */
export type AwsLogoutResult =
  | { ok: true; revoked: boolean; noToken: boolean; message: string }
  | { ok: false; code: string; message: string };

export const useAwsLoginStore = create<AwsLoginState>((set, get) => ({
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
  profiles: null,
  expiring: [],
  hiddenExpiry: {},
  profileModal: null,
  async refreshExpiry() {
    let d: { profiles?: unknown[]; error?: unknown } | null = null;
    try {
      d = await api("api/aws-login/profiles");
    } catch {
      return null;
    }
    if (!d || d.error || !Array.isArray(d.profiles)) return null;
    const profiles: AwsProfileState[] = [];
    for (const raw of d.profiles) {
      const p = raw as Record<string, unknown>;
      if (typeof p?.name !== "string" || !p.name) continue;
      profiles.push({
        name: p.name,
        label: String(p.label ?? ""),
        accountId: String(p.accountId ?? ""),
        roleName: String(p.roleName ?? ""),
        state: String(p.state ?? ""),
        expiresAt: typeof p.expiresAt === "string" ? p.expiresAt : "",
        expiring: p.expiring === true,
      });
    }
    const expiring: AwsProfileExpiry[] = profiles
      .filter((p) => p.expiring && p.expiresAt)
      .map(({ name, label, accountId, roleName, expiresAt }) => ({ name, label, accountId, roleName, expiresAt }));
    set({ profiles, expiring });
    return expiring;
  },
  hideExpiry(key) {
    set((s) => ({ hiddenExpiry: { ...s.hiddenExpiry, [key]: true } }));
  },
  showProfile(p) {
    set({ profileModal: p });
  },
  async openProfile(name) {
    // Only this answer counts: a failed ask leaves the old list, which may name a profile that
    // has since been logged in again or ended.
    const fresh = await get().refreshExpiry();
    const p = fresh?.find((x) => x.name === name);
    if (p) set({ profileModal: p });
  },
  closeProfile() {
    set({ profileModal: null });
  },
  loggingOut: {},
  async logoutProfile(name) {
    if (get().loggingOut[name]) return { ok: false, code: "busy", message: "" };
    set((s) => ({ loggingOut: { ...s.loggingOut, [name]: true } }));
    let d: { revoked?: unknown; noToken?: unknown; message?: unknown; error?: { code?: string; message?: string } } | null;
    try {
      d = await api(`api/aws-login/profiles/${encodeURIComponent(name)}/logout`, { method: "POST" });
    } catch (e) {
      return { ok: false, code: "", message: String((e as Error)?.message || e) };
    } finally {
      set((s) => {
        const { [name]: _, ...rest } = s.loggingOut;
        return { loggingOut: rest };
      });
    }
    // Whatever the answer, the cache may have changed under the list.
    void get().refreshExpiry();
    if (!d || d.error) return { ok: false, code: d?.error?.code || "", message: d?.error?.message || "" };
    return { ok: true, revoked: d.revoked === true, noToken: d.noToken === true, message: String(d.message ?? "") };
  },
}));

/** Who asks, as one line: the sessions and commands of the waiters, never the account. */
export function waitersLine(r: AwsLoginRequest): string {
  const parts = r.waiters.map((w) => [w.session, w.command].filter(Boolean).join(" · ")).filter(Boolean);
  return Array.from(new Set(parts)).join(", ");
}
