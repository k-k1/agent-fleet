// The tenant sign-in surface (docs/log/61 §61.9 / §61.11, ADR0043 decision 19 / 29-33).
//
// Two places host it — the deployment admin's admin modal and the tenant admin's tenant
// settings modal — so both have to be able to mount the same implementation. The only
// per-reader difference is props (isSuper / read-only); the permission itself always lives on
// the server:
//   - PUT of the login rules is fixed to withSuperAdmin (decision 19)
//   - "approve and enable" for a sign-in method is decided by the CP's setStatus looking at
//     super_admin (decision 30)
// What the UI shows is guidance, not the implementation of the permission.
//
// Three files in this family: here (the types both sides read), tenantLoginRules.tsx (login
// rules) and tenantSignInMethods.tsx (sign-in method list / editor / registry).

// Only the three tenant-row columns this surface reads (docs/log/61 §61.9.7). A subset of the
// admin API's tenant representation, so the caller can pass its own type straight through.
export interface TenantLoginFields {
  allowed_providers?: string;
  auto_join_domains?: string;
  allowed_domains?: string;
  // Methods that are accepted but not shown on this tenant's sign-in screen
  // (docs/log/61 §61.15.9). Display only — this is not a gate.
  hidden_providers?: string;
}

// A sign-in method defined by the tenant (docs/log/61 §61.11). client_secret is write-only:
// it never appears in a response, and has_secret is how you tell whether one is stored.
export interface TenantIdP {
  id: string;
  name: string;
  label_ja?: string;
  label_en?: string;
  // kind picks the adapter, which changes which fields are shown at all
  // (docs/log/61 §61.15). Defaults to oidc, since P4 rows predate this column.
  kind?: string;
  issuer: string;
  client_id: string;
  client_secret?: string;
  trust: string;
  allowed_tids?: string;
  allowed_domains?: string;
  allowed_orgs?: string;
  // The stable claim name rule 1.5 keys on (docs/log/61 §61.15.10). Only the *name* is
  // configured, never the value: the value is always read from the token. Only names the CP
  // permits can be written here.
  link_claim?: string;
  provider_id?: string;
  tenant_slug?: string;
  status?: string;
  has_secret?: boolean;
  approved_by?: string;
  approved_at?: string;
  usable?: boolean;
}
