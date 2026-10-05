// useGcpLoginAttempt — one press of "Log in" and the gcloud login it started (ADR 0107
// decision 3). Nothing starts until start() is called. The attempt id comes only from the
// answer to that press and lives only in this hook: the poll asks for that id alone, the
// verification code is posted to that id alone, and an attempt seen anywhere else (a
// notification, a list) is never adopted. If another press replaces it, the phase says so
// and the view never switches to the other attempt.
import { useCallback, useEffect, useRef, useState } from "react";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";
import { api, apiJSON } from "../../core/api/client.ts";
import { Button } from "../../ui/Button.tsx";

export type GcpLoginPhase = "idle" | "starting" | "authorize" | "done" | "failed" | "replaced" | "cancelled" | "gone";

const POLL_MS = 1500;

export interface GcpLoginAttempt {
  /** Which press this is; the code form starts empty for each. */
  gen: number;
  phase: GcpLoginPhase;
  /** The checked sign-in URL, only while this tab's own attempt waits for the code. */
  url: string;
  message: string;
  /** The error code of a start the server refused ("" otherwise). */
  errorCode: string;
  running: boolean;
  /** A code was accepted for this attempt; gcloud is redeeming it. */
  submitted: boolean;
  /** Why the last submit was refused ("" when it was not). */
  submitError: string;
  start(): Promise<void>;
  submit(code: string): Promise<void>;
}

