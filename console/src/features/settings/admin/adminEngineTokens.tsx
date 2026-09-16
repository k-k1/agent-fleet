import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Button } from "../../../ui/Button.tsx";

// The deployment's upstream account tokens (Hugging Face, Civitai): one screen, its own rail
// item, rather than tucked below one engine's catalogue — a super_admin looking for "where do I
// register a token" should not have to open a model list first, and a deployment with zero
// engines still has to be able to say why a gated repository refuses (ADR 0072 decision 11:
// browsing needs no engine, no token and no bucket — but REGISTERING one should not need an
// engine to exist first either).
//
// Both panels are write-only for the same reason: the Control Plane holds `PutSecretValue` on
// each account's own secret and never `GetSecretValue`, so "show the current token" is not
// something either could offer even if a screen wanted it. What can be shown is that one is
// registered, by whom and when.
export function EngineTokensAdminView() {
  return (
    <div className="admin-stage">
      <HfTokenPanel />
      <CivitaiTokenPanel />
    </div>
  );
}

/** What the CP knows about the operator's Hugging Face token. Never the value: the CP cannot
 *  read the secret it writes, and it does not offer to unseal the stored copy for a screen. */
type HfTokenStatus = {
  /** false on a stack that predates P5, where the token was a CloudFormation parameter and
   *  there is nowhere for the CP to put one. */
  available?: boolean;
  configured?: boolean;
  /** That older stack HAS a token: nothing to register, and gated repositories work. Without
   *  this the same screen would have to read as "no token" and send somebody to fix what is
   *  not broken. */
  stack_token?: boolean;
  updated_by?: string;
  updated_at?: string;
};

/** Registering the operator's Hugging Face token (ADR 0072 decision 6 as revised, phase P5).
 *
 * One token for the whole deployment, so this sits on its own rail item rather than inside one
 * engine's catalogue: a single ingest task serves both roles, and a per-engine field would
 * suggest a choice that does not exist.
 *
 * The field is write-only, and that is not a UI convention here — it is what the deployment
 * can actually do. The CP holds `PutSecretValue` on one secret and never `GetSecretValue`, so
 * "show the current token" is not something it could offer even if a screen wanted it. What
 * can be shown is that one is registered, by whom and when. */
export function HfTokenPanel() {
  const tr = useT();
  const [st, setSt] = useState<HfTokenStatus | null>(null);
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    const d = await api("api/admin/engines/hf-token");
    if (d?.error) {
      setErr(errDetail(d.error));
      return;
    }
    setSt(d || {});
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/engines/hf-token", "PUT", { token });
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      // Cleared on success only: a token that was refused is still in the box to be corrected,
      // and retyping 40 characters because the deployment answered 502 is its own small insult.
      setToken("");
      setSt(d || {});
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/engines/hf-token", "DELETE");
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setSt(d || {});
    } finally {
      setBusy(false);
    }
  };

  if (!st) return null;
  return (
    <section className="admin-panel">
      <div className="usage-toolbar">
        <span>{tr("admin.engines_hf_token")}</span>
      </div>
      {st.available === false ? (
        <p className="muted">
          {tr(st.stack_token ? "admin.engines_hf_token_stack" : "admin.engines_hf_token_unsupported")}
        </p>
      ) : (
        <>
          <p className="muted">
            {st.configured
              ? tr("admin.engines_hf_token_set")
                  .replace("{who}", st.updated_by || "-")
                  .replace("{when}", st.updated_at ? fmtDateTime(st.updated_at) : "-")
              : tr("admin.engines_hf_token_unset")}
          </p>
          <label className="engines-hf-row">
            <span>{tr("admin.engines_hf_token_field")}</span>
            <input
              type="password"
              autoComplete="off"
              value={token}
              placeholder="hf_..."
              onChange={(ev) => setToken(ev.currentTarget.value)}
            />
          </label>
          <div className="engines-model-add-actions">
            <Button variant="primary" small disabled={busy || !token.trim()} onClick={save}>
              {tr("admin.engines_hf_token_save")}
            </Button>
            {st.configured && (
              <Button small disabled={busy} onClick={remove}>
                {tr("admin.engines_hf_token_remove")}
              </Button>
            )}
          </div>
          <p className="muted">{tr("admin.engines_hf_token_note")}</p>
        </>
      )}
      {err && <p className="form-err">{err}</p>}
    </section>
  );
}

/** The same shape as HfTokenStatus, for a Civitai account. There is no `stack_token` case:
 *  Civitai never had a CloudFormation parameter to fall back on, so an unavailable deployment
 *  simply has nowhere to register one yet. */
type CivitaiTokenStatus = {
  available?: boolean;
  configured?: boolean;
  updated_by?: string;
  updated_at?: string;
};

/** Registering the operator's Civitai token, the same shape as HfTokenPanel above and for the
 *  same reason: an account whose owner has accepted an uploader's terms, or paid for early
 *  access, is what turns a "login required" download into one the ingest task can fetch. */
export function CivitaiTokenPanel() {
  const tr = useT();
  const [st, setSt] = useState<CivitaiTokenStatus | null>(null);
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    const d = await api("api/admin/engines/civitai-token");
    if (d?.error) {
      setErr(errDetail(d.error));
      return;
    }
    setSt(d || {});
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/engines/civitai-token", "PUT", { token });
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setToken("");
      setSt(d || {});
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/engines/civitai-token", "DELETE");
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setSt(d || {});
    } finally {
      setBusy(false);
    }
  };

  if (!st) return null;
  return (
    <section className="admin-panel">
      <div className="usage-toolbar">
        <span>{tr("admin.engines_civitai_token")}</span>
      </div>
      {st.available === false ? (
        <p className="muted">{tr("admin.engines_civitai_token_unsupported")}</p>
      ) : (
        <>
          <p className="muted">
            {st.configured
              ? tr("admin.engines_civitai_token_set")
                  .replace("{who}", st.updated_by || "-")
                  .replace("{when}", st.updated_at ? fmtDateTime(st.updated_at) : "-")
              : tr("admin.engines_civitai_token_unset")}
          </p>
          <label className="engines-hf-row">
            <span>{tr("admin.engines_civitai_token_field")}</span>
            <input
              type="password"
              autoComplete="off"
              value={token}
              placeholder="..."
              onChange={(ev) => setToken(ev.currentTarget.value)}
            />
          </label>
          <div className="engines-model-add-actions">
            <Button variant="primary" small disabled={busy || !token.trim()} onClick={save}>
              {tr("admin.engines_civitai_token_save")}
            </Button>
            {st.configured && (
              <Button small disabled={busy} onClick={remove}>
                {tr("admin.engines_civitai_token_remove")}
              </Button>
            )}
          </div>
          <p className="muted">{tr("admin.engines_civitai_token_note")}</p>
        </>
      )}
      {err && <p className="form-err">{err}</p>}
    </section>
  );
}
