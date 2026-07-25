// Tenant / identity store (zustand). Replaces the identity slice of the old
// God-context (state.tsx): whoami + memberships + active-tenant selection.
//
// Selection rules mirror the old initTenants (P3-2): a single-membership user
// never sees a picker; with several memberships the persisted choice wins if
// still valid, else the first membership. The chosen slug is persisted via
// core/api setTenant() so the global fetch wrapper stamps X-AF-Tenant.
import { create } from "zustand";
import { api, getTenant, setTenant, setUser } from "../api/client.ts";
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
    try {
      const who = await api("api/whoami");
      set({ whoami: who });
      // Scope the persisted layout to this identity (email preferred, user as
      // fallback) so the next account on this browser gets a clean layout. Resolved
      // before load() runs (init awaits here → App sets booted → load), so the key
      // is user-scoped from the first read/write.
      setUser(who?.email || who?.user || "");
    } catch {
      /* whoami is display-only — keep going */
    }
    let data;
    try {
      data = await api("api/tenants");
    } catch {
      return; // dev/single-tenant or CP without the endpoint
    }
    const list: Tenant[] = data.tenants || [];
    set({ superAdmin: !!data.super_admin, tenants: list });
    if (list.length <= 1) {
      const slug = list[0] ? list[0].slug : "";
      setTenant(slug);
      set({ tenant: slug, showPicker: false });
      return;
    }
    let cur = getTenant();
    if (!list.some((t) => t.slug === cur)) cur = list[0].slug;
    setTenant(cur);
    set({ tenant: cur, showPicker: true });
  },

  async select(slug: string) {
    if (slug === getTenant()) return;
    if (!(await confirmDirtyNavigation("tenant"))) return;
    setTenant(slug);
    set({ tenant: slug });
  },
}));
