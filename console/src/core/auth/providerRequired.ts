// providerRequired — a module-level latch for "this tenant needs a different
// sign-in method" (docs/log/61 §61.9.4 + ADR0043 決定 18).
//
// A session carries exactly ONE identity provider, deliberately: letting a cookie
// hold several would turn it into a set of authorization states and make expiry
// and offboarding ambiguous. So moving between departments whose tenants accept
// different IdPs genuinely requires signing in again — and the Control Plane says
// so with the error code `provider_required` rather than a bare 403.
//
// Ending there would leave the person stuck: every request for that tenant fails,
// with no hint that the remedy is one click away. This latch carries the tenant
// (and the provider it wants) to a dialog that offers exactly that click.
//
// Deliberately mirrors authExpired.ts — same shape, no React, no ./client import
// (avoiding an import cycle).

export interface ProviderRequired {
  /** Tenant slug that refused the session. */
  tenant: string;
  /** A provider id that tenant accepts ("" when the server named none). */
  provider: string;
}

let pending: ProviderRequired | null = null;
const listeners = new Set<(p: ProviderRequired) => void>();

export function providerRequiredState(): ProviderRequired | null {
  return pending;
}

// signalProviderRequired latches the refusal and notifies subscribers. Unlike the
// auth-expiry latch this one can be re-armed for a DIFFERENT tenant: switching to
// another department is a normal thing to do after dismissing the dialog. A repeat
// for the same tenant is a no-op, so the flood of failing requests behind one
// refusal fires the dialog once.
export function signalProviderRequired(p: ProviderRequired): void {
  if (pending && pending.tenant === p.tenant) return;
  pending = p;
  for (const fn of listeners) {
    try {
      fn(p);
    } catch {
      /* a listener throwing must not block the others */
    }
  }
}

export function clearProviderRequired(): void {
  pending = null;
}

export function subscribeProviderRequired(fn: (p: ProviderRequired) => void): () => void {
  listeners.add(fn);
  if (pending) {
    try {
      fn(pending);
    } catch {
      /* ignore */
    }
  }
  return () => listeners.delete(fn);
}

// reloginForTenant sends the browser to the CP login for a specific tenant and
// provider, carrying ?next= so the person lands back where they were. When the
// server named no provider we fall back to the tenant's own login page, which
// shows exactly the buttons that tenant accepts (docs/log/61 §61.9.3).
export function reloginForTenant(p: ProviderRequired): void {
  const next = location.pathname + location.search;
  if (!p.provider) {
    const page = new URL("login/" + encodeURIComponent(p.tenant), document.baseURI).toString();
    location.assign(page + "?next=" + encodeURIComponent(next));
    return;
  }
  const login = new URL("oauth2/login", document.baseURI).toString();
  const q = new URLSearchParams({ provider: p.provider, tenant: p.tenant, next });
  location.assign(login + "?" + q.toString());
}
