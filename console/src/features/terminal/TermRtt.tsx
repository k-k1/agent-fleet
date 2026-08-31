// TermRtt — how far away this pane's PTY is, in milliseconds.
//
// Why a terminal shows a latency figure at all: everything about a remote terminal is
// judged by the delay between a keypress and its echo, and until this chip existed that
// delay was the one property of the product with no observable quantity anywhere — a
// report of "typing is slow" could only be answered by guessing between the browser's
// link, the Control-Plane relay and the workspace. The number here is measured over the
// SAME socket and the SAME frames the keystrokes use (term.ts: the app-level ping the
// Agent echoes), so it covers the whole path except the PTY/tmux hop itself, which is
// sub-millisecond. If this chip reads 300 ms, the echo is 300 ms, and it is not the
// workspace's fault.
//
// It is deliberately unobtrusive rather than hidden behind a threshold: "it is fine
// right now" is exactly as load-bearing an observation as "it is bad right now", and a
// chip that only appears when things are bad cannot make the first one.
import { useEffect, useState } from "react";
import { onTermRtt, type RttStats } from "../../terminal/service.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Icon } from "../../ui/Icon.tsx";

// Thresholds for the colour, in round-trip ms. Below WARN a terminal feels local;
// past BAD every keystroke is visibly behind the finger.
const RTT_WARN = 120;
const RTT_BAD = 300;

function rttClass(ms: number) {
  if (ms >= RTT_BAD) return "term-rtt-bad";
  if (ms >= RTT_WARN) return "term-rtt-warn";
  return "term-rtt-ok";
}

export function TermRtt({ paneId }: { paneId: string }) {
  const tr = useT();
  const [rtt, setRtt] = useState<RttStats | null>(null);
  useEffect(() => onTermRtt(paneId, setRtt), [paneId]);
  if (!rtt) return null;
  // The median is what the chip shows: a single sample is dominated by whichever
  // scheduler/GC/retransmit it happened to land on, and the complaint being answered
  // is about the typical keystroke, not the worst one. The worst one is in the tip.
  const ms = Math.round(rtt.med);
  return (
    <span
      className={"term-rtt " + rttClass(ms)}
      title={tr("onb.rtt_title", { med: ms, max: Math.round(rtt.max), n: rtt.n })}
    >
      <Icon name="pulse" />
      {ms}
      {tr("onb.rtt_unit")}
    </span>
  );
}
