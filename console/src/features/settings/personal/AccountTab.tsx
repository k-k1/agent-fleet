import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText, rel } from "../../../core/api/client.ts";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { getLocale, useT } from "../../../lib/i18n/index.ts";

// AccountTab — the sign-in methods linked to your own account, and adding a second one
// (docs/log/61 §61.16 + ADR0043 decision 37).
//
// Why this surface exists: a combination where two different IdPs claim the same email address
// is rejected at login (decision 32). Linking may only be opened by the account owner pressing
// the button themselves, so the entry point is this signed-in surface, not the login screen.
//
// The list shown here is a VIEW, not a gate (decision 14). The same rules that narrow the
// pressable methods live in the CP (handleOAuthLink / linkableFor) and decide the actual
// permission; the link itself only succeeds when it passes that method's gate (org, allowed
// domains).

interface LinkedMethod {
  provider: string;
  // provider alone is not enough to name a row: in principle one provider can have two
  // subjects. Both detaching and the React key use this pair.
  subject: string;
  email?: string;
  last_login_at?: string;
  current?: boolean;
  // Whether a method can be detached is answered by the CP (not the last one left, not the
  // method of the current session). This is only a copy of that answer and decides nothing —
  // the detach API checks the same rules again (decision 14).
  removable?: boolean;
  label_ja?: string;
  label_en?: string;
}

interface LinkableMethod {
  provider: string;
  label_ja?: string;
  label_en?: string;
  tenant?: string;
}

// The CP returns the label in both languages, by the same rules as the login buttons. Fall back
// to the id only when it is empty (a method dropped from the config, a row of a suspended
// tenant): hiding the row would hide a method that appeared without the user noticing.
function labelOf(m: { provider: string; label_ja?: string; label_en?: string }): string {
  const label = getLocale() === "en" ? m.label_en : m.label_ja;
  return label || m.provider;
}

export function AccountTab() {
  const tr = useT();
  const askConfirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [linked, setLinked] = useState<LinkedMethod[] | null>(null);
  const [linkable, setLinkable] = useState<LinkableMethod[]>([]);
  const [err, setErr] = useState("");

  const load = useCallback(() => {
    setErr("");
    api("api/me/login-methods")
      .then((res) => {
        if (res && res.error) {
          setErr(res.error.message || tr("account.load_failed"));
          return;
        }
        setEnabled(res.enabled !== false);
        setLinked(res.linked || []);
        setLinkable(res.linkable || []);
      })
      .catch(() => setErr(tr("account.load_failed")));
  }, [tr]);
  useEffect(load, [load]);

  // Linking is a round trip through the CP (/oauth2/link → IdP → /oauth2/callback). The result
  // is shown on a small page the CP renders, and the user returns from there — the Console is
  // left in the middle, so pass the current location as next.
  const startLink = (provider: string) => {
    const next = location.pathname + location.search;
    const q = new URLSearchParams({ provider, next });
    location.assign(rel("oauth2/link") + "?" + q.toString());
  };

  // Detach (docs/log/61 §61.16.4). provider / subject go in the query string: a tenant-defined
  // provider id is "t:<slug>:<name>", which contains ":" and cannot ride on a path segment.
  const detach = async (m: LinkedMethod) => {
    const ok = await askConfirm({
      title: tr("account.detach_title", { name: labelOf(m) }),
      body: tr("account.detach_body"),
      confirmLabel: tr("account.detach"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      const q = new URLSearchParams({ provider: m.provider, subject: m.subject });
      const res = await apiJSON("api/me/login-methods?" + q.toString(), "DELETE");
      if (res?.error) {
        setErr(errText(res.error));
        return;
      }
      load();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="display-settings">
      <p className="muted ds-note">{tr("account.intro")}</p>

      {!enabled && <p className="muted ds-note">{tr("account.disabled")}</p>}
      {err && <p className="muted pad">{err}</p>}

      {linked && linked.length > 0 && (
        <table className="pat-table account-methods">
          <thead>
            <tr>
              <th>{tr("account.th_method")}</th>
              <th>{tr("account.th_email")}</th>
              <th>{tr("account.th_last_login")}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {linked.map((m) => (
              <tr key={m.provider + "\u0000" + m.subject}>
                <td>
                  {labelOf(m)}
                  {m.current && <span className="muted"> — {tr("account.current")}</span>}
                </td>
                <td>{m.email || "—"}</td>
                <td>{fmtDate(m.last_login_at) || "—"}</td>
                <td className="allow-acts">
                  {/* Show the button even on a row that cannot be detached: removing it would
                      leave nowhere to read why. The reason goes in the title, and whether it
                      is pressable comes from the server's answer. */}
                  <button
                    type="button"
                    className="ghost xs danger"
                    disabled={busy || !m.removable}
                    title={m.removable ? undefined : m.current ? tr("account.detach_current") : tr("account.detach_last")}
                    onClick={() => detach(m)}
                  >
                    {tr("account.detach")}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {linked && linked.length === 0 && <p className="muted pad">{tr("account.none")}</p>}

      {enabled && linkable.length > 0 && (
        <section className="ds-group">
          <h4 className="ds-title">{tr("account.add_title")}</h4>
          <p className="muted ds-note">{tr("account.add_note")}</p>
          <div className="ds-row account-add">
            {linkable.map((m) => (
              <button key={m.provider} type="button" onClick={() => startLink(m.provider)}>
                {labelOf(m)}
                {m.tenant ? ` (${m.tenant})` : ""}
              </button>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

function fmtDate(s: string | undefined): string {
  if (!s) return "";
  const d = new Date(s);
  if (isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}
