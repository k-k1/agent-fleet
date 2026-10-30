// The parts the AWS and Google Cloud login toasts share: the identity the login is for, laid
// out so the member checks it before pressing Log in (#3023), and a countdown to a deadline the
// Agent measured.
import type { ReactNode } from "react";
import { useNow } from "../../lib/useNow.ts";
import { fmtCountdown } from "./countdown.ts";

export function ToastIdentity({ name, fields }: { name: string; fields: { label: string; value: ReactNode }[] }) {
  return (
    <span className="login-toast-id">
      <strong className="login-toast-name">{name}</strong>
      {fields.map((f) => (
        <span key={f.label} className="login-toast-field">
          <span className="login-toast-key">{f.label}</span>
          <span className="login-toast-val">{f.value}</span>
        </span>
      ))}
    </span>
  );
}

/** A line that counts down to deadline each second; nothing when there is no deadline. */
export function ToastCountdown({ deadline, text }: { deadline: number | null; text: (time: string) => string }) {
  if (deadline === null) return null;
  return <Ticking deadline={deadline} text={text} />;
}

// Split out so the one-second clock runs only while a countdown is on screen.
function Ticking({ deadline, text }: { deadline: number; text: (time: string) => string }) {
  // The clock only schedules the redraw; the time is read here so that a poll moving the
  // deadline between two ticks is not drawn against a now up to a second old.
  useNow(1000);
  const left = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
  // Hidden from assistive tech: the toast is a polite live region (role=status, atomic), so a
  // line rewritten every second would have the whole toast read out again every second.
  return (
    <span className={"login-toast-countdown" + (left < 60 ? " is-soon" : "")} aria-hidden="true">
      {text(fmtCountdown(left))}
    </span>
  );
}
