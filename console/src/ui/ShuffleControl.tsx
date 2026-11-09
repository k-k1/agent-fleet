import { useState } from "react";
import { APPEARANCE_SHUFFLE_MODES, useSettings } from "../lib/settings.ts";
import { setAppearanceShuffle, shuffleAppearanceNow } from "../lib/appearanceShuffle.ts";
import { useT } from "../lib/i18n/index.ts";
import { Button } from "./Button.tsx";

// ShuffleControl: the random-colors schedule (segmented) plus the dice button that re-picks the
// colors at once. Shared by the appearance popover (TopBar) and the Display settings tab so the
// two stay in lockstep; `segClass` carries each host's segmented-control styling and `className`
// any spacing of its own.
export function ShuffleControl({ segClass, className }: { segClass: string; className?: string }) {
  const tr = useT();
  const s = useSettings();
  // Bumped on every press: the die is remounted under a new key so its roll animation restarts
  // even when pressed again mid-roll. 0 = never pressed, so nothing rolls on first render.
  const [rolls, setRolls] = useState(0);
  return (
    <div className={"shuffle-ctl" + (className ? " " + className : "")}>
      <div className={segClass}>
        {APPEARANCE_SHUFFLE_MODES.map((m) => (
          <button
            key={m.id}
            type="button"
            className={"seg-btn" + (s.appearanceShuffle === m.id ? " active" : "")}
            onClick={() => setAppearanceShuffle(m.id)}
          >
            {tr(m.labelKey)}
          </button>
        ))}
      </div>
      <Button
        className="shuffle-dice"
        title={tr("display.shuffle_now")}
        aria-label={tr("display.shuffle_now")}
        onClick={() => {
          setRolls((n) => n + 1);
          shuffleAppearanceNow();
        }}
      >
        <DiceIcon key={rolls} rolling={rolls > 0} />
      </Button>
    </div>
  );
}

// Codicons has no die, so this one is drawn inline in currentColor to follow the button's text.
function DiceIcon({ rolling }: { rolling: boolean }) {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" className={rolling ? "dice-roll" : undefined}>
      <rect x="1.5" y="1.5" width="13" height="13" rx="3" fill="none" stroke="currentColor" strokeWidth="1.3" />
      <circle cx="5" cy="5" r="1.2" fill="currentColor" />
      <circle cx="11" cy="5" r="1.2" fill="currentColor" />
      <circle cx="8" cy="8" r="1.2" fill="currentColor" />
      <circle cx="5" cy="11" r="1.2" fill="currentColor" />
      <circle cx="11" cy="11" r="1.2" fill="currentColor" />
    </svg>
  );
}
