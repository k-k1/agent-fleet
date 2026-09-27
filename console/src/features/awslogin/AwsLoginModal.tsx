// AwsLoginModal — the member's side of ADR 0102 decision 3. Nothing starts on open: the
// device code is created only when the member presses "Log in", and the modal shows the URL
// and code of the attempt its own press created, polled by that attempt id. If another start
// replaces it, the modal says so and never switches to the other attempt's code.
import { useEffect, useRef, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { api, apiJSON } from "../../core/api/client.ts";
import { DeviceCodeView } from "../sessions/DeviceCodeView.tsx";
import { useAwsLoginStore, waitersLine } from "./store.ts";

type Phase = "idle" | "starting" | "authorize" | "done" | "failed" | "replaced" | "cancelled" | "gone";

const POLL_MS = 1500;

export function AwsLoginModal({ id }: { id: string }) {
  const tr = useT();
  const req = useAwsLoginStore((s) => s.requests.find((r) => r.id === id));
  const close = useAwsLoginStore((s) => s.close);
  const refresh = useAwsLoginStore((s) => s.refresh);
  const [attempt, setAttempt] = useState("");
  const [phase, setPhase] = useState<Phase>("idle");
  const [url, setUrl] = useState("");
  const [code, setCode] = useState("");
  const [message, setMessage] = useState("");
  // The request can resolve while the modal is open (a login elsewhere). Remember what it was
  // so the finished modal can still say which profile it was about.
  const shown = useRef(req);
  if (req) shown.current = req;
  const r = shown.current;
  const hint = r ? `?profile=${encodeURIComponent(r.profile)}` : "";

  useEffect(() => {
    if (!attempt) return;
    let alive = true;
    let timer = 0;
    const poll = async () => {
      let d: { phase?: string; url?: string; code?: string; message?: string; error?: unknown } | null = null;
      try {
        d = await api(`api/aws-login/${encodeURIComponent(id)}/attempts/${encodeURIComponent(attempt)}`);
      } catch {
        d = null;
      }
      if (!alive) return;
      if (d && !d.error && d.phase) {
        const p = d.phase as Phase;
        setPhase(p);
        setUrl(p === "authorize" ? d.url || "" : "");
        setCode(p === "authorize" ? d.code || "" : "");
        setMessage(d.message || "");
        if (p === "done") {
          void refresh();
          return;
        }
        if (p !== "starting" && p !== "authorize") return;
      }
      timer = window.setTimeout(poll, POLL_MS);
    };
    timer = window.setTimeout(poll, 300);
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [attempt, id, refresh]);

  const start = async () => {
    setPhase("starting");
    setUrl("");
    setCode("");
    setMessage("");
    const d = await apiJSON(`api/aws-login/${encodeURIComponent(id)}/start${hint}`, "POST").catch(() => null);
    if (!d || d.error || !d.attempt) {
      setPhase("failed");
      setMessage((d?.error?.message as string) || "");
      return;
    }
    setAttempt(String(d.attempt));
  };

  const cancelRequest = async () => {
    await apiJSON(`api/aws-login/${encodeURIComponent(id)}/cancel${hint}`, "POST").catch(() => null);
    void refresh();
    close();
  };

  if (!r) {
    return (
      <Modal title={tr("awslogin.modal_title_generic")} onClose={close}>
        <div className="ui-modal-body">
          <p className="ui-field-hint">{tr("awslogin.not_pending")}</p>
        </div>
        <footer className="ui-modal-foot">
          <Button variant="ghost" onClick={close}>
            {tr("awslogin.close")}
          </Button>
        </footer>
      </Modal>
    );
  }

  const who = waitersLine(r);
  const running = phase === "starting" || phase === "authorize";
  return (
    <Modal title={tr("awslogin.modal_title", { profile: r.label || r.profile })} onClose={close}>
      <div className="ui-modal-body">
        <dl className="aws-login-facts">
          <dt>{tr("awslogin.field_profile")}</dt>
          <dd>{r.profile}</dd>
          <dt>{tr("awslogin.field_account")}</dt>
          <dd>{r.accountId}</dd>
          <dt>{tr("awslogin.field_role")}</dt>
          <dd>{r.roleName}</dd>
          {who && (
            <>
              <dt>{tr("awslogin.field_who")}</dt>
              <dd>{who}</dd>
            </>
          )}
        </dl>
        {phase === "idle" && <p className="ui-field-hint">{tr("awslogin.intro")}</p>}
        {phase === "starting" && <p className="ui-field-hint">{tr("awslogin.starting")}</p>}
        {phase === "authorize" && <DeviceCodeView url={url} code={code} hint={tr("awslogin.verify_hint")} />}
        {phase === "done" && <p className="ui-field-hint">{tr("awslogin.done")}</p>}
        {phase === "failed" && (
          <p className="ssm-error">
            {message === "unexpected sign-in URL" ? tr("awslogin.unexpected_url") : tr("awslogin.failed")}
            {message && message !== "unexpected sign-in URL" ? " " + message : ""}
          </p>
        )}
        {phase === "replaced" && <p className="ssm-error">{tr("awslogin.replaced")}</p>}
        {phase === "gone" && <p className="ssm-error">{tr("awslogin.gone")}</p>}
        {phase !== "done" && <p className="ui-field-hint">{tr("awslogin.close_hint")}</p>}
      </div>
      <footer className="ui-modal-foot">
        {phase !== "done" && (
          <Button variant="ghost" onClick={cancelRequest}>
            {tr("awslogin.cancel_request")}
          </Button>
        )}
        <Button variant="ghost" onClick={close}>
          {tr("awslogin.close")}
        </Button>
        {!running && phase !== "done" && (
          <Button variant="primary" onClick={start}>
            {phase === "idle" ? tr("awslogin.start") : tr("awslogin.retry")}
          </Button>
        )}
      </footer>
    </Modal>
  );
}
