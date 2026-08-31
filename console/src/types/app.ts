// App-level shared types: the signed-in identity and tenant membership shapes
// (consumed by core/store/tenant.ts). The old God-context types (AppState /
// PanePatch / Reveal) died with the pane-store migration and were removed —
// layout types now live in src/layout/types.ts.

// GET /api/whoami — the signed-in identity. email/user are the fields the UI reads.
// scheduler_enabled is a deployment capability flag (AF_SCHEDULER_INTERVAL is set): the
// left-rail schedules section is hidden when it is false, since nothing can ever fire.
export interface Whoami {
  email?: string;
  user?: string;
  scheduler_enabled?: boolean;
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
