// App-level shared types: the signed-in identity and tenant membership shapes
// (consumed by core/store/tenant.ts). The old God-context types (AppState /
// PanePatch / Reveal) died with the pane-store migration and were removed —
// layout types now live in src/layout/types.ts.

// GET /api/whoami — the signed-in identity. email/user are the fields the UI reads.
// scheduler_enabled is a deployment capability flag (AF_SCHEDULER_INTERVAL is set): the
// left-rail schedules section is hidden when it is false, since nothing can ever fire.
// home_wipe / home_erase / home_backups say which operations that remove part of a home this
// deployment's runtime can perform (control-plane/internal/runtime/home_wipe.go): a member's
// Recreate and Clean home, an administrator's Clean home, and deleting a member's backup
// copies. Where one is false the CP refuses it, so the Console does not offer it; absent (a
// whoami not loaded yet) counts as false, because these buttons destroy data.
export interface Whoami {
  email?: string;
  user?: string;
  scheduler_enabled?: boolean;
  home_wipe?: boolean;
  home_erase?: boolean;
  home_backups?: boolean;
  [k: string]: unknown;
}

// A tenant membership from GET /api/tenants.
export interface Tenant {
  slug: string;
  name?: string;
  role?: string;
  /** Sign-in methods this tenant accepts (docs/log/61 §61.9.4); empty/absent = any.
   *  Used to turn a `provider_required` refusal into a re-sign-in link. */
  allowed_providers?: string[];
}
