// Tenant / identity store (zustand). Replaces the identity slice of the old
// God-context (state.tsx): whoami + memberships + active-tenant selection.
//
// Selection rules mirror the old initTenants (P3-2): a single-membership user
// never sees a picker; with several memberships the persisted choice wins if
// still valid, else the first membership. The chosen slug is persisted via
// core/api setTenant() so the global fetch wrapper stamps X-AF-Tenant.
import { create } from "zustand";
import { api, getTenant, getUser, isTransientErr, setTenant, setUser } from "../api/client.ts";
import type { Whoami, Tenant } from "../../types/app.ts";
import { confirmDirtyNavigation } from "../../features/editor/dirtyRegistry.ts";

interface TenantStore {
  whoami: Whoami | null;
  tenants: Tenant[];
  /** Active tenant slug ("" until resolved / single-tenant CP without the endpoint). */
  tenant: string;
  /** Show the picker in the top bar (only when the user has ≥2 memberships). */
  showPicker: boolean;
  superAdmin: boolean;
  /** Bumped whenever the layout-scoping identity (setUser) changes — including a
   *  DELAYED whoami resolution after a transient boot failure. App keys its
   *  per-tenant sync effect on this so load() re-runs under the user-scoped key
   *  (otherwise the layout loaded under the shared no-user key would be persisted
   *  back into the user's key once setUser lands). */
  identityRev: number;
  /** Resolve identity + memberships once at boot. */
  init(): Promise<void>;
  /** Re-read whoami after a CP reconnect (see refreshWhoami). */
  refreshWhoami(): Promise<void>;
  /** Switch the active tenant (picker). Callers re-sync their own data. */
  select(slug: string): Promise<void>;
}

// whoami は本来ブート時の 1 回きりだった。そこにはアイデンティティだけでなく
// デプロイの capability（scheduler_enabled）も乗っているため、CP が設定変更を
// 伴って再起動しても、ブラウザをリロードするまで古い値のままだった（定時実行を
// 有効化したのに左ペインにスケジュールが出ない、の一因）。push ストリームの
// 再接続を CP 再起動の合図として読み直す — ただしタブの表示/非表示でも再接続
// するので、最短間隔で間引く。
const WHOAMI_MIN_INTERVAL_MS = 30000;
let lastWhoamiAt = 0;

export const useTenantStore = create<TenantStore>((set) => ({
  whoami: null,
  tenants: [],
  tenant: getTenant(),
  showPicker: false,
  superAdmin: false,
  identityRev: 0,

  async init() {
    // One resolution attempt. Returns true on a terminal result (success or a
    // genuine app error — stop), false on a transient failure (gateway/CP booting:
    // api() resolves a plain-text 5xx as {error:{code:"http_5xx"}}, it does NOT
    // throw — see isTransientErr) so the caller retries. Crucially a transient
    // failure must NOT be treated as "no tenants": that used to run setTenant("")
    // and wipe the persisted tenant selection on every CP restart.
    const attempt = async (): Promise<boolean> => {
      let who = null;
      try {
        who = await api("api/whoami");
      } catch {
        return false; // network drop — retry
      }
      lastWhoamiAt = Date.now();
      if (who?.error) {
        if (isTransientErr(who)) return false;
        /* terminal whoami error: identity stays unresolved (display-only) — keep
           going, but never setUser("") from an {error} payload: that would degrade
           the layout key to the shared no-user key. */
      } else {
        set({ whoami: who });
        // Scope the persisted layout to this identity (email preferred, user as
        // fallback) so the next account on this browser gets a clean layout. Fast
        // path: resolved before load() runs (init awaits the first attempt → App
        // sets booted → load). Slow path (CP was down at boot, a retry resolved
        // this late): load() already ran under the shared no-user key — bump
        // identityRev so App re-runs load() under the user key instead of
        // persisting the shared-key layout into it.
        const uid = who?.email || who?.user || "";
        if (uid !== getUser()) {
          setUser(uid);
          set((s) => ({ identityRev: s.identityRev + 1 }));
        }
      }
      let data;
      try {
        data = await api("api/tenants");
      } catch {
        return false; // network drop — retry
      }
      if (data?.error) {
        // Keep the current (persisted) selection either way: a 5xx is the CP/gateway
        // still booting (retry); anything else is a CP without the endpoint
        // (dev/single-tenant) — terminal.
        return !isTransientErr(data);
      }
      const list: Tenant[] = data.tenants || [];
      set({ superAdmin: !!data.super_admin, tenants: list });
      if (list.length <= 1) {
        const slug = list[0] ? list[0].slug : "";
        setTenant(slug);
        set({ tenant: slug, showPicker: false });
        return true;
      }
      let cur = getTenant();
      if (!list.some((t) => t.slug === cur)) cur = list[0].slug;
      setTenant(cur);
      set({ tenant: cur, showPicker: true });
      return true;
    };
    // First attempt is awaited (boot fast-path unchanged); on a transient failure
    // boot proceeds and retries continue in the background with capped backoff,
    // mirroring useRetryLoad.
    let tries = 0;
    const loop = async (): Promise<void> => {
      if (await attempt()) return;
      const delay = Math.min(5000, 700 * 2 ** Math.min(tries, 3));
      tries++;
      setTimeout(() => void loop(), delay);
    };
    await loop();
  },

  async refreshWhoami() {
    if (Date.now() - lastWhoamiAt < WHOAMI_MIN_INTERVAL_MS) return;
    lastWhoamiAt = Date.now();
    let who;
    try {
      who = await api("api/whoami");
    } catch {
      return; // network drop — the next reconnect tries again
    }
    // Never clobber a resolved identity with an error payload (same rule as the
    // boot path): a 5xx during a CP restart must not blank the account chip.
    if (!who || who.error) return;
    set({ whoami: who });
    // Identity can legitimately change here (re-login as another account in the
    // same tab) — keep the layout scoping key in step, exactly as init() does.
    const uid = who.email || who.user || "";
    if (uid !== getUser()) {
      setUser(uid);
      set((s) => ({ identityRev: s.identityRev + 1 }));
    }
  },

  async select(slug: string) {
    if (slug === getTenant()) return;
    if (!(await confirmDirtyNavigation("tenant"))) return;
    setTenant(slug);
    set({ tenant: slug });
  },
}));
