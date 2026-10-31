// The one place a failed send turns into a toast. An `auth_expired` refusal (the Agent's
// promptBlocker: the workspace's Claude login is dead) gets a sticky toast with a
// Re-authenticate button, because a member with no session has no other way to the login
// flow: the transcript's error card only appears after a turn failed with a 401, and with the
// credentials already dead no turn ever starts. Every other failure stays a plain toast.
import type { ReactNode } from "react";
import { useSettingsUI } from "../features/settings/store.ts";
import { dismissToast } from "../ui/toast.ts";
import type { ToastOptions } from "../ui/ToastProvider.tsx";
import { agentOf } from "../agents/registry.ts";
import { useT } from "./i18n/index.ts";
import { kindDisplayName } from "./sessionkind.ts";

export const AUTH_EXPIRED_CODE = "auth_expired";
const TOAST_KEY = "auth-expired";
const INITIAL_PROMPT_TOAST_KEY = "initial-prompt-auth-expired";

type ToastSink = (message: ReactNode, opts?: ToastOptions) => unknown;

// An absent kind means the caller cannot tell which agent was refused; only claude's
// wording and card are honest defaults there (the refusal began as claude's).
const kindOrClaude = (kind?: string) => kind || "claude";

// Compared through the registry rather than a kind literal: ADR 0124 caps those outside it, and
// agentOf already resolves "no kind" (and junk) to claude.
const isClaude = (kind?: string) => agentOf(kind) === agentOf(undefined);

/** openAgentsSettings opens Settings on the Agents section, scrolled to the card of `kind`
 *  (default claude), where that agent's login lives. */
export function openAgentsSettings(kind?: string): void {
  useSettingsUI.getState().openSettings("agents", { target: kindOrClaude(kind) });
}

/** authExpiredText is the refusal sentence for `kind`. Claude keeps its own catalogue entry; any
 *  other kind gets the kind-named one. */
export function authExpiredText(tr: ReturnType<typeof useT>, kind?: string): string {
  return isClaude(kind) ? tr("err.auth_expired") : tr("err.auth_expired_kind", { agent: kindDisplayName(kind) });
}

/** initialPromptAuthExpiredText is the initial-prompt counterpart of authExpiredText. */
export function initialPromptAuthExpiredText(tr: ReturnType<typeof useT>, name: string, kind?: string): string {
  return isClaude(kind)
    ? tr("start.initial_prompt_auth_expired", { name })
    : tr("start.initial_prompt_auth_expired_kind", { name, agent: kindDisplayName(kind) });
}

export function AuthExpiredToast({ text, toastKey = TOAST_KEY, kind }: { text?: string; toastKey?: string; kind?: string }) {
  const tr = useT();
  return (
    <span className="update-toast">
      <span className="update-toast-txt">
        <span>{text ?? authExpiredText(tr, kind)}</span>
      </span>
      <button
        type="button"
        className="update-toast-btn"
        onClick={() => {
          dismissToast(toastKey);
          openAgentsSettings(kind);
        }}
      >
        {tr("mirror.error_auth_action")}
      </button>
    </span>
  );
}

/** toastSendFailure reports a rejected send. `message` is the already-localized reason
 *  (errText), `code` the wire code when the caller has one. */
export function toastSendFailure(
  toast: ToastSink,
  fail: { code?: string; message?: string },
  fallback: string,
  kind?: string,
): void {
  if (fail.code === AUTH_EXPIRED_CODE) {
    // Keyed per kind: a claude and a codex refusal at once are two different problems.
    const key = `${TOAST_KEY}:${kindOrClaude(kind)}`;
    toast(<AuthExpiredToast kind={kind} toastKey={key} />, { kind: "error", duration: 0, key });
    return;
  }
  toast(fail.message || fallback);
}


/** initialPromptToastKey is the sticky toast's key. It carries the (user, tenant) scope the
 *  failure was observed in: the same session name in another workspace is another failure, and
 *  its toast must neither replace nor be dismissed by this one. */
export const initialPromptToastKey = (scope: string, session: string) => `${INITIAL_PROMPT_TOAST_KEY}:${scope}:${session}`;

/** toastInitialPromptAuthExpired reports a create-time initial prompt the Agent refused to type
 *  because the agent's login (`kind`, default claude) has expired: nothing was sent, and no send of the member's own was
 *  involved, so no other path would tell them why the session sits idle. */
export function toastInitialPromptAuthExpired(
  toast: ToastSink,
  scope: string,
  session: string,
  text: string,
  kind?: string,
): void {
  const toastKey = initialPromptToastKey(scope, session);
  toast(<AuthExpiredToast text={text} toastKey={toastKey} kind={kind} />, { kind: "error", duration: 0, key: toastKey });
}
