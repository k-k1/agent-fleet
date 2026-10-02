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
  hide(id: string): void;
  open(id: string): void;
  close(): void;
  /** Every Settings profile the Agent last listed; null until it has answered once. */
  profiles: GcpProfileState[] | null;
  /** Asks the Agent for the profiles' login states; a failed ask keeps the old list. */
  refreshProfiles(): Promise<void>;
  /**
   * Settings changed a profile. The Agent reads Settings on its own pull, every five minutes
   * (cloudbridge.PollInterval), and its profile list is that pull's, so this asks now and
   * once more after the next pull is due; a later change re-arms the one wait.
   */
  settingsChanged(): void;
  profileModal: GcpProfileModal | null;
  showProfile(m: GcpProfileModal): void;
  closeProfile(): void;
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

// The Agent's Settings pull interval plus slack for the pull itself.
export const SETTINGS_SYNC_MS = 5 * 60_000 + 15_000;
let syncTimer: ReturnType<typeof setTimeout> | undefined;

export const useGcpLoginStore = create<GcpLoginState>((set, get) => ({
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
  profiles: null,
  async refreshProfiles() {
    let d: { profiles?: unknown[]; error?: unknown } | null = null;
    try {
      d = await api("api/gcp-login/profiles");
    } catch {
      return;
    }
    if (!d || d.error || !Array.isArray(d.profiles)) return;
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
    set({ profiles });
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
}));

/** Who asks, as one line: the sessions and commands of the waiters, never the account. */
export function gcpWaitersLine(r: GcpLoginRequest): string {
  const parts = r.waiters.map((w) => [w.session, w.command].filter(Boolean).join(" · ")).filter(Boolean);
  return Array.from(new Set(parts)).join(", ");
}
