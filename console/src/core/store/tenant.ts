// Tenant / identity store (zustand). Replaces the identity slice of the old
// God-context (state.tsx): whoami + memberships + active-tenant selection.
//
// Selection rules mirror the old initTenants (P3-2): a single-membership user
// never sees a picker; with several memberships the persisted choice wins if
// still valid, else the first membership. The chosen slug is persisted via
// core/api setTenant() so the global fetch wrapper stamps X-AF-Tenant.
import { create } from "zustand";
import { api, getTenant, isTransientErr, setTenant, setUser } from "../api/client.ts";
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
  /** Resolve identity + memberships once at boot. */
  init(): Promise<void>;
  /** Switch the active tenant (picker). Callers re-sync their own data. */
  select(slug: string): Promise<void>;
}

export const useTenantStore = create<TenantStore>((set) => ({
  whoami: null,
  tenants: [],
  tenant: getTenant(),
  showPicker: false,
  superAdmin: false,

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
      if (who?.error) {
        if (isTransientErr(who)) return false;
        /* terminal whoami error: identity stays unresolved (display-only) — keep
           going, but never setUser("") from an {error} payload: that would degrade
           the layout key to the shared no-user key. */
      } else {
        set({ whoami: who });
        // Scope the persisted layout to this identity (email preferred, user as
        // fallback) so the next account on this browser gets a clean layout. Resolved
        // before load() runs (init awaits here → App sets booted → load), so the key
        // is user-scoped from the first read/write.
        setUser(who?.email || who?.user || "");
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

  async select(slug: string) {
    if (slug === getTenant()) return;
    if (!(await confirmDirtyNavigation("tenant"))) return;
    setTenant(slug);
    set({ tenant: slug });
  },
}));
