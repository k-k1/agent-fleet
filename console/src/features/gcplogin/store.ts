// af-gcloud-exec's Console login requests (ADR 0107 decision 3). The notification only says
// "look": the outbox is writable by every agent, so what the toast and the modal show —
// profile, project, account, who asks — always comes from GET /api/gcp-login, which the
// Agent joins with Settings. An id the Agent does not list shows nothing, and nothing here
// ever holds an attempt id: only the modal's own press does (useGcpLoginAttempt).
import { create } from "zustand";
import { api, getTenant } from "../../core/api/client.ts";

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

/** A Settings profile and its login, as GET /api/gcp-login/profiles lists it (the WS bar badge). */
export interface GcpProfileState {
  name: string;
  label: string;
  project: string;
  /** The account the profile's gcloud configuration selects; "" before a login chose one. */
  account: string;
  /** "signed_in" | "none". The Agent cannot tell a login Google revoked from a good one
   *  until something uses it, so "signed_in" means "a user credential is stored". */
  state: string;
}

/** The profile login modal the badge opened: force is "Log in again". */
export interface GcpProfileModal {
  profile: { name: string; label: string; project: string; account: string };
  force: boolean;
}

interface GcpLoginState {
  requests: GcpLoginRequest[];
  /** Requests whose toast the member closed in this tab; the next reload shows them again. */
  hidden: Record<string, true>;
  /** The request whose login modal is open, if any. */
  modal: string | null;
  refresh(): Promise<void>;
  /** Back to the never-asked state: everything here describes the previous tenant's workspace. */
  reset(): void;
  hide(id: string): void;
  open(id: string): void;
  close(): void;
  /** Every Settings profile the Agent last listed; null until it has answered once. */
  profiles: GcpProfileState[] | null;
  /** Asks the Agent for the profiles' login states; a failed ask keeps the old list. */
  /** true once the Agent answered (and the answer is still for the current tenant). */
  refreshProfiles(): Promise<boolean>;
  /**
   * refreshProfiles for a caller that acts on the answer: the list this ask got, or null. The
   * store keeps the answer of the latest ask only, so a slower, older one never overwrites it.
   */
  loadProfiles(): Promise<GcpProfileState[] | null>;
  /**
   * Settings changed a profile. The Agent reads Settings on its own pull, every five minutes
   * (cloudbridge.PollInterval), and its profile list is that pull's, so this asks now and
   * once more after the next pull is due; a later change re-arms the one wait.
   */
  settingsChanged(): void;
  profileModal: GcpProfileModal | null;
  showProfile(m: GcpProfileModal): void;
  closeProfile(): void;
  /** Profiles a logout is running for, each with its call's id; their buttons stay off until it answers. */
  loggingOut: Record<string, number>;
  /**
   * Signs the workspace out of account, the profile's account the member confirmed, then
   * re-reads the list. tenant is the tenant the member pressed under: under any other the call
   * sends nothing ("tenant_changed"). A second call while one runs is refused ("in_flight").
   */
  logoutProfile(name: string, account: string, tenant: string): Promise<GcpLogoutResult>;
}

/**
 * The Agent's answer to a logout. gcloud's store is per account, so every profile that
 * selected the account (profiles) is signed out with it. "account_changed": a login selected
 * another account since the confirmation. Nothing is revoked at Google.
 */
export type GcpLogoutResult = { ok: true; account: string; profiles: string[] } | { ok: false; code: string; message: string };

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

// The Agent's Settings pull interval plus slack for the pull itself.
export const SETTINGS_SYNC_MS = 5 * 60_000 + 15_000;
let syncTimer: ReturnType<typeof setTimeout> | undefined;
let logoutSeq = 0;
let profilesSeq = 0;

