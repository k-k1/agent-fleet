// Provided routes (ADR 0129 D3, D4) as the Model access pages read them: the deployment's
// Bedrock source (super_admin), each tenant's grant and monthly ceiling (super_admin), the
// tenant's member policy and per-member grants and budgets (tenant_admin), with this month's
// spend.
//
// The shapes mirror control-plane/provided_routes.go and internal/routegateway (SourceView,
// SourceInput, TenantView, MemberView, GrantInput, PolicyInput, MemberInput). Every PUT there is
// decoded with DisallowUnknownFields and takes a narrower shape than its GET answers, so a body
// is built from the input fields alone (the loaded values with the edited ones replaced), never
// by echoing the view.
//
// The routes exist only on a CP started with provided routes on; otherwise the mux has no such
// path and answers a plain-text 404, which api() turns into code "http_404". That one answer
// means "off" and draws nothing. A JSON 404 (no_tenant) or any other failure is a failure.
//
// Pure data and functions only: no store, no component.

/** routegateway.SourceView. */
export interface SourceView {
  id: string;
  binding: string;
  name: string;
  role_arn: string;
  region: string;
  models: string[];
  fingerprint: string;
  policy_version: number;
  active: boolean;
  updated_at: string;
}

/** routegateway.SourceInput. active is always sent: the CP reads an absent one as true. */
export interface SourceInput {
  name: string;
  role_arn: string;
  region: string;
  models: string[];
  active: boolean;
}

export interface ModelReservation {
  model: string;
  min_micro: number;
  max_output_micro: number;
}

/** routegateway.MemberView. budget_micro null = the tenant's default. */
export interface ProvidedMember {
  membership_id: string;
  user_key: string;
  email: string;
  granted: boolean;
  budget_micro: number | null;
  effective_micro: number;
  spent_micro: number;
  reserved_micro: number;
}

/** routegateway.TenantView. */
export interface ProvidedTenantView {
  granted: boolean;
  ceiling_micro: number | null;
  members_only: boolean;
  member_default_micro: number;
  month: string;
  spent_micro: number;
  reserved_micro: number;
  members: ProvidedMember[];
  reservations: ModelReservation[];
  floor_micro: number;
}

export interface GrantInput {
  granted: boolean;
  ceiling_micro: number | null;
}

export interface PolicyInput {
  members_only: boolean;
  member_default_micro: number;
}

export interface MemberInput {
  membership_id: string;
  granted: boolean;
  budget_micro: number | null;
}

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const num = (v: unknown) => typeof v === "number" && Number.isFinite(v);
const numOrNull = (v: unknown) => v === null || num(v);
const strs = (v: unknown) => Array.isArray(v) && v.every((s) => typeof s === "string");

/** Load is what a provided-routes read came to: off (the CP does not serve them), failed, or the
 *  value. */
export type Load<T> = { state: "off" } | { state: "failed"; reason: string | null } | { state: "ok"; value: T };

/** isOff: the plain-text 404 of a CP without the routes. */
export const isOff = (d: unknown) => isObj(d) && isObj(d.error) && d.error.code === "http_404";

export function parseSource(d: unknown): { source: SourceView | null } | null {
  if (!isObj(d) || !("source" in d)) return null;
  const s = d.source;
  if (s === null) return { source: null };
  if (
    !isObj(s) ||
    typeof s.id !== "string" ||
    typeof s.name !== "string" ||
    typeof s.role_arn !== "string" ||
    typeof s.region !== "string" ||
    !strs(s.models) ||
    typeof s.fingerprint !== "string" ||
    !num(s.policy_version) ||
    typeof s.active !== "boolean" ||
    typeof s.updated_at !== "string"
  )
    return null;
  return { source: s as unknown as SourceView };
}

const isMember = (m: unknown): m is ProvidedMember =>
  isObj(m) &&
  typeof m.membership_id === "string" &&
  typeof m.user_key === "string" &&
  typeof m.granted === "boolean" &&
  numOrNull(m.budget_micro) &&
  num(m.effective_micro) &&
  num(m.spent_micro) &&
  num(m.reserved_micro);

export function parseProvidedTenant(d: unknown): ProvidedTenantView | null {
  if (
    !isObj(d) ||
    typeof d.granted !== "boolean" ||
    !numOrNull(d.ceiling_micro) ||
    typeof d.members_only !== "boolean" ||
    !num(d.member_default_micro) ||
    typeof d.month !== "string" ||
    !num(d.spent_micro) ||
    !num(d.reserved_micro) ||
    !Array.isArray(d.members) ||
    !d.members.every(isMember) ||
    !Array.isArray(d.reservations) ||
    !num(d.floor_micro)
  )
    return null;
  return d as unknown as ProvidedTenantView;
}

/** usd writes a micro-USD amount as dollars with cents; the caller adds "≈" (an estimate, D4). */
export const usd = (micro: number) => "$" + (micro / 1e6).toFixed(2);

/** usdInput is an amount as the editors show it: dollars without a sign, "" for none. */
export const usdInput = (micro: number | null) => (micro === null ? "" : String(micro / 1e6));

/** parseUsd reads an editor's dollars: "" is null (no amount), anything not a number at or above
 *  zero is undefined (refused before it is sent). */
export function parseUsd(s: string): number | null | undefined {
  const t = s.trim();
  if (t === "") return null;
  if (!/^\d+(\.\d+)?$/.test(t)) return undefined;
  return Math.round(Number(t) * 1e6);
}

/** modelsOf reads the source editor's model list: one id per line or comma. */
export const modelsOf = (text: string) =>
  text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
