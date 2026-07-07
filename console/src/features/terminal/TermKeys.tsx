// Mobile-only control-key strip for the terminal. Soft keyboards can't emit the
// keys a TUI needs — Esc, arrows, Tab, Ctrl-C — so we surface them as taps that
// inject the raw escape sequences into the PTY. CSS hides this strip on desktop.
// onMouseDown preventDefault keeps focus on the terminal's textarea so the soft
// keyboard doesn't dismiss between taps.
import { sendInput } from "../../terminal/service.ts";

const KEYS = [
  { label: "Esc", seq: "\x1b" },
  { label: "Tab", seq: "\t" },
  { label: "←", seq: "\x1b[D" },
  { label: "↑", seq: "\x1b[A" },
  { label: "↓", seq: "\x1b[B" },
  { label: "→", seq: "\x1b[C" },
  { label: "^C", seq: "\x03" },
  { label: "⏎", seq: "\r" },
];

export function TermKeys({ paneId }: { paneId: string }) {
  return (
    <div className="termkeys">
      {KEYS.map((k) => (
        <button
          key={k.label}
          type="button"
          className="termkey"
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => sendInput(paneId, k.seq)}
        >
          {k.label}
        </button>
      ))}
    </div>
  );
}