export const useGcpLoginStore = create<GcpLoginState>((set, get) => ({
  requests: [],
  hidden: {},
  modal: null,
  async refresh() {
    const tenant = getTenant();
    let d: { requests?: unknown[]; error?: unknown } | null = null;
    try {
      d = await api("api/gcp-login");
    } catch {
      return; // a dropped connection proves nothing; the next poll asks again
    }
    if (!d || d.error || !Array.isArray(d.requests)) return;
    if (getTenant() !== tenant) return; // asked under the previous tenant
    set({ requests: d.requests.map(asRequest).filter((r): r is GcpLoginRequest => r !== null) });
  },
  reset() {
    clearTimeout(syncTimer);
    set({ requests: [], hidden: {}, modal: null, profiles: null, profileModal: null, loggingOut: {} });
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
  async refreshProfiles() {
    return (await get().loadProfiles()) !== null;
  },
  async loadProfiles() {
    const tenant = getTenant();
    const seq = ++profilesSeq;
    let d: { profiles?: unknown[]; error?: unknown } | null = null;
    try {
      d = await api("api/gcp-login/profiles");
    } catch {
      return null;
    }
    if (!d || d.error || !Array.isArray(d.profiles)) return null;
    if (getTenant() !== tenant) return null; // asked under the previous tenant
    const profiles: GcpProfileState[] = [];
    for (const raw of d.profiles) {
      const p = raw as Record<string, unknown>;
      if (typeof p?.name !== "string" || !p.name) continue;
      profiles.push({
        name: p.name,
        label: String(p.label ?? ""),
        project: String(p.project ?? ""),
        account: String(p.account ?? ""),
        state: String(p.state ?? ""),
      });
    }
    if (seq === profilesSeq) set({ profiles });
    return profiles;
  },
  settingsChanged() {
    void get().refreshProfiles();
    clearTimeout(syncTimer);
    syncTimer = setTimeout(() => void get().refreshProfiles(), SETTINGS_SYNC_MS);
  },
  profileModal: null,
  showProfile(m) {
    set({ profileModal: m });
  },
  closeProfile() {
    set({ profileModal: null });
  },
  loggingOut: {},
  async logoutProfile(name, account, tenant) {
    // The request carries the selected tenant at the time it is sent: a press made under
    // another one would sign out a same-named profile of a different workspace.
    if (getTenant() !== tenant) return { ok: false, code: "tenant_changed", message: "" };
    // typeof, not truthiness: a profile named "constructor" would read Object.prototype's.
    // "in_flight", not the Agent's "busy" (another login holds the store), which is shown.
    if (typeof get().loggingOut[name] === "number") return { ok: false, code: "in_flight", message: "" };
    const id = ++logoutSeq;
    set((s) => ({ loggingOut: { ...s.loggingOut, [name]: id } }));
    let d: { account?: unknown; profiles?: unknown; error?: { code?: string; message?: string } } | null;
    try {
      d = await api(`api/gcp-login/profiles/${encodeURIComponent(name)}/logout`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ account }),
      });
    } catch (e) {
      d = { error: { code: "", message: String((e as Error)?.message || e) } };
    } finally {
      // Only this call's mark: after a tenant switch (reset) the name may be another call's.
      set((s) => {
        if (s.loggingOut[name] !== id) return {};
        const { [name]: _, ...rest } = s.loggingOut;
        return { loggingOut: rest };
      });
    }
    // An answer about the previous tenant's workspace says nothing about this one.
    if (getTenant() !== tenant) return { ok: false, code: "tenant_changed", message: "" };
    // Whatever the answer, the store may have changed under the list.
    void get().refreshProfiles();
    if (!d || d.error) return { ok: false, code: d?.error?.code || "", message: d?.error?.message || "" };
    const profiles = Array.isArray(d.profiles) ? d.profiles.filter((p): p is string => typeof p === "string") : [];
    return { ok: true, account: String(d.account ?? ""), profiles };
  },
}));

/** Who asks, as one line: the sessions and commands of the waiters, never the account. */
export function gcpWaitersLine(r: GcpLoginRequest): string {
  const parts = r.waiters.map((w) => [w.session, w.command].filter(Boolean).join(" · ")).filter(Boolean);
  return Array.from(new Set(parts)).join(", ");
}
