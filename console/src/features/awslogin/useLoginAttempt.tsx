// useLoginAttempt — one press of "Log in" and the attempt it created (ADR 0102 decision 3).
// Nothing starts until start() is called, and the poll asks for that attempt id only: if
// another start replaces it, the phase says so and the view never switches to the other
// attempt's code. Both the request modal and the Settings row's modal (#1028) use it; they
// differ only in the routes.
import { useCallback, useEffect, useRef, useState } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { api, apiJSON } from "../../core/api/client.ts";
import { DeviceCodeView } from "../sessions/DeviceCodeView.tsx";

export type LoginPhase = "idle" | "starting" | "authorize" | "done" | "failed" | "replaced" | "cancelled" | "gone";

const POLL_MS = 1500;

export interface LoginAttempt {
  phase: LoginPhase;
  url: string;
  code: string;
  message: string;
  /** The error code of a start the server refused ("" otherwise). */
  errorCode: string;
  running: boolean;
  start(): Promise<void>;
}

export function useLoginAttempt(startPath: string, attemptPath: (attempt: string) => string, onDone: () => void): LoginAttempt {
  const [attempt, setAttempt] = useState("");
  const [phase, setPhase] = useState<LoginPhase>("idle");
  const [url, setUrl] = useState("");
  const [code, setCode] = useState("");
  const [message, setMessage] = useState("");
  const [errorCode, setErrorCode] = useState("");
  // Callers rebuild these on every render; the poll only needs the latest.
  const paths = useRef({ attemptPath, onDone });
  paths.current = { attemptPath, onDone };

  useEffect(() => {
    if (!attempt) return;
    let alive = true;
    let timer = 0;
    const poll = async () => {
      let d: { phase?: string; url?: string; code?: string; message?: string; error?: unknown } | null = null;
      try {
        d = await api(paths.current.attemptPath(attempt));
      } catch {
        d = null;
      }
      if (!alive) return;
      if (d && !d.error && d.phase) {
        const p = d.phase as LoginPhase;
        setPhase(p);
        setUrl(p === "authorize" ? d.url || "" : "");
        setCode(p === "authorize" ? d.code || "" : "");
        setMessage(d.message || "");
        if (p === "done") {
          paths.current.onDone();
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
  }, [attempt]);

  const start = useCallback(async () => {
    setPhase("starting");
    setUrl("");
    setCode("");
    setMessage("");
    setErrorCode("");
    const d = await apiJSON(startPath, "POST").catch(() => null);
    if (!d || d.error || !d.attempt) {
      setPhase("failed");
      setMessage((d?.error?.message as string) || "");
      setErrorCode((d?.error?.code as string) || "");
      return;
    }
    setAttempt(String(d.attempt));
  }, [startPath]);

  return { phase, url, code, message, errorCode, running: phase === "starting" || phase === "authorize", start };
}

/** What the attempt is doing, in the modal body. `verifyHint` and `done` are the caller's
 *  words: a request has a command waiting on it, a Settings row does not. */
export function LoginAttemptStatus({
  a,
  verifyHint,
  done,
  failed,
}: {
  a: LoginAttempt;
  verifyHint: string;
  done: string;
  /** Overrides the failure line (a start the server refused for a known reason). */
  failed?: string;
}) {
  const tr = useT();
  const unexpected = a.message === "unexpected sign-in URL";
  return (
    <>
      {a.phase === "starting" && <p className="ui-field-hint">{tr("awslogin.starting")}</p>}
      {a.phase === "authorize" && <DeviceCodeView url={a.url} code={a.code} hint={verifyHint} />}
      {a.phase === "done" && <p className="ui-field-hint">{done}</p>}
      {a.phase === "failed" && (
        <p className="ssm-error">
          {failed ??
            (unexpected ? tr("awslogin.unexpected_url") : tr("awslogin.failed") + (a.message ? " " + a.message : ""))}
        </p>
      )}
      {a.phase === "replaced" && <p className="ssm-error">{tr("awslogin.replaced")}</p>}
      {a.phase === "gone" && <p className="ssm-error">{tr("awslogin.gone")}</p>}
    </>
  );
}