/** profilePath is api/gcp-login/profiles/<name>: every attempt is read and given its code there. */
export function useGcpLoginAttempt(startPath: string, profilePath: string, onDone: () => void): GcpLoginAttempt {
  const [attempt, setAttempt] = useState("");
  const [phase, setPhase] = useState<GcpLoginPhase>("idle");
  const [url, setUrl] = useState("");
  const [message, setMessage] = useState("");
  const [errorCode, setErrorCode] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const [submitError, setSubmitError] = useState("");
  // Bumped by every press: the code form is keyed by it, so a code typed for one attempt is
  // gone when another starts (it belongs to the gcloud whose URL produced it).
  const [gen, setGen] = useState(0);
  const latest = useRef({ profilePath, onDone });
  latest.current = { profilePath, onDone };

  useEffect(() => {
    if (!attempt) return;
    let alive = true;
    let timer = 0;
    const poll = async () => {
      let d: {
        phase?: string;
        url?: string;
        message?: string;
        error?: unknown;
      } | null = null;
      try {
        d = await api(`${latest.current.profilePath}/attempts/${encodeURIComponent(attempt)}`);
      } catch {
        d = null;
      }
      if (!alive) return;
      if (d && !d.error && d.phase) {
        const p = d.phase as GcpLoginPhase;
        setPhase(p);
        setUrl(p === "authorize" ? d.url || "" : "");
        setMessage(d.message || "");
        if (p === "done") {
          latest.current.onDone();
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
    setGen((g) => g + 1);
    setAttempt("");
    setPhase("starting");
    setUrl("");
    setMessage("");
    setErrorCode("");
    setSubmitted(false);
    setSubmitError("");
    const d = await apiJSON(startPath, "POST").catch(() => null);
    if (!d || d.error || !d.attempt) {
      setPhase("failed");
      setMessage((d?.error?.message as string) || "");
      setErrorCode((d?.error?.code as string) || "");
      return;
    }
    setAttempt(String(d.attempt));
  }, [startPath]);

  const submit = useCallback(
    async (code: string) => {
      const c = code.trim();
      if (!attempt || !c || submitted) return;
      setSubmitError("");
      setSubmitted(true);
      const d = await apiJSON(`${latest.current.profilePath}/attempts/${encodeURIComponent(attempt)}/code`, "POST", {
        code: c,
      }).catch(() => null);
      if (!d || d.error) {
        setSubmitted(false);
        setSubmitError((d?.error?.code as string) || "network");
      }
    },
    [attempt, submitted],
  );

  return {
    gen,
    phase,
    url,
    message,
    errorCode,
    running: phase === "starting" || phase === "authorize",
    submitted,
    submitError,
    start,
    submit,
  };
}

// The fixed messages the Agent ends an attempt with (gcpx/login_agent.go), in the member's words.
const ENDINGS: Record<string, MsgKey> = {
  "unexpected sign-in URL": "gcplogin.unexpected_url",
  "gcloud asked a question the Console login does not answer (on a Compute Engine VM: whether to use a personal account); log in from a terminal":
    "gcplogin.gce_prompt",
  "the profile changed in Settings during the login; start it again": "gcplogin.profile_changed",
  "gcloud printed no sign-in URL": "gcplogin.no_url",
};

const SUBMIT_ERRORS: Record<string, MsgKey> = {
  bad_code: "gcplogin.err_bad_code",
  not_awaiting_code: "gcplogin.err_not_awaiting",
  not_found: "gcplogin.err_not_awaiting",
  busy: "gcplogin.err_busy",
  profile_changed: "gcplogin.profile_changed",
};

/** The attempt in the modal body: the sign-in link and the single code field, only for this tab's own attempt. */
export function GcpLoginAttemptView({ a, done, failed }: { a: GcpLoginAttempt; done: string; failed?: string }) {
  const tr = useT();
  const ending = ENDINGS[a.message];
  return (
    <>
      {a.phase === "starting" && <p className="ui-field-hint">{tr("gcplogin.starting")}</p>}
      {a.phase === "authorize" && (
        <div className="gcp-login-authorize">
          <p className="ui-field-hint">{tr("gcplogin.authorize_hint")}</p>
          <div>
            <Button
              variant="primary"
              icon="link-external"
              onClick={() => window.open(a.url, "_blank", "noopener,noreferrer")}
            >
              {tr("gcplogin.open_sign_in")}
            </Button>
          </div>
          <p className="gcp-login-url">
            <code>{a.url}</code>
          </p>
          <p className="ui-field-hint gcp-login-warn">{tr("gcplogin.paste_rule")}</p>
          <CodeForm key={a.gen} a={a} />
          {a.submitted && <p className="ui-field-hint">{tr("gcplogin.checking")}</p>}
          {a.submitError && <p className="ssm-error">{tr(SUBMIT_ERRORS[a.submitError] ?? "gcplogin.err_submit")}</p>}
        </div>
      )}
      {a.phase === "done" && <p className="ui-field-hint">{done}</p>}
      {a.phase === "failed" && (
        <p className="ssm-error">
          {failed ?? (ending ? tr(ending) : tr("gcplogin.failed") + (a.message ? " " + a.message : ""))}
        </p>
      )}
      {a.phase === "replaced" && <p className="ssm-error">{tr("gcplogin.replaced")}</p>}
      {a.phase === "cancelled" && <p className="ssm-error">{tr("gcplogin.cancelled")}</p>}
      {a.phase === "gone" && <p className="ssm-error">{tr("gcplogin.gone")}</p>}
    </>
  );
}

/** The single code field. It lives only while its own attempt waits for a code, and is keyed
 *  by the press, so a code never carries over to another attempt. */
function CodeForm({ a }: { a: GcpLoginAttempt }) {
  const tr = useT();
  const [code, setCode] = useState("");
  return (
    <form
      className="gcp-login-code"
      onSubmit={(e) => {
        e.preventDefault();
        void a.submit(code);
      }}
    >
      <label className="ui-field-label" htmlFor="gcp-login-code">
        {tr("gcplogin.code_label")}
      </label>
      <input
        id="gcp-login-code"
        className="cinput"
        autoComplete="off"
        spellCheck={false}
        value={code}
        disabled={a.submitted}
        onChange={(e) => setCode(e.target.value)}
      />
      <Button variant="primary" type="submit" disabled={a.submitted || !code.trim()}>
        {tr("gcplogin.submit")}
      </Button>
    </form>
  );
}
